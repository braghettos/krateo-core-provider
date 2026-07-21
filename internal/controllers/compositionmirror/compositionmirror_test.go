package compositionmirror

import (
	"context"
	"testing"

	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
