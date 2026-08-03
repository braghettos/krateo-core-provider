package getter

import (
	"context"
	"fmt"

	coreprovv1 "github.com/krateo-platformops/core-provider/apis/compositiondefinitions/v1alpha1"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	CompositionDefinitionGroup    = "core.krateo.io"
	CompositionDefinitionVersion  = "v1alpha1"
	CompositionDefinitionResource = "compositiondefinitions"
)

func (c *Client) GetCompositionDefinition(uid string, namespace string) (*coreprovv1.CompositionDefinition, error) {
	li, err := c.dynamic.Resource(schema.GroupVersionResource{
		Group:    CompositionDefinitionGroup,
		Version:  CompositionDefinitionVersion,
		Resource: CompositionDefinitionResource,
	}).Namespace(namespace).List(context.Background(), v1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list composition definitions: %v", err)
	}

	for _, item := range li.Items {
		if string(item.GetUID()) == uid {
			var compDef coreprovv1.CompositionDefinition
			err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &compDef)
			return &compDef, err
		}
	}

	return nil, errors.NewNotFound(schema.GroupResource{
		Group:    CompositionDefinitionGroup,
		Resource: CompositionDefinitionResource,
	}, uid)
}

func (c *Client) GetSecret(selector rtv1.SecretKeySelector) (string, error) {
	sec, err := c.dynamic.Resource(schema.GroupVersionResource{
		Version:  "v1",
		Resource: "secrets",
		Group:    "",
	}).Namespace(selector.Namespace).Get(context.Background(), selector.Name, v1.GetOptions{})
	if err != nil {
		return "", errors.NewNotFound(schema.GroupResource{
			Resource: "secrets",
		}, selector.Name)
	}
	var secret corev1.Secret
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(sec.Object, &secret); err != nil {
		return "", fmt.Errorf("failed to convert secret: %v", err)
	}

	return string(secret.Data[selector.Key]), nil
}
