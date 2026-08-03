package composition

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// When the Composition carries a deletionTimestamp, Observe must short-circuit and report the
// resource as existing + up-to-date WITHOUT running the helm-upgrade drift check. Otherwise the
// Upgrade fails with "has no deployed releases" once the release is mid-uninstall, and resync
// re-enqueues Observe forever, pinning the Composition in ReconcileError and never finalizing.
// The deletion itself is driven by the Delete handler, not Observe.
func TestObserve_DeletionTimestamp_SkipsDriftObserve(t *testing.T) {
	// Zero-value handler is sufficient: the deletion short-circuit returns before any handler
	// field (kubeconfig, helm client, package getter, ...) is touched.
	h := &handler{}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "composition.krateo.io",
		Version: "v0-2-105",
		Kind:    "Installer",
	})
	u.SetName("installer")
	u.SetNamespace("krateo-system")
	now := metav1.Now()
	u.SetDeletionTimestamp(&now)

	obs, err := h.Observe(context.Background(), u)
	if err != nil {
		t.Fatalf("Observe on a deleting Composition returned error %v; want nil (must not run drift/upgrade)", err)
	}
	if !obs.ResourceExists || !obs.ResourceUpToDate {
		t.Fatalf("Observe on a deleting Composition = %+v; want ResourceExists=true, ResourceUpToDate=true so the runtime routes to Delete", obs)
	}
}
