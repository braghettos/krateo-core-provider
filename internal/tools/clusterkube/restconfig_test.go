package clusterkube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: spoke
  cluster:
    server: https://kube.example.com:6443
    insecure-skip-tls-verify: true
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
current-context: spoke
users:
- name: spoke
  user:
    token: kubeconfig-token
`

func TestRestConfigFromSecret_KubeconfigForm(t *testing.T) {
	s := &corev1.Secret{Data: map[string][]byte{"kubeconfig": []byte(testKubeconfig)}}
	rc, err := restConfigFromSecret(s, "kubeconfig")
	if err != nil {
		t.Fatalf("form (a): %v", err)
	}
	if rc.Host != "https://kube.example.com:6443" {
		t.Fatalf("host = %q", rc.Host)
	}
	if rc.QPS == 0 || rc.Burst == 0 {
		t.Fatalf("QPS/Burst not defaulted: qps=%v burst=%v", rc.QPS, rc.Burst)
	}
}

func TestRestConfigFromSecret_TokenForm(t *testing.T) {
	s := &corev1.Secret{Data: map[string][]byte{
		"token":  []byte("  sa-bearer-token\n"),
		"server": []byte("https://spoke.example.com:6443\n"),
		"ca.crt": []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"),
	}}
	// ref.Key points at "kubeconfig" (the default) which is absent -> falls back to token form.
	rc, err := restConfigFromSecret(s, "kubeconfig")
	if err != nil {
		t.Fatalf("form (b): %v", err)
	}
	if rc.Host != "https://spoke.example.com:6443" {
		t.Fatalf("host = %q (server not trimmed/used)", rc.Host)
	}
	if rc.BearerToken != "sa-bearer-token" {
		t.Fatalf("token = %q (not trimmed/used)", rc.BearerToken)
	}
	if len(rc.TLSClientConfig.CAData) == 0 {
		t.Fatal("ca.crt not wired into TLSClientConfig")
	}
	if rc.QPS == 0 || rc.Burst == 0 {
		t.Fatalf("QPS/Burst not defaulted")
	}
}

func TestRestConfigFromSecret_NeitherFormErrors(t *testing.T) {
	s := &corev1.Secret{Data: map[string][]byte{"unrelated": []byte("x")}}
	if _, err := restConfigFromSecret(s, "kubeconfig"); err == nil {
		t.Fatal("expected error when the secret has neither a kubeconfig nor token+server")
	}
	// token without server is not enough
	s2 := &corev1.Secret{Data: map[string][]byte{"token": []byte("t")}}
	if _, err := restConfigFromSecret(s2, "kubeconfig"); err == nil {
		t.Fatal("expected error when server is missing")
	}
}
