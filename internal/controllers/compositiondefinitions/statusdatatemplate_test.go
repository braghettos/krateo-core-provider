package compositiondefinitions

import (
	"strings"
	"testing"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	"github.com/krateoplatformops/core-provider/internal/tools/objects"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestEncodeStatusDataTemplate(t *testing.T) {
	// empty when nothing declared
	if got := encodeStatusDataTemplate(&compositiondefinitionsv1alpha1.CompositionDefinition{}); got != "" {
		t.Errorf("empty = %q, want \"\"", got)
	}

	cr := &compositiondefinitionsv1alpha1.CompositionDefinition{}
	cr.Spec.StatusDataTemplate = []compositiondefinitionsv1alpha1.StatusFieldMapping{
		{ForPath: "url", Expression: `${ "https://\(.self.spec.host)" }`},
		{ForPath: "ready", Expression: `${ .helm.status == "deployed" }`, Type: "boolean"},
	}
	got := encodeStatusDataTemplate(cr)
	// wire format: lowercase forPath/expression keys, type omitted (only forPath+expression travel).
	want := `[{"forPath":"url","expression":"${ \"https://\\(.self.spec.host)\" }"},{"forPath":"ready","expression":"${ .helm.status == \"deployed\" }"}]`
	if got != want {
		t.Errorf("encoded =\n  %s\nwant\n  %s", got, want)
	}
}

// The CDC ConfigMap template renders the serialized template into
// COMPOSITION_CONTROLLER_STATUS_DATA_TEMPLATE as a YAML-safe quoted string, so the CDC
// receives the raw JSON.
func TestConfigMapRendersStatusDataTemplate(t *testing.T) {
	cm := corev1.ConfigMap{}
	json := `[{"forPath":"url","expression":"${ .self.spec.host }"}]`
	err := objects.CreateK8sObject(&cm,
		schema.GroupVersionResource{Group: "composition.krateo.io", Version: "v1-0-0", Resource: "fireworksapps"},
		types.NamespacedName{Name: "fireworksapps-v1-0-0-configmap", Namespace: "demo"},
		"testdata/manifests/configmap.yaml",
		"composition_controller_sa_name", "sa",
		"composition_controller_sa_namespace", "demo",
		"status_data_template", json,
	)
	if err != nil {
		t.Fatalf("rendering configmap: %v", err)
	}
	got := cm.Data["COMPOSITION_CONTROLLER_STATUS_DATA_TEMPLATE"]
	if got != json {
		t.Errorf("env value = %q, want raw json %q", got, json)
	}
	if !strings.Contains(got, `"forPath"`) {
		t.Errorf("env value lost JSON structure: %q", got)
	}
}
