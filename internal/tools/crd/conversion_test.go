package crd

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// TestSetNoneConversion guards that generated CRDs use the built-in None converter (no
// webhook). The old conversion webhook only copied metadata/spec/status verbatim, which
// None does for free; lossless multi-version storage is handled by the permissive
// "vacuum" storage version, not by conversion.
func TestSetNoneConversion(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	setNoneConversion(crd)

	if crd.Spec.Conversion == nil {
		t.Fatal("expected conversion to be set")
	}
	if crd.Spec.Conversion.Strategy != apiextensionsv1.NoneConverter {
		t.Fatalf("strategy = %q, want None", crd.Spec.Conversion.Strategy)
	}
	if crd.Spec.Conversion.Webhook != nil {
		t.Fatal("None strategy must not carry a webhook config")
	}
}
