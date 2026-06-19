package getters

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	contexttools "github.com/krateoplatformops/core-provider/internal/tools/context"
	"github.com/krateoplatformops/core-provider/internal/tools/retry"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	"github.com/krateoplatformops/core-provider/internal/tools/deploy"
	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	compositionRetryAttempts     = 5
	compositionRetryInitialDelay = 250 * time.Millisecond
	compositionRetryMaximumDelay = 2 * time.Second
)

var retryWait = retry.Wait

func GetCompositions(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource) (*unstructured.UnstructuredList, error) {
	return GetCompositionsByVersionLabel(ctx, dyn, gvr, gvr.Version)
}

// DefinitionRef identifies the CompositionDefinition that owns a set of composition instances.
// An empty DefinitionRef means "do not scope by owner" (match instances of any definition).
type DefinitionRef struct {
	Name      string
	Namespace string
}

// GetCompositionsByVersionLabel lists compositions served at gvr whose
// krateo.io/composition-version label equals versionLabel, across ALL owning definitions. The
// LABEL (not the served apiVersion) identifies the owning per-version controller, because the
// vacuum storage version erases the apiVersion; so listing through any served endpoint with a
// given label finds those instances regardless of which version they were written through.
func GetCompositionsByVersionLabel(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, versionLabel string) (*unstructured.UnstructuredList, error) {
	return GetOwnedCompositionsByVersionLabel(ctx, dyn, gvr, versionLabel, DefinitionRef{})
}

// GetOwnedCompositionsByVersionLabel lists compositions served at gvr that carry versionLabel
// in composition-version AND (when owner is non-empty) are owned by that CompositionDefinition.
// Owner scoping matters because one CRD/Kind can be shared by multiple CompositionDefinitions at
// different versions; migration must only ever touch its own definition's instances.
func GetOwnedCompositionsByVersionLabel(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, versionLabel string, owner DefinitionRef) (*unstructured.UnstructuredList, error) {
	log := contexttools.LoggerFromCtx(ctx, logging.NewNopLogger())

	selector, err := compositionSelector(versionLabel, owner)
	if err != nil {
		log.Debug("Error creating label selector", "error", err)
		return nil, fmt.Errorf("error creating label selector: %w", err)
	}

	ul, err := dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		log.Debug("Error listing compositions", "error", err)
		return nil, fmt.Errorf("error listing compositions: %w", err)
	}

	return ul, nil
}

// compositionSelector builds the label selector for the composition-version label plus, when
// owner is set, the composition-definition-name/-namespace ownership labels.
func compositionSelector(versionLabel string, owner DefinitionRef) (string, error) {
	selector := labels.NewSelector()
	reqs := []struct{ key, val string }{{deploy.CompositionVersionLabel, versionLabel}}
	if owner.Name != "" {
		reqs = append(reqs, struct{ key, val string }{deploy.CompositionDefinitionNameLabel, owner.Name})
	}
	if owner.Namespace != "" {
		reqs = append(reqs, struct{ key, val string }{deploy.CompositionDefinitionNamespaceLabel, owner.Namespace})
	}
	for _, r := range reqs {
		req, err := labels.NewRequirement(r.key, selection.Equals, []string{r.val})
		if err != nil {
			return "", err
		}
		selector = selector.Add(*req)
	}
	return selector.String(), nil
}

// UpdateCompositionsVersion re-stamps every composition OWNED BY owner and currently labelled
// fromVersion to toVersion, performing BOTH the list and the update THROUGH the gvr endpoint —
// whose served version must be toVersion. This is deliberate: the in-apiserver
// composition-version MutatingAdmissionPolicy stamps the label from the REQUEST's served
// version, so a write through the old endpoint would re-stamp the old version and silently undo
// the migration (leaving the composition selected by the retired old-version controller and
// orphaned by the new one). Writing through the toVersion endpoint makes the policy agree with
// the explicit relabel; the explicit label set in updateCompositionWithRetry also covers
// clusters where the policy is not installed. Owner scoping ensures a version bump of one
// definition never re-stamps another definition's instances that legitimately share the CRD.
func UpdateCompositionsVersion(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, fromVersion, toVersion string, owner DefinitionRef) error {
	log := contexttools.LoggerFromCtx(ctx, logging.NewNopLogger())

	if fromVersion == toVersion {
		return nil
	}

	ul, err := getCompositionsWithRetry(ctx, dyn, gvr, fromVersion, owner, log)
	if err != nil {
		return fmt.Errorf("error getting compositions: %w", err)
	}

	if len(ul.Items) == 0 {
		log.Debug("No compositions found for the specified GVR and version", "fromVersion", fromVersion, "owner", owner.Name)
		return nil
	}

	for _, u := range ul.Items {
		if err := updateCompositionWithRetry(ctx, dyn, gvr, u.GetNamespace(), u.GetName(), toVersion, log); err != nil {
			return err
		}
	}

	return nil
}

func getCompositionsWithRetry(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, versionLabel string, owner DefinitionRef, log logging.Logger) (*unstructured.UnstructuredList, error) {
	ul, err := retry.Do[*unstructured.UnstructuredList](ctx, retry.Config[*unstructured.UnstructuredList]{
		Attempts:     compositionRetryAttempts,
		InitialDelay: compositionRetryInitialDelay,
		MaximumDelay: compositionRetryMaximumDelay,
		Wait:         retryWait,
		Retryable:    isRetryableCompositionError,
		OnRetry: func(attempt int, nextDelay time.Duration, err error) {
			log.Warn("Retrying composition list", "gvr", gvr.String(), "versionLabel", versionLabel, "attempt", attempt, "next_delay", nextDelay, "error", err)
		},
	}, func(context.Context) (*unstructured.UnstructuredList, error) {
		return GetOwnedCompositionsByVersionLabel(ctx, dyn, gvr, versionLabel, owner)
	})
	if err != nil {
		return nil, fmt.Errorf("error listing compositions after %d attempts: %w", compositionRetryAttempts, err)
	}

	return ul, nil
}

func updateCompositionWithRetry(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, name, newVersion string, log logging.Logger) error {
	compositionName := compositionKey(namespace, name)
	_, err := retry.Do[struct{}](ctx, retry.Config[struct{}]{
		Attempts:     compositionRetryAttempts,
		InitialDelay: compositionRetryInitialDelay,
		MaximumDelay: compositionRetryMaximumDelay,
		Wait:         retryWait,
		Retryable:    isRetryableCompositionError,
		OnRetry: func(attempt int, nextDelay time.Duration, err error) {
			log.Warn("Retrying composition update", "composition", compositionName, "gvr", gvr.String(), "attempt", attempt, "next_delay", nextDelay, "error", err)
		},
	}, func(context.Context) (struct{}, error) {
		u, err := dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			log.Debug("Composition disappeared before version update, skipping", "composition", compositionName, "gvr", gvr.String())
			return struct{}{}, nil
		}
		if err != nil {
			return struct{}{}, fmt.Errorf("error getting composition %s: %w", compositionName, err)
		}

		labelmap, ok, err := unstructured.NestedStringMap(u.Object, "metadata", "labels")
		if err != nil {
			return struct{}{}, fmt.Errorf("error getting labels from composition %s: %w", compositionName, err)
		}
		if !ok {
			labelmap = make(map[string]string)
		}
		if labelmap[deploy.CompositionVersionLabel] == newVersion {
			return struct{}{}, nil
		}

		labelmap[deploy.CompositionVersionLabel] = newVersion
		if err := unstructured.SetNestedStringMap(u.Object, labelmap, "metadata", "labels"); err != nil {
			return struct{}{}, fmt.Errorf("error setting labels on composition %s: %w", compositionName, err)
		}

		if _, err := dyn.Resource(gvr).Namespace(namespace).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				log.Debug("Composition disappeared during version update, skipping", "composition", compositionName, "gvr", gvr.String())
				return struct{}{}, nil
			}
			return struct{}{}, err
		}

		return struct{}{}, nil
	})
	if err != nil {
		return fmt.Errorf("composition %s update failed after %d attempts: %w", compositionName, compositionRetryAttempts, err)
	}

	return nil
}

func compositionKey(namespace, name string) string {
	if namespace == "" {
		return name
	}

	return namespace + "/" + name
}

func isRetryableCompositionError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	if isNonRetryableCompositionError(err) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}

	return true
}

func isNonRetryableCompositionError(err error) bool {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsNotFound(err) || apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
		return true
	}
	if statusErr, ok := err.(*apierrors.StatusError); ok {
		switch statusErr.ErrStatus.Code {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusBadRequest:
			return true
		}
	}

	return false
}

func GetCompositionDefinitions(ctx context.Context, cli client.Client, gk schema.GroupKind) ([]compositiondefinitionsv1alpha1.CompositionDefinition, error) {
	var cdList compositiondefinitionsv1alpha1.CompositionDefinitionList
	err := cli.List(ctx, &cdList, &client.ListOptions{Namespace: metav1.NamespaceAll})
	if err != nil {
		return nil, fmt.Errorf("error listing CompositionDefinitions: %s", err)
	}

	lst := []compositiondefinitionsv1alpha1.CompositionDefinition{}
	for i := range cdList.Items {
		cd := &cdList.Items[i]

		cdgvk := schema.FromAPIVersionAndKind(cd.Status.ApiVersion, cd.Status.Kind)
		if cdgvk.Group == gk.Group &&
			cdgvk.Kind == gk.Kind {
			lst = append(lst, *cd)
		}

		// if cd.Status.Managed.Group == gk.Group &&
		// 	cd.Status.Managed.Kind == gk.Kind {
		// 	lst = append(lst, *cd)
		// }
	}

	return lst, nil
}

// GetCompositionDefinitionsWithVersion retrieves CompositionDefinitions that match the specified Composition GVK
func GetCompositionDefinitionsWithVersion(ctx context.Context, cli client.Client, gvk schema.GroupVersionKind) ([]compositiondefinitionsv1alpha1.CompositionDefinition, error) {
	var cdList compositiondefinitionsv1alpha1.CompositionDefinitionList
	err := cli.List(ctx, &cdList, &client.ListOptions{Namespace: metav1.NamespaceAll})
	if err != nil {
		return nil, fmt.Errorf("error listing CompositionDefinitions: %s", err)
	}

	lst := []compositiondefinitionsv1alpha1.CompositionDefinition{}
	for i := range cdList.Items {
		cd := &cdList.Items[i]
		cdgvk := schema.FromAPIVersionAndKind(cd.Status.ApiVersion, cd.Status.Kind)
		if cdgvk.Group == gvk.Group && cdgvk.Kind == gvk.Kind && cdgvk.Version == gvk.Version {
			lst = append(lst, *cd)
		}
	}

	return lst, nil
}
