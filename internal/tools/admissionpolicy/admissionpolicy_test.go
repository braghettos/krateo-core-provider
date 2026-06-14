package admissionpolicy

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPolicy_Shape(t *testing.T) {
	p := policy()

	if got := p.GetAPIVersion(); got != "admissionregistration.k8s.io/v1" {
		t.Fatalf("apiVersion = %q", got)
	}
	if got := p.GetKind(); got != "MutatingAdmissionPolicy" {
		t.Fatalf("kind = %q", got)
	}
	if got := p.GetName(); got != policyName {
		t.Fatalf("name = %q", got)
	}

	rules, _, _ := unstructured.NestedSlice(p.Object, "spec", "matchConstraints", "resourceRules")
	if len(rules) != 1 {
		t.Fatalf("expected 1 resourceRule, got %d", len(rules))
	}
	rule := rules[0].(map[string]any)
	groups, _, _ := unstructured.NestedSlice(rule, "apiGroups")
	if len(groups) != 1 || groups[0] != compositionGroup {
		t.Fatalf("apiGroups = %v, want [%s]", groups, compositionGroup)
	}

	muts, _, _ := unstructured.NestedSlice(p.Object, "spec", "mutations")
	if len(muts) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(muts))
	}
	expr, _, _ := unstructured.NestedString(muts[0].(map[string]any), "applyConfiguration", "expression")
	if !strings.Contains(expr, versionLabel) {
		t.Fatalf("expression missing the version label: %q", expr)
	}
	if !strings.Contains(expr, "request.requestKind.version") {
		t.Fatalf("expression should stamp the request's version: %q", expr)
	}
}

func TestBinding_ReferencesPolicy(t *testing.T) {
	b := binding()
	if got := b.GetKind(); got != "MutatingAdmissionPolicyBinding" {
		t.Fatalf("kind = %q", got)
	}
	name, _, _ := unstructured.NestedString(b.Object, "spec", "policyName")
	if name != policyName {
		t.Fatalf("policyName = %q, want %q", name, policyName)
	}
}
