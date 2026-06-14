package compositiondefinitions

import (
	"testing"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
)

func chartCredsSecret(name, namespace string) rtv1.SecretKeySelector {
	sel := rtv1.SecretKeySelector{Key: "password"}
	sel.Name = name
	sel.Namespace = namespace
	return sel
}

func TestCompositionReferencesChartSecret(t *testing.T) {
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{
		Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{
			Chart: &compositiondefinitionsv1alpha1.ChartInfo{
				Url:         "oci://example.com/chart",
				Credentials: &compositiondefinitionsv1alpha1.Credentials{Username: "u", PasswordRef: chartCredsSecret("chart-creds", "demo-system")},
			},
		},
	}

	if !compositionReferencesChartSecret(cd, "demo-system", "chart-creds") {
		t.Fatal("expected match on chart credential secret")
	}
	if compositionReferencesChartSecret(cd, "other", "chart-creds") {
		t.Fatal("did not expect match on a different namespace")
	}
	if compositionReferencesChartSecret(&compositiondefinitionsv1alpha1.CompositionDefinition{}, "demo-system", "chart-creds") {
		t.Fatal("did not expect match when there are no chart credentials")
	}
}

func TestCompositionReferencesTargetIn(t *testing.T) {
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{
		Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{
			Deploy: &compositiondefinitionsv1alpha1.DeploymentTarget{
				TargetRef: &compositiondefinitionsv1alpha1.TargetReference{Name: "prod-eu"},
			},
		},
	}

	if !compositionReferencesTargetIn(cd, map[string]bool{"prod-eu": true}) {
		t.Fatal("expected match when the referenced target is in the set")
	}
	if compositionReferencesTargetIn(cd, map[string]bool{"prod-us": true}) {
		t.Fatal("did not expect match for an unrelated target")
	}
	if compositionReferencesTargetIn(&compositiondefinitionsv1alpha1.CompositionDefinition{}, map[string]bool{"prod-eu": true}) {
		t.Fatal("did not expect match when there is no deploy.targetRef")
	}
	local := &compositiondefinitionsv1alpha1.CompositionDefinition{
		Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{Deploy: &compositiondefinitionsv1alpha1.DeploymentTarget{}},
	}
	if compositionReferencesTargetIn(local, map[string]bool{"prod-eu": true}) {
		t.Fatal("did not expect match for a local deploy (no targetRef)")
	}
}
