package resolvers

import (
	"context"

	"github.com/krateo-platformops/plumbing/kubeutil/secretref"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"k8s.io/client-go/dynamic"
)

// GetSecret reads and base64-decodes secretKeySelector's key from its Secret. A thin wrapper over
// plumbing/kubeutil/secretref, kept in this package (rather than switching every call site to the
// plumbing import directly) so the rtv1.SecretKeySelector -> (namespace, name, key) unpacking lives in
// exactly one place.
func GetSecret(ctx context.Context, dyn dynamic.Interface, secretKeySelector rtv1.SecretKeySelector) (string, error) {
	return secretref.GetSecretValue(ctx, dyn, secretKeySelector.Namespace, secretKeySelector.Name, secretKeySelector.Key)
}
