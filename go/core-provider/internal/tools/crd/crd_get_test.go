package crd

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestGet_SetsGVK guards the fix that makes Get populate TypeMeta: downstream
// kube.Apply derives the GVK from the object, and the direct (non-cached) client used
// for remote targets does not populate it on typed reads, which broke remote
// multi-version CRD updates.
func TestGet_SetsGVK(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	existing := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets", Kind: "Widget"},
			Scope: apiextensionsv1.NamespaceScoped,
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	got, err := Get(context.Background(), cl, schema.GroupResource{Group: "example.com", Resource: "widgets"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a CRD")
	}
	gvk := got.GetObjectKind().GroupVersionKind()
	if gvk.Kind != "CustomResourceDefinition" || gvk.Group != apiextensionsv1.GroupName {
		t.Fatalf("expected CRD GVK to be set, got %q", gvk.String())
	}
}
