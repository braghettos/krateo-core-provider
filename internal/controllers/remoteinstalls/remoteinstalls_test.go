package remoteinstalls

import (
	"context"
	"testing"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := compositiondefinitionsv1alpha1.SchemeBuilder.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Reconciling a RemoteInstall must create an owned, remote-targeted CompositionDefinition and roll
// its (not-yet-ready) status up to Pending.
func TestReconcile_CreatesOwnedRemoteCompositionDefinition(t *testing.T) {
	ri := &compositiondefinitionsv1alpha1.RemoteInstall{
		ObjectMeta: metav1.ObjectMeta{Name: "krateo", Namespace: "team-a", UID: "ri-uid"},
		Spec: compositiondefinitionsv1alpha1.RemoteInstallSpec{
			TargetRef: compositiondefinitionsv1alpha1.TargetReference{Name: "spoke"},
			Chart: &compositiondefinitionsv1alpha1.ChartInfo{
				Url:     "oci://ghcr.io/braghettos/krateo/installer",
				Version: "0.2.180",
			},
		},
	}
	scheme := testScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ri).
		WithStatusSubresource(&compositiondefinitionsv1alpha1.RemoteInstall{}).
		Build()

	r := &Reconciler{client: cl, scheme: scheme, log: logging.NewNopLogger()}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "krateo", Namespace: "team-a"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// the owned CompositionDefinition must exist, remote-targeted at the spoke.
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "krateo", Namespace: "team-a"}, cd); err != nil {
		t.Fatalf("owned CompositionDefinition not created: %v", err)
	}
	if cd.Spec.Chart == nil || cd.Spec.Chart.Url != ri.Spec.Chart.Url || cd.Spec.Chart.Version != "0.2.180" {
		t.Fatalf("CD chart mismatch: %+v", cd.Spec.Chart)
	}
	if cd.Spec.Deploy == nil || cd.Spec.Deploy.TargetRef == nil || cd.Spec.Deploy.TargetRef.Name != "spoke" {
		t.Fatalf("CD deploy.targetRef mismatch: %+v", cd.Spec.Deploy)
	}
	if len(cd.OwnerReferences) != 1 || cd.OwnerReferences[0].Name != "krateo" || cd.OwnerReferences[0].Controller == nil || !*cd.OwnerReferences[0].Controller {
		t.Fatalf("CD is not controller-owned by the RemoteInstall: %+v", cd.OwnerReferences)
	}

	// the RemoteInstall status rolls up to Pending (CD has no Ready condition yet).
	got := &compositiondefinitionsv1alpha1.RemoteInstall{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "krateo", Namespace: "team-a"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phasePending {
		t.Fatalf("phase = %q, want %q", got.Status.Phase, phasePending)
	}
	if got.Status.CompositionDefinition != "team-a/krateo" {
		t.Fatalf("compositionDefinition ref = %q", got.Status.CompositionDefinition)
	}
}

// When the owned CompositionDefinition is Ready but has not yet recorded its generated GVK, the
// instance cannot be applied — the RemoteInstall must go Failed (surfacing the problem) rather than
// claim Ready.
func TestReconcile_CDReadyWithoutGVKMarksFailed(t *testing.T) {
	ri := &compositiondefinitionsv1alpha1.RemoteInstall{
		ObjectMeta: metav1.ObjectMeta{Name: "krateo", Namespace: "team-a", UID: "ri-uid"},
		Spec: compositiondefinitionsv1alpha1.RemoteInstallSpec{
			TargetRef: compositiondefinitionsv1alpha1.TargetReference{Name: "spoke"},
			Chart:     &compositiondefinitionsv1alpha1.ChartInfo{Url: "oci://ghcr.io/x/y", Version: "1.0.0"},
		},
	}
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "krateo", Namespace: "team-a"},
	}
	cd.Status.SetConditions(rtv1.Available()) // Ready=True but no ApiVersion/Kind/Resource

	scheme := testScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ri, cd).
		WithStatusSubresource(&compositiondefinitionsv1alpha1.RemoteInstall{}, &compositiondefinitionsv1alpha1.CompositionDefinition{}).
		Build()

	r := &Reconciler{client: cl, scheme: scheme, log: logging.NewNopLogger()}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "krateo", Namespace: "team-a"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &compositiondefinitionsv1alpha1.RemoteInstall{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "krateo", Namespace: "team-a"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phaseFailed {
		t.Fatalf("phase = %q, want %q", got.Status.Phase, phaseFailed)
	}
}

// When the owned CompositionDefinition is Ready with a recorded generated GVK, the shim authors a
// hub Composition from spec.values (owned by the RemoteInstall, for GC) instead of applying onto the
// spoke directly; the RemoteInstall rolls up to Ready. The compositionmirror reflector — not tested
// here — is what carries that hub Composition to the spoke.
func TestReconcile_CDReadyCreatesHubComposition(t *testing.T) {
	ctx := context.Background()
	ri := &compositiondefinitionsv1alpha1.RemoteInstall{
		ObjectMeta: metav1.ObjectMeta{Name: "krateo", Namespace: "team-a", UID: "ri-uid"},
		Spec: compositiondefinitionsv1alpha1.RemoteInstallSpec{
			TargetRef: compositiondefinitionsv1alpha1.TargetReference{Name: "spoke"},
			Chart:     &compositiondefinitionsv1alpha1.ChartInfo{Url: "oci://ghcr.io/x/y", Version: "1.0.0"},
			Values:    &runtime.RawExtension{Raw: []byte(`{"tenant":"acme"}`)},
		},
	}
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "krateo", Namespace: "team-a"},
	}
	cd.Status.SetConditions(rtv1.Available())
	cd.Status.ApiVersion = "composition.krateo.io/v1-0-0"
	cd.Status.Kind = "Portal"
	cd.Status.Resource = "portals"

	scheme := testScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ri, cd).
		WithStatusSubresource(&compositiondefinitionsv1alpha1.RemoteInstall{}, &compositiondefinitionsv1alpha1.CompositionDefinition{}).
		Build()

	gvr := schema.GroupVersionResource{Group: "composition.krateo.io", Version: "v1-0-0", Resource: "portals"}
	hubDyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "PortalList"},
	)

	r := &Reconciler{client: cl, hubDynamic: hubDyn, scheme: scheme, log: logging.NewNopLogger()}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "krateo", Namespace: "team-a"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// hub Composition authored from spec.values, owned (controller) by the RemoteInstall.
	comp, err := hubDyn.Resource(gvr).Namespace("team-a").Get(ctx, "krateo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("hub composition not created: %v", err)
	}
	if v, _, _ := unstructured.NestedString(comp.Object, "spec", "tenant"); v != "acme" {
		t.Errorf("hub composition spec.tenant = %q, want acme", v)
	}
	owners := comp.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "RemoteInstall" || owners[0].Name != "krateo" || owners[0].Controller == nil || !*owners[0].Controller {
		t.Fatalf("hub composition not controller-owned by the RemoteInstall: %+v", owners)
	}

	// the RemoteInstall rolls up to Ready.
	got := &compositiondefinitionsv1alpha1.RemoteInstall{}
	if err := cl.Get(ctx, types.NamespacedName{Name: "krateo", Namespace: "team-a"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phaseReady {
		t.Fatalf("phase = %q, want %q", got.Status.Phase, phaseReady)
	}
}
