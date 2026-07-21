//go:build e2e

// Package compositionmirror e2e validation. Runs only with -tags e2e and requires two real
// clusters via env vars:
//
//	MGMT_KUBECONFIG   - path to the management/hub cluster kubeconfig (e.g. a local kind)
//	TARGET_KUBECONFIG - path to a self-contained (bearer-token) kubeconfig for the remote
//	                    spoke cluster (e.g. a disposable GKE ServiceAccount-token kubeconfig)
//
// It validates what the unit tests cannot: that the reflector's reflection engine, run against
// two REAL apiservers, mirrors a hub Composition onto the spoke, reads spoke status back onto the
// hub, and garbage-collects only the spoke mirrors it owns — never an unmanaged spoke instance.
// The composition CRD is applied to BOTH clusters (increment 1's hub+spoke shape). Neither cluster
// is the caller's active kubeconfig context: both are addressed only through these explicit
// kubeconfig files. Provision + teardown is handled by scripts/e2e-remote-composition-mirror.sh.
package compositionmirror

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	e2eNamespace  = "default"
	e2eGroup      = "composition.krateo.io"
	e2eVersion    = "v1-0-0"
	e2eKind       = "Portal"
	e2ePlural     = "portals"
	e2eAPIVersion = e2eGroup + "/" + e2eVersion
)

var e2eGVR = schema.GroupVersionResource{Group: e2eGroup, Version: e2eVersion, Resource: e2ePlural}

// clientsFor builds a typed client (CRD apply/wait) and a dynamic client (instances) for the
// cluster behind the given kubeconfig path. Only the explicit kubeconfig is used — never the
// process/active context.
func clientsFor(t *testing.T, kubeconfigEnv string) (client.Client, dynamic.Interface) {
	t.Helper()
	path := os.Getenv(kubeconfigEnv)
	rc, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("building rest config from %s=%q: %v", kubeconfigEnv, path, err)
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	cl, err := client.New(rc, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building typed client for %s: %v", kubeconfigEnv, err)
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		t.Fatalf("building dynamic client for %s: %v", kubeconfigEnv, err)
	}
	return cl, dyn
}

func TestE2E_RemoteCompositionMirror(t *testing.T) {
	if os.Getenv("MGMT_KUBECONFIG") == "" || os.Getenv("TARGET_KUBECONFIG") == "" {
		t.Skip("MGMT_KUBECONFIG and TARGET_KUBECONFIG must be set")
	}
	ctx := context.Background()
	log := logging.NewNopLogger()

	hubCl, hubDyn := clientsFor(t, "MGMT_KUBECONFIG")
	spokeCl, spokeDyn := clientsFor(t, "TARGET_KUBECONFIG")

	// 1. The composition CRD must exist on BOTH clusters (increment 1: hub authoring + spoke render).
	crd := compositionCRD()
	for _, tc := range []struct {
		name string
		cl   client.Client
	}{{"hub", hubCl}, {"spoke", spokeCl}} {
		c := crd.DeepCopy()
		if err := tc.cl.Create(ctx, c); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating composition CRD on %s: %v", tc.name, err)
		}
		if err := waitEstablished(ctx, tc.cl, crd.Name, 120*time.Second); err != nil {
			t.Fatalf("composition CRD not established on %s: %v", tc.name, err)
		}
	}
	t.Cleanup(func() {
		c := &apiextensionsv1.CustomResourceDefinition{}
		c.Name = crd.Name
		_ = hubCl.Delete(ctx, c)
		_ = spokeCl.Delete(ctx, c)
	})

	hubRes := hubDyn.Resource(e2eGVR).Namespace(e2eNamespace)
	spokeRes := spokeDyn.Resource(e2eGVR).Namespace(e2eNamespace)

	// 2. Author a desired Composition on the HUB. Seed the SPOKE with a managed orphan (no hub
	//    counterpart -> must be GC'd) and an unmanaged foreign instance (must survive).
	if _, err := hubRes.Create(ctx, e2ePortal("portal-a", map[string]interface{}{"tenant": "acme"}, false), metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating hub portal-a: %v", err)
	}
	if _, err := spokeRes.Create(ctx, e2ePortal("portal-orphan", map[string]interface{}{"tenant": "gone"}, true), metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating spoke orphan: %v", err)
	}
	if _, err := spokeRes.Create(ctx, e2ePortal("portal-foreign", map[string]interface{}{"tenant": "keep"}, false), metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating spoke foreign: %v", err)
	}

	params := reflectParams{
		hub: hubDyn, spoke: spokeDyn, gvr: e2eGVR,
		apiVersion: e2eAPIVersion, kind: e2eKind, namespace: e2eNamespace, log: log,
	}

	// 3. First reflection pass: mirror down + GC.
	if err := reflectInstances(ctx, params); err != nil {
		t.Fatalf("reflectInstances (pass 1): %v", err)
	}

	// portal-a mirrored to the spoke, stamped managed, spec carried over.
	a, err := spokeRes.Get(ctx, "portal-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("portal-a not mirrored to spoke: %v", err)
	}
	if a.GetLabels()[managedByLabel] != managedByValue {
		t.Errorf("spoke portal-a missing management label")
	}
	if v, _, _ := unstructured.NestedString(a.Object, "spec", "tenant"); v != "acme" {
		t.Errorf("spoke portal-a spec.tenant = %q, want acme", v)
	}
	// orphan garbage-collected.
	if _, err := spokeRes.Get(ctx, "portal-orphan", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("spoke portal-orphan should be GC'd, got err=%v", err)
	}
	// unmanaged foreign survived.
	if _, err := spokeRes.Get(ctx, "portal-foreign", metav1.GetOptions{}); err != nil {
		t.Errorf("spoke portal-foreign (unmanaged) must survive GC, got err=%v", err)
	}

	// 4. Simulate the spoke cdc writing status, then a second pass reads it back onto the hub.
	a, err = spokeRes.Get(ctx, "portal-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-reading spoke portal-a: %v", err)
	}
	_ = unstructured.SetNestedField(a.Object, "deployed", "status", "phase")
	if _, err := spokeRes.UpdateStatus(ctx, a, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("seeding spoke portal-a status: %v", err)
	}

	if err := reflectInstances(ctx, params); err != nil {
		t.Fatalf("reflectInstances (pass 2): %v", err)
	}

	hb, err := hubRes.Get(ctx, "portal-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading hub portal-a: %v", err)
	}
	if phase, _, _ := unstructured.NestedString(hb.Object, "status", "phase"); phase != "deployed" {
		t.Errorf("hub portal-a status.phase = %q, want deployed (readback)", phase)
	}

	t.Logf("OK: hub Composition mirrored to spoke (managed), orphan GC'd, foreign spared, spoke status read back to hub")
}

func e2ePortal(name string, spec map[string]interface{}, managed bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(e2eAPIVersion)
	u.SetKind(e2eKind)
	u.SetNamespace(e2eNamespace)
	u.SetName(name)
	if spec != nil {
		_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	}
	if managed {
		u.SetLabels(map[string]string{managedByLabel: managedByValue})
	}
	return u
}

// compositionCRD builds a namespaced Portal CRD with a status subresource (so status readback via
// UpdateStatus works on a real apiserver) and open spec/status schemas.
func compositionCRD() *apiextensionsv1.CustomResourceDefinition {
	preserve := true
	schema := apiextensionsv1.CustomResourceValidation{
		OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"spec":   {Type: "object", XPreserveUnknownFields: &preserve},
				"status": {Type: "object", XPreserveUnknownFields: &preserve},
			},
		},
	}
	return &apiextensionsv1.CustomResourceDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "CustomResourceDefinition"},
		ObjectMeta: metav1.ObjectMeta{Name: e2ePlural + "." + e2eGroup},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: e2eGroup,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: e2ePlural, Singular: "portal", Kind: e2eKind, ListKind: e2eKind + "List",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: e2eVersion, Served: true, Storage: true, Schema: &schema,
				Subresources: &apiextensionsv1.CustomResourceSubresources{Status: &apiextensionsv1.CustomResourceSubresourceStatus{}},
			}},
		},
	}
}

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
