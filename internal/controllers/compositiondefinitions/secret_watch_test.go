package compositiondefinitions

import (
	"testing"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
)

func secretSelector(name, namespace string) rtv1.SecretKeySelector {
	sel := rtv1.SecretKeySelector{Key: "kubeconfig"}
	sel.Name = name
	sel.Namespace = namespace
	return sel
}

func TestCompositionReferencesSecret(t *testing.T) {
	kubeconfigRef := secretSelector("prod-eu-kubeconfig", "demo-system")
	credsRef := secretSelector("chart-creds", "demo-system")

	cases := []struct {
		name   string
		cd     *compositiondefinitionsv1alpha1.CompositionDefinition
		ns     string
		secret string
		want   bool
	}{
		{
			name: "matches kubeconfig ref",
			cd: &compositiondefinitionsv1alpha1.CompositionDefinition{
				Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{
					Deploy: &compositiondefinitionsv1alpha1.DeploymentTarget{
						Mode:          compositiondefinitionsv1alpha1.DeploymentModeRemote,
						KubeconfigRef: &kubeconfigRef,
					},
				},
			},
			ns: "demo-system", secret: "prod-eu-kubeconfig", want: true,
		},
		{
			name: "matches chart credential ref",
			cd: &compositiondefinitionsv1alpha1.CompositionDefinition{
				Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{
					Chart: &compositiondefinitionsv1alpha1.ChartInfo{
						Url:         "oci://example.com/chart",
						Credentials: &compositiondefinitionsv1alpha1.Credentials{Username: "u", PasswordRef: credsRef},
					},
				},
			},
			ns: "demo-system", secret: "chart-creds", want: true,
		},
		{
			name: "wrong namespace does not match",
			cd: &compositiondefinitionsv1alpha1.CompositionDefinition{
				Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{
					Deploy: &compositiondefinitionsv1alpha1.DeploymentTarget{KubeconfigRef: &kubeconfigRef},
				},
			},
			ns: "other", secret: "prod-eu-kubeconfig", want: false,
		},
		{
			name:   "no refs does not match",
			cd:     &compositiondefinitionsv1alpha1.CompositionDefinition{},
			ns:     "demo-system",
			secret: "prod-eu-kubeconfig",
			want:   false,
		},
		{
			name: "local deploy without kubeconfig ref does not match",
			cd: &compositiondefinitionsv1alpha1.CompositionDefinition{
				Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{
					Deploy: &compositiondefinitionsv1alpha1.DeploymentTarget{Mode: compositiondefinitionsv1alpha1.DeploymentModeLocal},
				},
			},
			ns: "demo-system", secret: "prod-eu-kubeconfig", want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compositionReferencesSecret(tc.cd, tc.ns, tc.secret); got != tc.want {
				t.Fatalf("compositionReferencesSecret() = %v, want %v", got, tc.want)
			}
		})
	}
}
