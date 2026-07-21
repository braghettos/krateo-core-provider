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
	"strings"
	"testing"
	"time"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	corev1 "k8s.io/api/core/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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
	return e2ePortalIn(e2eAPIVersion, e2eKind, name, spec, managed)
}

func e2ePortalIn(apiVersion, kind, name string, spec map[string]interface{}, managed bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
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

// compositionCRD builds the default Portal composition CRD.
func compositionCRD() *apiextensionsv1.CustomResourceDefinition {
	return compositionCRDNamed(e2eGroup, e2eVersion, e2ePlural, e2eKind)
}

// compositionCRDNamed builds a namespaced composition CRD for the given identity, with a status
// subresource (so status readback via UpdateStatus works on a real apiserver) and open schemas.
func compositionCRDNamed(group, version, plural, kind string) *apiextensionsv1.CustomResourceDefinition {
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
		ObjectMeta: metav1.ObjectMeta{Name: plural + "." + group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: plural, Singular: strings.ToLower(kind), Kind: kind, ListKind: kind + "List",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: version, Served: true, Storage: true, Schema: &schema,
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

// coreHubClient is a typed client for the hub that also knows the core.krateo.io kinds
// (CompositionDefinition, KubernetesTarget) the reflector's teardown path resolves.
func coreHubClient(t *testing.T) client.Client {
	t.Helper()
	rc, err := clientcmd.BuildConfigFromFlags("", os.Getenv("MGMT_KUBECONFIG"))
	if err != nil {
		t.Fatalf("building hub rest config: %v", err)
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = compositiondefinitionsv1alpha1.SchemeBuilder.AddToScheme(scheme)
	cl, err := client.New(rc, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building core hub client: %v", err)
	}
	return cl
}

// coreCRD builds a minimal (open-schema) core.krateo.io CRD so the typed CompositionDefinition /
// KubernetesTarget objects the reflector reads can exist on the hub. CompositionDefinition needs a
// status subresource (the test records the generated GVK on status).
func coreCRD(plural, kind string, withStatus bool) *apiextensionsv1.CustomResourceDefinition {
	preserve := true
	props := map[string]apiextensionsv1.JSONSchemaProps{
		"spec": {Type: "object", XPreserveUnknownFields: &preserve},
	}
	ver := apiextensionsv1.CustomResourceDefinitionVersion{Name: "v1alpha1", Served: true, Storage: true}
	if withStatus {
		props["status"] = apiextensionsv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: &preserve}
		ver.Subresources = &apiextensionsv1.CustomResourceSubresources{Status: &apiextensionsv1.CustomResourceSubresourceStatus{}}
	}
	ver.Schema = &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: props}}
	return &apiextensionsv1.CustomResourceDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "CustomResourceDefinition"},
		ObjectMeta: metav1.ObjectMeta{Name: plural + ".core.krateo.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "core.krateo.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: plural, Singular: strings.ToLower(kind), Kind: kind, ListKind: kind + "List",
			},
			Scope:    apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{ver},
		},
	}
}

func applyAndWaitCRD(ctx context.Context, t *testing.T, cl client.Client, crd *apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	if err := cl.Create(ctx, crd); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating CRD %s: %v", crd.Name, err)
	}
	if err := waitEstablished(ctx, cl, crd.Name, 120*time.Second); err != nil {
		t.Fatalf("CRD %s not established: %v", crd.Name, err)
	}
	t.Cleanup(func() {
		c := &apiextensionsv1.CustomResourceDefinition{}
		c.Name = crd.Name
		_ = cl.Delete(context.Background(), c)
	})
}

// TestE2E_RemoteCompositionMirrorTeardown validates the CD-finalizer teardown (PR #53) against real
// clusters by driving the actual Reconcile: a live remote CompositionDefinition gains the finalizer
// and mirrors its hub Composition to the spoke; deleting the CD then removes BOTH the hub Composition
// and its spoke mirror and releases the finalizer so the CD completes deletion. This exercises the
// real reconcileDelete orchestration (finalizer + clusterkube.Remote from the CD + cross-cluster
// delete), which fakes cannot.
func TestE2E_RemoteCompositionMirrorTeardown(t *testing.T) {
	if os.Getenv("MGMT_KUBECONFIG") == "" || os.Getenv("TARGET_KUBECONFIG") == "" {
		t.Skip("MGMT_KUBECONFIG and TARGET_KUBECONFIG must be set")
	}
	ctx := context.Background()

	// A distinct Kind (own group) so this test never shares a CRD with TestE2E_RemoteCompositionMirror
	// — the two run sequentially against the same clusters, and a shared CRD's teardown would race.
	const tdGroup = "tdmirror.krateo.io"
	tdAPIVersion := tdGroup + "/" + e2eVersion
	tdGVR := schema.GroupVersionResource{Group: tdGroup, Version: e2eVersion, Resource: e2ePlural}

	hubCl := coreHubClient(t)
	_, hubDyn := clientsFor(t, "MGMT_KUBECONFIG")
	spokeCl, spokeDyn := clientsFor(t, "TARGET_KUBECONFIG")

	// CRDs: this test's composition Kind on both clusters; the core kinds on the hub.
	applyAndWaitCRD(ctx, t, hubCl, compositionCRDNamed(tdGroup, e2eVersion, e2ePlural, e2eKind))
	applyAndWaitCRD(ctx, t, spokeCl, compositionCRDNamed(tdGroup, e2eVersion, e2ePlural, e2eKind))
	applyAndWaitCRD(ctx, t, hubCl, coreCRD("compositiondefinitions", "CompositionDefinition", true))
	applyAndWaitCRD(ctx, t, hubCl, coreCRD("kubernetestargets", "KubernetesTarget", false))

	// KubernetesTarget + kubeconfig Secret on the hub, pointing at the spoke.
	targetKubeconfig, err := os.ReadFile(os.Getenv("TARGET_KUBECONFIG"))
	if err != nil {
		t.Fatalf("reading target kubeconfig: %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-td-target", Namespace: e2eNamespace},
		Data:       map[string][]byte{"kubeconfig": targetKubeconfig},
	}
	if err := hubCl.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating kubeconfig secret: %v", err)
	}
	t.Cleanup(func() { _ = hubCl.Delete(context.Background(), secret) })

	ref := rtv1.SecretKeySelector{Key: "kubeconfig"}
	ref.Name = "e2e-td-target"
	ref.Namespace = e2eNamespace
	kt := &compositiondefinitionsv1alpha1.KubernetesTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-td-target", Namespace: e2eNamespace},
		Spec:       compositiondefinitionsv1alpha1.KubernetesTargetSpec{KubeconfigRef: ref},
	}
	if err := hubCl.Create(ctx, kt); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating KubernetesTarget: %v", err)
	}
	t.Cleanup(func() { _ = hubCl.Delete(context.Background(), kt) })

	// Remote CompositionDefinition with its generated GVK recorded on status.
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-portal-cd", Namespace: e2eNamespace},
		Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{
			Chart:  &compositiondefinitionsv1alpha1.ChartInfo{Url: "oci://ghcr.io/x/y", Version: "1.0.0"},
			Deploy: &compositiondefinitionsv1alpha1.DeploymentTarget{TargetRef: &compositiondefinitionsv1alpha1.TargetReference{Name: "e2e-td-target"}},
		},
	}
	if err := hubCl.Create(ctx, cd); err != nil {
		t.Fatalf("creating CompositionDefinition: %v", err)
	}
	cd.Status.ApiVersion = tdAPIVersion
	cd.Status.Kind = e2eKind
	cd.Status.Resource = e2ePlural
	if err := hubCl.Status().Update(ctx, cd); err != nil {
		t.Fatalf("setting CD status GVK: %v", err)
	}

	hubRes := hubDyn.Resource(tdGVR).Namespace(e2eNamespace)
	spokeRes := spokeDyn.Resource(tdGVR).Namespace(e2eNamespace)
	if _, err := hubRes.Create(ctx, e2ePortalIn(tdAPIVersion, e2eKind, "portal-x", map[string]interface{}{"tenant": "acme"}, false), metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating hub Composition: %v", err)
	}

	r := &Reconciler{hub: hubCl, hubDynamic: hubDyn, log: logging.NewNopLogger()}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "e2e-portal-cd", Namespace: e2eNamespace}}

	// Live reconcile: adds the finalizer and mirrors the hub Composition to the spoke.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile (live): %v", err)
	}
	if _, err := spokeRes.Get(ctx, "portal-x", metav1.GetOptions{}); err != nil {
		t.Fatalf("hub Composition should be mirrored to the spoke: %v", err)
	}
	got := &compositiondefinitionsv1alpha1.CompositionDefinition{}
	if err := hubCl.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatalf("get CD: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, cdFinalizer) {
		t.Fatalf("CD should carry the teardown finalizer after a live reconcile, has %v", got.GetFinalizers())
	}
	t.Cleanup(func() { // best effort if a later assertion fails mid-teardown
		cur := &compositiondefinitionsv1alpha1.CompositionDefinition{}
		if hubCl.Get(context.Background(), req.NamespacedName, cur) == nil && controllerutil.RemoveFinalizer(cur, cdFinalizer) {
			_ = hubCl.Update(context.Background(), cur)
		}
	})

	// Delete the CD: the finalizer turns this into a deletionTimestamp; reconcile then tears down.
	if err := hubCl.Delete(ctx, got); err != nil {
		t.Fatalf("deleting CD: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile (delete): %v", err)
	}

	if _, err := hubRes.Get(ctx, "portal-x", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("hub Composition should be deleted on teardown, got err=%v", err)
	}
	if _, err := spokeRes.Get(ctx, "portal-x", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("spoke mirror should be deleted on teardown, got err=%v", err)
	}
	if err := hubCl.Get(ctx, req.NamespacedName, got); err == nil {
		t.Errorf("CD should be gone once the finalizer is released, still present with finalizers %v", got.GetFinalizers())
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get CD after teardown: %v", err)
	}

	t.Logf("OK: CD teardown removed hub + spoke instances and released the finalizer")
}
