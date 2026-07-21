package compositionmirror

import (
	"context"
	"testing"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	testAPIVersion = "composition.krateo.io/v1-0-0"
	testKind       = "Portal"
	testNamespace  = "krateo-system"
)

var testGVR = schema.GroupVersionResource{Group: "composition.krateo.io", Version: "v1-0-0", Resource: "portals"}

func portal(name string, spec map[string]interface{}, managed bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(testAPIVersion)
	u.SetKind(testKind)
	u.SetNamespace(testNamespace)
	u.SetName(name)
	if spec != nil {
		_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	}
	if managed {
		u.SetLabels(map[string]string{managedByLabel: managedByValue})
	}
	return u
}

func newFakeDyn(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{testGVR: "PortalList"},
		objs...,
	)
}

// reflectInstances must, in one pass: create a hub-only instance on the spoke, force the hub spec
// onto a drifted spoke mirror (hub wins), read the spoke status back onto the hub instance, delete a
// managed spoke mirror with no hub counterpart, and leave an unmanaged spoke instance untouched.
func TestReflectInstances(t *testing.T) {
	ctx := context.Background()

	// Hub: portal-a is new; portal-b's desired spec is replicas=5.
	hub := newFakeDyn(
		portal("portal-a", map[string]interface{}{"replicas": int64(2)}, false),
		portal("portal-b", map[string]interface{}{"replicas": int64(5)}, false),
	)

	// Spoke: portal-b is a managed mirror with a stale spec (1) and a rendered status; portal-orphan
	// is a managed mirror with no hub counterpart; portal-foreign is NOT managed by us.
	spokeB := portal("portal-b", map[string]interface{}{"replicas": int64(1)}, true)
	_ = unstructured.SetNestedMap(spokeB.Object, map[string]interface{}{"ready": true}, "status")
	spoke := newFakeDyn(
		spokeB,
		portal("portal-orphan", map[string]interface{}{}, true),
		portal("portal-foreign", map[string]interface{}{}, false),
	)

	if err := reflectInstances(ctx, reflectParams{
		hub:        hub,
		spoke:      spoke,
		gvr:        testGVR,
		apiVersion: testAPIVersion,
		kind:       testKind,
		namespace:  testNamespace,
		log:        logging.NewNopLogger(),
	}); err != nil {
		t.Fatalf("reflectInstances: %v", err)
	}

	spokeRes := spoke.Resource(testGVR).Namespace(testNamespace)
	hubRes := hub.Resource(testGVR).Namespace(testNamespace)

	// portal-a created on the spoke, stamped managed, spec mirrored from the hub.
	a, err := spokeRes.Get(ctx, "portal-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("portal-a not created on spoke: %v", err)
	}
	if a.GetLabels()[managedByLabel] != managedByValue {
		t.Errorf("portal-a missing management label")
	}
	if r, _, _ := unstructured.NestedInt64(a.Object, "spec", "replicas"); r != 2 {
		t.Errorf("portal-a spec.replicas = %d, want 2", r)
	}

	// portal-b spec forced to the hub's value (hub wins over the spoke's stale 1).
	b, err := spokeRes.Get(ctx, "portal-b", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("portal-b get: %v", err)
	}
	if r, _, _ := unstructured.NestedInt64(b.Object, "spec", "replicas"); r != 5 {
		t.Errorf("portal-b spec.replicas = %d, want 5 (hub wins on drift)", r)
	}

	// portal-b spoke status read back onto the hub instance.
	hb, err := hubRes.Get(ctx, "portal-b", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("hub portal-b get: %v", err)
	}
	if ready, found, _ := unstructured.NestedBool(hb.Object, "status", "ready"); !found || !ready {
		t.Errorf("hub portal-b status.ready = %v (found=%v), want true", ready, found)
	}

	// orphaned managed mirror deleted.
	if _, err := spokeRes.Get(ctx, "portal-orphan", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("portal-orphan should be garbage-collected, got err=%v", err)
	}

	// unmanaged instance left untouched.
	if _, err := spokeRes.Get(ctx, "portal-foreign", metav1.GetOptions{}); err != nil {
		t.Errorf("portal-foreign (unmanaged) must survive GC, got err=%v", err)
	}
}

func remoteCD(name, ns, targetRef string) *compositiondefinitionsv1alpha1.CompositionDefinition {
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if targetRef != "" {
		cd.Spec.Deploy = &compositiondefinitionsv1alpha1.DeploymentTarget{
			TargetRef: &compositiondefinitionsv1alpha1.TargetReference{Name: targetRef},
		}
	}
	return cd
}

func newReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := compositiondefinitionsv1alpha1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	hub := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &Reconciler{hub: hub, hubDynamic: newFakeDyn(), log: logging.NewNopLogger()}
}

func reconcileReq(name, ns string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
}

// A local CompositionDefinition (no deploy.targetRef) is a strict no-op for the reflector.
func TestReconcile_LocalIsNoOp(t *testing.T) {
	r := newReconciler(t, remoteCD("local-cd", "krateo-system", ""))
	res, err := r.Reconcile(context.Background(), reconcileReq("local-cd", "krateo-system"))
	if err != nil {
		t.Fatalf("local CD reconcile: unexpected error %v", err)
	}
	if res != (reconcile.Result{}) {
		t.Fatalf("local CD reconcile should not requeue, got %+v", res)
	}
}

// A remote CD whose generated GVK is not yet recorded requeues on the short pending interval and does
// not reach the spoke.
func TestReconcile_PendingGVKRequeuesShort(t *testing.T) {
	r := newReconciler(t, remoteCD("pending-cd", "krateo-system", "spoke"))
	res, err := r.Reconcile(context.Background(), reconcileReq("pending-cd", "krateo-system"))
	if err != nil {
		t.Fatalf("pending CD reconcile: unexpected error %v", err)
	}
	if res.RequeueAfter != requeuePending {
		t.Fatalf("pending CD should requeue after %v, got %v", requeuePending, res.RequeueAfter)
	}
}

// Failure isolation: when the spoke is unreachable (its KubernetesTarget is missing), the reflector
// SURFACES the error so controller-runtime applies rate-limited backoff — rather than swallowing it
// and hammering the dead target at the flat resync interval.
func TestReconcile_UnreachableSpokeSurfacesError(t *testing.T) {
	cd := remoteCD("remote-cd", "krateo-system", "missing-target")
	cd.Status.ApiVersion = testAPIVersion
	cd.Status.Kind = testKind
	cd.Status.Resource = "portals"

	r := newReconciler(t, cd)
	_, err := r.Reconcile(context.Background(), reconcileReq("remote-cd", "krateo-system"))
	if err == nil {
		t.Fatal("expected reconcile to return an error for an unreachable spoke (backoff), got nil")
	}
}

// A hub Composition event maps only to the remote-targeted CompositionDefinition of the SAME Kind in
// the SAME namespace — never a local CD (which doesn't reflect), a different Kind, or another namespace.
func TestEnqueueCDForComposition(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := compositiondefinitionsv1alpha1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	remote := remoteCD("portal-cd", "krateo-system", "spoke")
	remote.Status.Kind = "Portal"
	local := remoteCD("portal-local", "krateo-system", "") // local, same Kind -> excluded (no reflection)
	local.Status.Kind = "Portal"
	otherKind := remoteCD("db-cd", "krateo-system", "spoke")
	otherKind.Status.Kind = "Database" // different Kind -> excluded
	otherNS := remoteCD("portal-elsewhere", "team-b", "spoke")
	otherNS.Status.Kind = "Portal" // different namespace -> excluded

	hub := fakeclient.NewClientBuilder().WithScheme(scheme).
		WithObjects(remote, local, otherKind, otherNS).Build()

	comp := &unstructured.Unstructured{}
	comp.SetGroupVersionKind(schema.GroupVersionKind{Group: "composition.krateo.io", Version: "v1-0-0", Kind: "Portal"})
	comp.SetNamespace("krateo-system")
	comp.SetName("my-portal")

	reqs := enqueueCDForComposition(hub)(context.Background(), comp)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one enqueued CD, got %d: %v", len(reqs), reqs)
	}
	if reqs[0].Name != "portal-cd" || reqs[0].Namespace != "krateo-system" {
		t.Fatalf("expected krateo-system/portal-cd, got %v", reqs[0].NamespacedName)
	}
}

// mirrorStatusUp must NOT write when the hub status already equals the spoke's. An unconditional
// UpdateStatus would bump the hub Composition's resourceVersion and re-fire the dynamic watch, looping
// forever. Here hub and spoke already agree, so zero status writes must occur.
func TestReflectInstances_StatusIdempotent(t *testing.T) {
	ctx := context.Background()
	ready := map[string]interface{}{"phase": "ready"}

	hubA := portal("portal-a", map[string]interface{}{"tenant": "acme"}, false)
	_ = unstructured.SetNestedMap(hubA.Object, ready, "status")
	hub := newFakeDyn(hubA)

	spokeA := portal("portal-a", map[string]interface{}{"tenant": "acme"}, true)
	_ = unstructured.SetNestedMap(spokeA.Object, ready, "status")
	spoke := newFakeDyn(spokeA)

	var statusWrites int
	hub.PrependReactor("update", "portals", func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "status" {
			statusWrites++
		}
		return false, nil, nil
	})

	if err := reflectInstances(ctx, reflectParams{
		hub: hub, spoke: spoke, gvr: testGVR,
		apiVersion: testAPIVersion, kind: testKind, namespace: testNamespace, log: logging.NewNopLogger(),
	}); err != nil {
		t.Fatalf("reflectInstances: %v", err)
	}
	if statusWrites != 0 {
		t.Errorf("expected 0 hub status writes when status is unchanged, got %d — would loop the dynamic watch", statusWrites)
	}
}
