package composition

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// cdGVR is the CompositionDefinition GVR the composition CR labels point at.
var cdGVR = schema.GroupVersionResource{Group: "core.krateo.io", Version: "v1alpha1", Resource: "compositiondefinitions"}

// crWithLabels builds a composition CR carrying the definition-link + version labels the umbrella
// stamps, so isVersionMigrationHandover can resolve its owning CompositionDefinition.
func crWithLabels(crVersion string, extra map[string]string) *unstructured.Unstructured {
	labels := map[string]string{
		"krateo.io/composition-definition-group":     "core.krateo.io",
		"krateo.io/composition-definition-version":   "v1alpha1",
		"krateo.io/composition-definition-resource":  "compositiondefinitions",
		"krateo.io/composition-definition-namespace": "krateo-system",
		"krateo.io/composition-definition-name":      "krateo-observability",
		"krateo.io/composition-version":              crVersion,
		"krateo.io/release-name":                     "krateo-observability",
	}
	for k, v := range extra {
		if v == "" {
			delete(labels, k)
		} else {
			labels[k] = v
		}
	}
	mg := &unstructured.Unstructured{}
	mg.SetAPIVersion("composition.krateo.io/" + crVersion)
	mg.SetKind("KrateoObservability")
	mg.SetNamespace("krateo-system")
	mg.SetName("krateo-observability")
	mg.SetLabels(labels)
	return mg
}

// compositionDefinition builds a CompositionDefinition unstructured with the given chart version.
func compositionDefinition(chartVersion string) *unstructured.Unstructured {
	cd := &unstructured.Unstructured{}
	cd.SetGroupVersionKind(schema.GroupVersionKind{Group: "core.krateo.io", Version: "v1alpha1", Kind: "CompositionDefinition"})
	cd.SetNamespace("krateo-system")
	cd.SetName("krateo-observability")
	_ = unstructured.SetNestedField(cd.Object, chartVersion, "spec", "chart", "version")
	return cd
}

func TestIsVersionMigrationHandover(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{cdGVR: "CompositionDefinitionList"}

	cases := []struct {
		name       string
		cr         *unstructured.Unstructured
		cdVersion  string // "" => CompositionDefinition absent (genuine deletion)
		wantResult bool
	}{
		{
			name:       "migration: CD advanced to a newer version than the pruned CR",
			cr:         crWithLabels("v0-1-7", nil),
			cdVersion:  "0.1.8",
			wantResult: true, // handover -> skip uninstall
		},
		{
			name:       "genuine deletion: CD version equals the CR version",
			cr:         crWithLabels("v0-1-7", nil),
			cdVersion:  "0.1.7",
			wantResult: false, // uninstall
		},
		{
			name:       "genuine deletion: CompositionDefinition absent",
			cr:         crWithLabels("v0-1-7", nil),
			cdVersion:  "", // not created in the fake client
			wantResult: false, // uninstall
		},
		{
			name:       "missing definition-name label -> safe default (uninstall)",
			cr:         crWithLabels("v0-1-7", map[string]string{"krateo.io/composition-definition-name": ""}),
			cdVersion:  "0.1.8",
			wantResult: false,
		},
		{
			name:       "missing composition-version label -> safe default (uninstall)",
			cr:         crWithLabels("v0-1-7", map[string]string{"krateo.io/composition-version": ""}),
			cdVersion:  "0.1.8",
			wantResult: false,
		},
		{
			name:       "multi-digit versions normalize correctly (0.2.190 vs v0-2-193)",
			cr:         crWithLabels("v0-2-190", nil),
			cdVersion:  "0.2.193",
			wantResult: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			if tc.cdVersion != "" {
				objs = append(objs, compositionDefinition(tc.cdVersion))
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)

			h := &handler{}
			got, err := h.isVersionMigrationHandover(context.Background(), dyn, tc.cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantResult {
				t.Errorf("isVersionMigrationHandover = %v, want %v", got, tc.wantResult)
			}
		})
	}
}