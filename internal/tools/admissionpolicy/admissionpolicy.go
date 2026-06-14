// Package admissionpolicy provisions the cluster-wide MutatingAdmissionPolicy that
// stamps each composition instance with the served version it was written through
// (label krateo.io/composition-version). This is the in-apiserver (CEL) replacement for
// the former /mutate admission webhook: no webhook server, serving cert, or
// MutatingWebhookConfiguration.
//
// It requires the MutatingAdmissionPolicy GA API (admissionregistration.k8s.io/v1),
// available from Kubernetes 1.36, on every cluster a composition CRD lives in (the
// management cluster and any remote targets). Objects are built unstructured because the
// GA Go types post-date the vendored client libraries.
package admissionpolicy

import (
	"context"
	"fmt"

	"github.com/krateoplatformops/core-provider/internal/tools/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	apiVersion = "admissionregistration.k8s.io/v1"

	policyName  = "krateo-composition-version"
	bindingName = "krateo-composition-version"

	// compositionGroup is the API group shared by all generated composition CRDs.
	compositionGroup = "composition.krateo.io"
	// versionLabel records which served version a composition was written through.
	versionLabel = "krateo.io/composition-version"
)

// labelExpression sets the composition-version label from the request's original
// (pre-conversion) version using the ApplyConfiguration CEL form.
var labelExpression = fmt.Sprintf(
	`Object{ metadata: Object.metadata{ labels: {%q: request.requestKind.version} } }`,
	versionLabel,
)

func policy() *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion(apiVersion)
	o.SetKind("MutatingAdmissionPolicy")
	o.SetName(policyName)
	o.Object["spec"] = map[string]any{
		"matchConstraints": map[string]any{
			"matchPolicy": "Exact",
			"resourceRules": []any{
				map[string]any{
					"apiGroups":   []any{compositionGroup},
					"apiVersions": []any{"*"},
					"operations":  []any{"CREATE", "UPDATE"},
					"resources":   []any{"*"},
				},
			},
		},
		"failurePolicy":      "Fail",
		"reinvocationPolicy": "Never",
		"mutations": []any{
			map[string]any{
				"patchType": "ApplyConfiguration",
				"applyConfiguration": map[string]any{
					"expression": labelExpression,
				},
			},
		},
	}
	return o
}

func binding() *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion(apiVersion)
	o.SetKind("MutatingAdmissionPolicyBinding")
	o.SetName(bindingName)
	o.Object["spec"] = map[string]any{
		"policyName": policyName,
	}
	return o
}

// Ensure idempotently applies the MutatingAdmissionPolicy and its binding to the cluster
// reachable by kube (the management cluster, or a remote target). It is safe to call on
// every reconcile.
func Ensure(ctx context.Context, kc client.Client) error {
	for _, obj := range []*unstructured.Unstructured{policy(), binding()} {
		if err := kube.Apply(ctx, kc, obj, kube.ApplyOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("MutatingAdmissionPolicy API (%s) not found - the cluster must run Kubernetes >= 1.36: %w", apiVersion, err)
			}
			return fmt.Errorf("applying %s %q: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return nil
}
