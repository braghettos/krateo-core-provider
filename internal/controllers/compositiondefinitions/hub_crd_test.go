package compositiondefinitions

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testCompositionCRD() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "portals.composition.krateo.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "composition.krateo.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "portals", Kind: "Portal"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1-0-0", Served: true, Storage: true},
			},
		},
	}
}

// A local CompositionDefinition must leave the hub untouched: its composition CRD already lives on
// the (local) provisioning cluster, so applyHubCompositionCRD is a strict no-op. This locks the
// "local behavior is unchanged" guarantee of docs/design/remote-composition-mirror.md — mgmtDynamic
// is deliberately nil to prove the local path returns before ever dereferencing it.
//
// The remote apply path is not unit-tested here because ApplyOrUpdateCRD blocks on a CRD-established
// watcher against a real API server; it is build-verified and exercised by the reflector integration
// tests (increment 2).
func TestApplyHubCompositionCRD_LocalIsNoOp(t *testing.T) {
	sch := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(sch); err != nil {
		t.Fatalf("add apiextensions to scheme: %v", err)
	}
	mgmt := fakeclient.NewClientBuilder().WithScheme(sch).Build()

	e := &external{remote: false, mgmt: mgmt, mgmtDynamic: nil}

	crd := testCompositionCRD()
	if err := e.applyHubCompositionCRD(context.Background(), crd); err != nil {
		t.Fatalf("expected nil error for local CD, got: %v", err)
	}

	got := &apiextensionsv1.CustomResourceDefinition{}
	err := mgmt.Get(context.Background(), client.ObjectKey{Name: crd.Name}, got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no CRD created on hub for local CD, got err=%v", err)
	}
}
