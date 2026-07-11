package generation

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestAddCompositionVersionColumn(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{
		{Name: "v1-0-0", Served: true},
		{Name: "v2-0-0", Served: true, AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
			{Name: "AGE", Type: "date", JSONPath: ".metadata.creationTimestamp"},
		}},
		{Name: "vacuum", Served: false, Storage: true},
	}

	AddCompositionVersionColumn(crd)

	hasVersionCol := func(v apiextensionsv1.CustomResourceDefinitionVersion) bool {
		for _, c := range v.AdditionalPrinterColumns {
			if c.Name == "VERSION" && c.JSONPath == `.metadata.labels.krateo\.io/composition-version` {
				return true
			}
		}
		return false
	}

	byName := map[string]apiextensionsv1.CustomResourceDefinitionVersion{}
	for _, v := range crd.Spec.Versions {
		byName[v.Name] = v
	}

	if !hasVersionCol(byName["v1-0-0"]) {
		t.Errorf("v1-0-0 should have the VERSION column")
	}
	// existing AGE column preserved, VERSION appended
	if got := len(byName["v2-0-0"].AdditionalPrinterColumns); got != 2 || !hasVersionCol(byName["v2-0-0"]) {
		t.Errorf("v2-0-0 should keep AGE and add VERSION (cols=%d)", got)
	}
	// vacuum (non-served storage version) is skipped
	if len(byName["vacuum"].AdditionalPrinterColumns) != 0 {
		t.Errorf("vacuum must not get a printer column")
	}

	// idempotent: a second call adds nothing
	AddCompositionVersionColumn(crd)
	for _, v := range crd.Spec.Versions {
		n := 0
		for _, c := range v.AdditionalPrinterColumns {
			if c.Name == "VERSION" {
				n++
			}
		}
		if n > 1 {
			t.Errorf("version %s has %d VERSION columns; AddCompositionVersionColumn is not idempotent", v.Name, n)
		}
	}
}
