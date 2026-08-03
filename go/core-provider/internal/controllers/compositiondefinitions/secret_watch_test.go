package compositiondefinitions

import (
	"testing"

	compositiondefinitionsv1alpha1 "github.com/krateo-platformops/core-provider/apis/compositiondefinitions/v1alpha1"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// The CompositionDefinition resolves its targetRef in its OWN namespace, so the match is
	// on (namespace, name) — a same-named target in another namespace must NOT match.
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo-system"},
		Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{
			Deploy: &compositiondefinitionsv1alpha1.DeploymentTarget{
				TargetRef: &compositiondefinitionsv1alpha1.TargetReference{Name: "prod-eu"},
			},
		},
	}

	if !compositionReferencesTargetIn(cd, map[targetKey]bool{{namespace: "demo-system", name: "prod-eu"}: true}) {
		t.Fatal("expected match when the referenced target is in the set (same namespace)")
	}
	if compositionReferencesTargetIn(cd, map[targetKey]bool{{namespace: "demo-system", name: "prod-us"}: true}) {
		t.Fatal("did not expect match for an unrelated target name")
	}
	if compositionReferencesTargetIn(cd, map[targetKey]bool{{namespace: "other-ns", name: "prod-eu"}: true}) {
		t.Fatal("did not expect match for a same-named target in a different namespace")
	}
	if compositionReferencesTargetIn(&compositiondefinitionsv1alpha1.CompositionDefinition{}, map[targetKey]bool{{namespace: "demo-system", name: "prod-eu"}: true}) {
		t.Fatal("did not expect match when there is no deploy.targetRef")
	}
	local := &compositiondefinitionsv1alpha1.CompositionDefinition{
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo-system"},
		Spec:       compositiondefinitionsv1alpha1.CompositionDefinitionSpec{Deploy: &compositiondefinitionsv1alpha1.DeploymentTarget{}},
	}
	if compositionReferencesTargetIn(local, map[targetKey]bool{{namespace: "demo-system", name: "prod-eu"}: true}) {
		t.Fatal("did not expect match for a local deploy (no targetRef)")
	}
}
