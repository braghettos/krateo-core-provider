package compositiondefinitions

import (
	"testing"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func discoveryWithVersion(t *testing.T, gitVersion string) *fakediscovery.FakeDiscovery {
	t.Helper()
	cs := fake.NewSimpleClientset()
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatal("could not get fake discovery")
	}
	fd.FakedServerVersion = &version.Info{GitVersion: gitVersion}
	return fd
}

func TestSetTargetStatus_LocalHealthy(t *testing.T) {
	e := &external{discovery: discoveryWithVersion(t, "v1.30.2")}
	cr := &compositiondefinitionsv1alpha1.CompositionDefinition{}

	e.setTargetStatus(cr)

	if cr.Status.Target == nil {
		t.Fatal("expected target status to be set")
	}
	if cr.Status.Target.Mode != string(compositiondefinitionsv1alpha1.DeploymentModeLocal) {
		t.Fatalf("mode = %q, want Local", cr.Status.Target.Mode)
	}
	if cr.Status.Target.ConnectionStatus != "Healthy" {
		t.Fatalf("connectionStatus = %q, want Healthy", cr.Status.Target.ConnectionStatus)
	}
	if cr.Status.Target.Version != "v1.30.2" {
		t.Fatalf("version = %q, want v1.30.2", cr.Status.Target.Version)
	}
	if cr.Status.Target.KubeconfigSecretResourceVersion != "" {
		t.Fatal("local target must not record a kubeconfig secret resourceVersion")
	}
}

func TestSetTargetStatus_RemoteRecordsSecretVersion(t *testing.T) {
	e := &external{
		discovery:             discoveryWithVersion(t, "v1.29.0"),
		remote:                true,
		secretResourceVersion: "12345",
	}
	cr := &compositiondefinitionsv1alpha1.CompositionDefinition{
		Spec: compositiondefinitionsv1alpha1.CompositionDefinitionSpec{
			Deploy: &compositiondefinitionsv1alpha1.DeploymentTarget{
				Mode: compositiondefinitionsv1alpha1.DeploymentModeRemote,
			},
		},
	}

	e.setTargetStatus(cr)

	if cr.Status.Target.Mode != string(compositiondefinitionsv1alpha1.DeploymentModeRemote) {
		t.Fatalf("mode = %q, want Remote", cr.Status.Target.Mode)
	}
	if cr.Status.Target.KubeconfigSecretResourceVersion != "12345" {
		t.Fatalf("kubeconfigSecretResourceVersion = %q, want 12345", cr.Status.Target.KubeconfigSecretResourceVersion)
	}
}
