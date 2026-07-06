package compositiondefinitions

import (
	"testing"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
)

func TestStatusGVR(t *testing.T) {
	// deployed CD: status carries the generated apiVersion + resource -> usable GVR.
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{}
	cd.Status.ApiVersion = "composition.krateo.io/v0-1-2"
	cd.Status.Kind = "HyperdxProvider"
	cd.Status.Resource = "hyperdxproviders"
	gvr, ok := statusGVR(cd)
	if !ok {
		t.Fatal("expected ok for a deployed CD")
	}
	if gvr.Group != "composition.krateo.io" || gvr.Version != "v0-1-2" || gvr.Resource != "hyperdxproviders" {
		t.Fatalf("gvr = %+v", gvr)
	}

	// not-yet-deployed CD: no status GVR.
	if _, ok := statusGVR(&compositiondefinitionsv1alpha1.CompositionDefinition{}); ok {
		t.Fatal("expected !ok when status has no generated GVK")
	}
}
