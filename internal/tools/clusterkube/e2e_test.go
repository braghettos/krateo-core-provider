//go:build e2e

// Package clusterkube e2e validation. Runs only with -tags e2e and requires two real
// clusters via env vars:
//
//	MGMT_KUBECONFIG   - path to the management cluster kubeconfig (e.g. a local kind)
//	TARGET_KUBECONFIG - path to a self-contained (bearer-token) kubeconfig for the remote
//	                    target cluster (e.g. a GKE ServiceAccount-token kubeconfig)
//
// It validates the cross-cluster targeting that unit tests cannot: that clusterkube
// builds working clients for a remote cluster from a kubeconfig Secret living in the
// management cluster, that resources land in the TARGET (not the management cluster),
// and that a remote NoneConverter multi-version CRD is accepted by the real apiserver.
package clusterkube

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	crdclient "github.com/krateoplatformops/core-provider/internal/tools/crd"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func mgmtClient(t *testing.T) client.Client {
	t.Helper()
	rc, err := clientcmd.BuildConfigFromFlags("", os.Getenv("MGMT_KUBECONFIG"))
	if err != nil {
		t.Fatalf("building mgmt rest config: %v", err)
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	cl, err := client.New(rc, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building mgmt client: %v", err)
	}
	return cl
}

func uniqueSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func TestE2E_RemoteTargeting(t *testing.T) {
	if os.Getenv("MGMT_KUBECONFIG") == "" || os.Getenv("TARGET_KUBECONFIG") == "" {
		t.Skip("MGMT_KUBECONFIG and TARGET_KUBECONFIG must be set")
	}
	ctx := context.Background()
	mgmt := mgmtClient(t)

	targetKubeconfig, err := os.ReadFile(os.Getenv("TARGET_KUBECONFIG"))
	if err != nil {
		t.Fatalf("reading target kubeconfig: %v", err)
	}

	// 1. Store the target kubeconfig as a native Secret in the management cluster.
	secretName := "e2e-target-kubeconfig"
	secret := corev1Secret(secretName, "default", targetKubeconfig)
	if err := mgmt.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating kubeconfig secret: %v", err)
	}
	t.Cleanup(func() { _ = mgmt.Delete(ctx, secret) })

	ref := &rtv1.SecretKeySelector{Key: "kubeconfig"}
	ref.Name = secretName
	ref.Namespace = "default"
	target := &compositiondefinitionsv1alpha1.DeploymentTarget{
		Mode:          compositiondefinitionsv1alpha1.DeploymentModeRemote,
		KubeconfigRef: ref,
	}

	// 2. Build remote clients from the Secret (the real feature code path).
	clients, err := Remote(ctx, mgmt, target)
	if err != nil {
		t.Fatalf("clusterkube.Remote: %v", err)
	}
	if clients.SecretResourceVersion == "" {
		t.Fatal("expected SecretResourceVersion to be captured")
	}

	// 3. The clients must actually reach the TARGET apiserver.
	ver, err := clients.Clientset.Discovery().ServerVersion()
	if err != nil {
		t.Fatalf("target ServerVersion: %v", err)
	}
	t.Logf("target cluster reachable, version=%s, secretRV=%s", ver.GitVersion, clients.SecretResourceVersion)

	// 4. Drive the REAL feature helper crd.ApplyOrUpdateCRD against the remote target,
	//    exactly as Create()/Update() do. First a single version creates the CRD; a second
	//    version exercises the AppendVersion + injectConversionConfToCRD remote path, which
	//    must produce a NoneConverter (no CORE_PROVIDER_WEBHOOK_URL) the real apiserver
	//    accepts and establishes.
	group := fmt.Sprintf("e2e%s.krateo.io", uniqueSuffix())
	crdName := "widgets." + group
	t.Cleanup(func() {
		c := &apiextensionsv1.CustomResourceDefinition{}
		c.Name = crdName
		_ = clients.Kube.Delete(ctx, c)
	})

	opts := crdclient.ApplyOpts{Remote: true} // remote, no webhook URL -> NoneConverter
	if _, err := crdclient.ApplyOrUpdateCRD(ctx, clients.Kube, clients.Dynamic, widgetCRD(group, "v1alpha1"), opts); err != nil {
		t.Fatalf("ApplyOrUpdateCRD (v1alpha1) on target: %v", err)
	}
	if _, err := crdclient.ApplyOrUpdateCRD(ctx, clients.Kube, clients.Dynamic, widgetCRD(group, "v1alpha2"), opts); err != nil {
		t.Fatalf("ApplyOrUpdateCRD (v1alpha2) on target: %v", err)
	}

	if err := waitEstablished(ctx, clients.Kube, crdName, 90*time.Second); err != nil {
		t.Fatalf("CRD not established on target: %v", err)
	}

	// 5a. The CRD exists in the TARGET, multi-version, with NoneConverter strategy.
	gotTarget := &apiextensionsv1.CustomResourceDefinition{}
	if err := clients.Kube.Get(ctx, types.NamespacedName{Name: crdName}, gotTarget); err != nil {
		t.Fatalf("getting CRD from target: %v", err)
	}
	if gotTarget.Spec.Conversion == nil || gotTarget.Spec.Conversion.Strategy != apiextensionsv1.NoneConverter {
		t.Fatalf("expected NoneConverter on target CRD, got %+v", gotTarget.Spec.Conversion)
	}
	served := 0
	for _, v := range gotTarget.Spec.Versions {
		if v.Name == "v1alpha1" || v.Name == "v1alpha2" {
			served++
		}
	}
	if served != 2 {
		t.Fatalf("expected both v1alpha1 and v1alpha2 on the target CRD, versions=%+v", gotTarget.Spec.Versions)
	}

	// 5b. Isolation: the CRD must NOT exist in the management cluster.
	gotMgmt := &apiextensionsv1.CustomResourceDefinition{}
	err = mgmt.Get(ctx, types.NamespacedName{Name: crdName}, gotMgmt)
	if err == nil {
		t.Fatalf("CRD leaked into the management cluster: %s", crdName)
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected error checking management cluster: %v", err)
	}

	t.Logf("OK: CRD %s applied via crd.ApplyOrUpdateCRD (remote, NoneConverter), established on target, absent in management", crdName)
}

func corev1Secret(name, namespace string, kubeconfig []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string][]byte{"kubeconfig": kubeconfig},
	}
}

// widgetCRD builds a single-version CRD for the given group/version with a spec and a
// status property. The status schema is identical across versions so that
// generation.StatusEqual passes and ApplyOrUpdateCRD takes the AppendVersion +
// injectConversionConfToCRD path on the second apply.
func widgetCRD(group, version string) *apiextensionsv1.CustomResourceDefinition {
	schema := apiextensionsv1.CustomResourceValidation{
		OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"spec":   {Type: "object", XPreserveUnknownFields: ptrBool(true)},
				"status": {Type: "object", XPreserveUnknownFields: ptrBool(true)},
			},
		},
	}
	return &apiextensionsv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apiextensions.k8s.io/v1",
			Kind:       "CustomResourceDefinition",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "widgets." + group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "widgets",
				Singular: "widget",
				Kind:     "Widget",
				ListKind: "WidgetList",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: version, Served: true, Storage: true, Schema: &schema},
			},
		},
	}
}

func ptrBool(b bool) *bool { return &b }

func waitEstablished(ctx context.Context, cl client.Client, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := cl.Get(ctx, types.NamespacedName{Name: name}, crd); err == nil {
			for _, c := range crd.Status.Conditions {
				if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %s to be established", name)
}
