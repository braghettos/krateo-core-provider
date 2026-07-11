package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMigrationApproved(t *testing.T) {
	cd := func(policy UpgradePolicy, ann map[string]string) *CompositionDefinition {
		c := &CompositionDefinition{Spec: CompositionDefinitionSpec{UpgradePolicy: policy}}
		c.ObjectMeta = metav1.ObjectMeta{Annotations: ann}
		return c
	}

	cases := []struct {
		name   string
		cd     *CompositionDefinition
		target string
		want   bool
	}{
		{"unset -> automatic (backward compat)", cd("", nil), "v2-0-0", true},
		{"automatic always migrates", cd(UpgradePolicyAutomatic, nil), "v2-0-0", true},
		{"paused never migrates", cd(UpgradePolicyPaused, nil), "v2-0-0", false},
		{"manual without annotation does not migrate", cd(UpgradePolicyManual, nil), "v2-0-0", false},
		{"manual with matching annotation migrates", cd(UpgradePolicyManual, map[string]string{AnnotationKeyUpgradeToVersion: "v2-0-0"}), "v2-0-0", true},
		{"manual with non-matching annotation does not migrate", cd(UpgradePolicyManual, map[string]string{AnnotationKeyUpgradeToVersion: "v1-0-0"}), "v2-0-0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cd.MigrationApproved(tc.target); got != tc.want {
				t.Errorf("MigrationApproved(%q) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}
