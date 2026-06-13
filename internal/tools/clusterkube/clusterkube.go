// Package clusterkube resolves the Kubernetes clients used to deploy the
// composition-dynamic-controller (and its generated CRD and RBAC) to the cluster
// selected by a CompositionDefinition: the local management cluster (default) or a
// remote target cluster addressed by a kubeconfig Secret.
//
// Credentials are consumed as native Kubernetes Secrets and re-read on every
// reconcile, so rotation performed by an external secret manager (e.g. External
// Secrets Operator) is picked up automatically.
package clusterkube

import (
	"context"
	"fmt"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Clients bundles the clients needed to install/observe resources in a target cluster.
type Clients struct {
	Kube      client.Client
	Dynamic   dynamic.Interface
	Discovery discovery.DiscoveryInterface
	Config    *rest.Config
	// SecretResourceVersion is the resourceVersion of the kubeconfig Secret these
	// clients were built from, for rotation traceability (empty for local targets).
	SecretResourceVersion string
}

// IsRemote reports whether the deployment target selects a remote cluster.
func IsRemote(target *compositiondefinitionsv1alpha1.DeploymentTarget) bool {
	return target != nil && target.Mode == compositiondefinitionsv1alpha1.DeploymentModeRemote
}

// Remote builds the clients for a remote target cluster from the kubeconfig stored in
// the Secret referenced by target.KubeconfigRef. The Secret is read from the management
// cluster via mgmt.
func Remote(ctx context.Context, mgmt client.Client, target *compositiondefinitionsv1alpha1.DeploymentTarget) (*Clients, error) {
	if !IsRemote(target) {
		return nil, fmt.Errorf("deployment target is not remote")
	}
	if target.KubeconfigRef == nil {
		return nil, fmt.Errorf("kubeconfigRef is required when deployment mode is Remote")
	}

	secret := &corev1.Secret{}
	if err := mgmt.Get(ctx, types.NamespacedName{
		Namespace: target.KubeconfigRef.Namespace,
		Name:      target.KubeconfigRef.Name,
	}, secret); err != nil {
		return nil, fmt.Errorf("reading kubeconfig secret %s/%s: %w",
			target.KubeconfigRef.Namespace, target.KubeconfigRef.Name, err)
	}

	kubeconfig := secret.Data[target.KubeconfigRef.Key]
	if len(kubeconfig) == 0 {
		return nil, fmt.Errorf("kubeconfig secret %s/%s key %q is empty",
			target.KubeconfigRef.Namespace, target.KubeconfigRef.Name, target.KubeconfigRef.Key)
	}

	rc, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig from secret %s/%s: %w",
			target.KubeconfigRef.Namespace, target.KubeconfigRef.Name, err)
	}

	clients, err := clientsFor(rc)
	if err != nil {
		return nil, err
	}
	clients.SecretResourceVersion = secret.ResourceVersion
	return clients, nil
}

func clientsFor(rc *rest.Config) (*Clients, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return nil, err
	}

	cl, err := client.New(rc, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("building target kube client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("building target dynamic client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("building target discovery client: %w", err)
	}

	return &Clients{Kube: cl, Dynamic: dyn, Discovery: disc, Config: rc}, nil
}
