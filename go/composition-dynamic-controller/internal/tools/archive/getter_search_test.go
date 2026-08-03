package archive

import (
	"strings"
	"testing"

	compositionMeta "github.com/krateo-platformops/composition-dynamic-controller/pkg/meta"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// searchTestCDGVR is the GVR searchCompositionDefinition lists.
var searchTestCDGVR = schema.GroupVersionResource{
	Group:    "core.krateo.io",
	Version:  "v1alpha1",
	Resource: "compositiondefinitions",
}

// newSearchTestCD builds a CompositionDefinition with the status fields the matcher reads
// (status.apiVersion carries the chart-version suffix, status.kind the served kind).
func newSearchTestCD(name, namespace, statusVersion, statusKind string) *unstructured.Unstructured {
	cd := &unstructured.Unstructured{}
	cd.SetGroupVersionKind(schema.GroupVersionKind{Group: "core.krateo.io", Version: "v1alpha1", Kind: "CompositionDefinition"})
	cd.SetNamespace(namespace)
	cd.SetName(name)
	_ = unstructured.SetNestedField(cd.Object, "composition.krateo.io/"+statusVersion, "status", "apiVersion")
	_ = unstructured.SetNestedField(cd.Object, statusKind, "status", "kind")
	return cd
}

// newSearchTestComposition builds a composition instance with the given kind and
// composition-version label; extraLabels lets cases add the definition-ref labels.
func newSearchTestComposition(kind, versionLabel string, extraLabels map[string]string) *unstructured.Unstructured {
	lbl := map[string]string{
		compositionMeta.CompositionVersionLabel: versionLabel,
	}
	for k, v := range extraLabels {
		lbl[k] = v
	}
	mg := &unstructured.Unstructured{}
	mg.SetAPIVersion("composition.krateo.io/" + versionLabel)
	mg.SetKind(kind)
	mg.SetNamespace("krateo-system")
	mg.SetName("my-instance")
	mg.SetLabels(lbl)
	return mg
}

func definitionRefLabels(name, namespace string) map[string]string {
	return map[string]string{
		compositionMeta.CompositionDefinitionNameLabel:      name,
		compositionMeta.CompositionDefinitionNamespaceLabel: namespace,
	}
}

func TestSearchCompositionDefinition(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "composition.krateo.io", Version: "v1-5-11", Resource: "fireworksapps"}

	cases := []struct {
		name        string
		definitions []*unstructured.Unstructured
		instance    *unstructured.Unstructured
		wantName    string // expected resolved CompositionDefinition name; "" => expect error
		wantErr     string // substring the error must contain when wantName == ""
	}{
		{
			// (a) The instance's composition-version label lags the CD (chart bump in flight)
			// AND two CDs serve the same kind, so neither exact-version nor unique-kind can
			// resolve: only the definition-ref labels identify the owner.
			name: "definition-ref labels win despite version skew and same-kind ambiguity",
			definitions: []*unstructured.Unstructured{
				newSearchTestCD("portal", "krateo-system", "v1-5-12", "FireworksApp"),
				newSearchTestCD("portal-old", "krateo-system", "v1-5-10", "FireworksApp"),
			},
			instance: newSearchTestComposition("FireworksApp", "v1-5-11",
				definitionRefLabels("portal", "krateo-system")),
			wantName: "portal",
		},
		{
			// (b) Regression: the pre-existing exact (version, kind) match still resolves
			// when no definition-ref labels are present.
			name: "exact version+kind match still works without ref labels",
			definitions: []*unstructured.Unstructured{
				newSearchTestCD("portal", "krateo-system", "v1-5-11", "FireworksApp"),
				newSearchTestCD("other", "krateo-system", "v2-0-0", "OtherApp"),
			},
			instance: newSearchTestComposition("FireworksApp", "v1-5-11", nil),
			wantName: "portal",
		},
		{
			// (b bis) Stale definition-ref labels (owner renamed/deleted) must not wedge:
			// fall through to the exact version+kind match.
			name: "stale ref labels fall through to exact version match",
			definitions: []*unstructured.Unstructured{
				newSearchTestCD("portal", "krateo-system", "v1-5-11", "FireworksApp"),
				newSearchTestCD("other", "krateo-system", "v2-0-0", "OtherApp"),
			},
			instance: newSearchTestComposition("FireworksApp", "v1-5-11",
				definitionRefLabels("gone", "krateo-system")),
			wantName: "portal",
		},
		{
			// (b ter) Kind guard: the ref labels point at an EXISTING CD that serves a
			// DIFFERENT kind (mislabeled instance, name reuse after delete/recreate).
			// Trusting the labels would fetch the wrong chart: fall through to the exact
			// version+kind match on the true owner instead.
			name: "ref labels pointing at a different-kind definition fall through to exact match",
			definitions: []*unstructured.Unstructured{
				newSearchTestCD("portal", "krateo-system", "v1-5-11", "OtherApp"),
				newSearchTestCD("true-owner", "krateo-system", "v1-5-11", "FireworksApp"),
			},
			instance: newSearchTestComposition("FireworksApp", "v1-5-11",
				definitionRefLabels("portal", "krateo-system")),
			wantName: "true-owner",
		},
		{
			// (c) Version-bump wedge: label says v1-5-11, CD moved to v1-5-12, no ref labels.
			// Exactly one CD serves the kind -> unique-kind fallback unwedges the migration.
			name: "unique-kind fallback tolerates version skew without ref labels",
			definitions: []*unstructured.Unstructured{
				newSearchTestCD("portal", "krateo-system", "v1-5-12", "FireworksApp"),
				newSearchTestCD("other", "krateo-system", "v2-0-0", "OtherApp"),
			},
			instance: newSearchTestComposition("FireworksApp", "v1-5-11", nil),
			wantName: "portal",
		},
		{
			// (d) Two CDs serve the same kind, version skew, no ref labels: still ambiguous,
			// keep the existing error rather than guessing.
			name: "ambiguous same-kind definitions with version skew still error",
			definitions: []*unstructured.Unstructured{
				newSearchTestCD("portal", "krateo-system", "v1-5-12", "FireworksApp"),
				newSearchTestCD("portal-old", "krateo-system", "v1-5-10", "FireworksApp"),
			},
			instance: newSearchTestComposition("FireworksApp", "v1-5-11", nil),
			wantErr:  "too many definitions",
		},
		{
			// tot == 1 fast path unchanged: the single CD is used even with version skew.
			name: "single definition fast path unchanged",
			definitions: []*unstructured.Unstructured{
				newSearchTestCD("portal", "krateo-system", "v1-5-12", "FireworksApp"),
			},
			instance: newSearchTestComposition("FireworksApp", "v1-5-11", nil),
			wantName: "portal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			gvrToListKind := map[schema.GroupVersionResource]string{searchTestCDGVR: "CompositionDefinitionList"}

			objs := make([]runtime.Object, 0, len(tc.definitions))
			for _, cd := range tc.definitions {
				objs = append(objs, cd)
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)

			g := &dynamicGetter{
				dynamicClient: dyn,
				logger:        logging.NewNopLogger(),
			}

			got, err := g.searchCompositionDefinition(gvr, tc.instance)

			if tc.wantName == "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got definition %q", tc.wantErr, got.GetName())
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.GetName() != tc.wantName {
				t.Errorf("resolved definition = %q, want %q", got.GetName(), tc.wantName)
			}
		})
	}
}
