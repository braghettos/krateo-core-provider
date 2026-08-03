// Package policy projects the cluster-wide composition-version MutatingAdmissionPolicy
// into the cluster where composition CRDs live.
//
// core-provider hosts no admission webhooks: the krateo.io/composition-version label
// (which per-version listing/migration and safe deletion rely on) is stamped onto
// composition instances by an in-apiserver MutatingAdmissionPolicy. Because instances are
// created in the TARGET cluster, that policy must exist there. For local targets the
// management chart ships it; for remote targets core-provider projects it during
// bootstrap so the label is reliably present without a manual onboarding step.
package policy

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// policyAPIVersion is the GA MutatingAdmissionPolicy API, on by default since
	// Kubernetes 1.36 (the floor for remote targets).
	policyAPIVersion = "admissionregistration.k8s.io/v1"

	// PolicyName and BindingName are the cluster-wide singletons projected into every
	// target cluster that hosts composition CRDs.
	PolicyName  = "krateo-composition-version"
	BindingName = "krateo-composition-version"

	// compositionGroup is the API group of all generated composition CRDs.
	compositionGroup = "composition.krateo.io"

	// versionLabel must match deploy.CompositionVersionLabel. The policy stamps the
	// request's served version onto this label so per-version listing and migration keep
	// working after the vacuum storage version erases the apiVersion.
	versionLabel = "krateo.io/composition-version"
)

// objects returns the MutatingAdmissionPolicy and its binding as unstructured objects.
func objects() (*unstructured.Unstructured, *unstructured.Unstructured) {
	p := &unstructured.Unstructured{}
	p.SetAPIVersion(policyAPIVersion)
	p.SetKind("MutatingAdmissionPolicy")
	p.SetName(PolicyName)
	p.Object["spec"] = map[string]any{
		"matchConstraints": map[string]any{
			"matchPolicy": "Exact",
			"resourceRules": []any{map[string]any{
				"apiGroups":   []any{compositionGroup},
				"apiVersions": []any{"*"},
				"operations":  []any{"CREATE", "UPDATE"},
				"resources":   []any{"*"},
			}},
		},
		"failurePolicy":      "Fail",
		"reinvocationPolicy": "Never",
		"mutations": []any{map[string]any{
			"patchType": "ApplyConfiguration",
			"applyConfiguration": map[string]any{
				"expression": `Object{ metadata: Object.metadata{ labels: {"` + versionLabel + `": request.requestKind.version} } }`,
			},
		}},
	}

	b := &unstructured.Unstructured{}
	b.SetAPIVersion(policyAPIVersion)
	b.SetKind("MutatingAdmissionPolicyBinding")
	b.SetName(BindingName)
	b.Object["spec"] = map[string]any{"policyName": PolicyName}

	return p, b
}

// EnsureCompositionVersionPolicy guarantees the composition-version policy (and its
// binding) exist in the cluster reached by kube. It is create-if-absent and idempotent:
// an existing policy (e.g. one shipped by a chart or installed by an operator) is left
// untouched, so this never fights another field manager.
//
// The policy is a cluster singleton shared by every composition CRD, so it is
// intentionally never removed on CompositionDefinition deletion.
//
// Requires the GA MutatingAdmissionPolicy API (admissionregistration.k8s.io/v1),
// i.e. Kubernetes >= 1.36 on the target.
func EnsureCompositionVersionPolicy(ctx context.Context, kube client.Client) error {
	p, b := objects()
	// Create the policy before the binding: the binding references it by name.
	for _, o := range []*unstructured.Unstructured{p, b} {
		if err := kube.Create(ctx, o); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}

	return nil
}
