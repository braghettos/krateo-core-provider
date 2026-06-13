package clusterkube

import (
	"context"
	"testing"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func kubeconfigRef() *rtv1.SecretKeySelector {
	ref := &rtv1.SecretKeySelector{Key: "kubeconfig"}
	ref.Name = "prod-eu-kubeconfig"
	ref.Namespace = "demo-system"
	return ref
}

func TestIsRemote(t *testing.T) {
	cases := []struct {
		name   string
		target *compositiondefinitionsv1alpha1.DeploymentTarget
		want   bool
	}{
		{"nil target", nil, false},
		{"empty mode", &compositiondefinitionsv1alpha1.DeploymentTarget{}, false},
		{"local", &compositiondefinitionsv1alpha1.DeploymentTarget{Mode: compositiondefinitionsv1alpha1.DeploymentModeLocal}, false},
		{"remote", &compositiondefinitionsv1alpha1.DeploymentTarget{Mode: compositiondefinitionsv1alpha1.DeploymentModeRemote}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRemote(tc.target); got != tc.want {
				t.Fatalf("IsRemote() = %v, want %v", got, tc.want)
			}
		})
	}
}

func newFakeClient(objs ...runtime.Object) *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...)
}

func remoteTarget() *compositiondefinitionsv1alpha1.DeploymentTarget {
	return &compositiondefinitionsv1alpha1.DeploymentTarget{
		Mode:          compositiondefinitionsv1alpha1.DeploymentModeRemote,
		KubeconfigRef: kubeconfigRef(),
	}
}

func TestRemote_NotRemote(t *testing.T) {
	c := newFakeClient().Build()
	if _, err := Remote(context.Background(), c, &compositiondefinitionsv1alpha1.DeploymentTarget{Mode: compositiondefinitionsv1alpha1.DeploymentModeLocal}); err == nil {
		t.Fatal("expected error for non-remote target")
	}
}

func TestRemote_MissingKubeconfigRef(t *testing.T) {
	c := newFakeClient().Build()
	target := &compositiondefinitionsv1alpha1.DeploymentTarget{Mode: compositiondefinitionsv1alpha1.DeploymentModeRemote}
	if _, err := Remote(context.Background(), c, target); err == nil {
		t.Fatal("expected error for missing kubeconfigRef")
	}
}

func TestRemote_SecretNotFound(t *testing.T) {
	c := newFakeClient().Build()
	if _, err := Remote(context.Background(), c, remoteTarget()); err == nil {
		t.Fatal("expected error when kubeconfig secret is missing")
	}
}

func TestRemote_EmptyKubeconfig(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-eu-kubeconfig", Namespace: "demo-system"},
		Data:       map[string][]byte{"kubeconfig": []byte("")},
	}
	c := newFakeClient(secret).Build()
	if _, err := Remote(context.Background(), c, remoteTarget()); err == nil {
		t.Fatal("expected error for empty kubeconfig")
	}
}

func TestRemote_InvalidKubeconfig(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-eu-kubeconfig", Namespace: "demo-system"},
		Data:       map[string][]byte{"kubeconfig": []byte("not-a-valid-kubeconfig")},
	}
	c := newFakeClient(secret).Build()
	if _, err := Remote(context.Background(), c, remoteTarget()); err == nil {
		t.Fatal("expected error for invalid kubeconfig")
	}
}

func TestRemote_ValidKubeconfig(t *testing.T) {
	const kubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: target
  cluster:
    server: https://target.example.com:6443
    insecure-skip-tls-verify: true
contexts:
- name: target
  context:
    cluster: target
    user: target
current-context: target
users:
- name: target
  user:
    token: abc123
`
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-eu-kubeconfig", Namespace: "demo-system"},
		Data:       map[string][]byte{"kubeconfig": []byte(kubeconfig)},
	}
	c := newFakeClient(secret).Build()
	got, err := Remote(context.Background(), c, remoteTarget())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Config.Host != "https://target.example.com:6443" {
		t.Fatalf("unexpected host: %q", got.Config.Host)
	}
	if got.Kube == nil || got.Dynamic == nil || got.Clientset == nil {
		t.Fatal("expected non-nil clients")
	}
	if got.SecretResourceVersion == "" {
		t.Fatal("expected SecretResourceVersion to be captured from the kubeconfig Secret")
	}
}
