package restactionrbac

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

func TestGenerate_GroupSubjectAndPerRowVerbs(t *testing.T) {
	readSet := []Resource{
		{Group: "", Version: "v1", Resource: "services", Namespace: "apps", Verb: "list"},
		{Group: "", Version: "v1", Resource: "configmaps", Namespace: "apps", Verb: "get"},
		{Group: "apps", Version: "v1", Resource: "deployments", Namespace: "apps", Verb: "list"},
		{Group: "", Version: "v1", Resource: "nodes", Namespace: "", Verb: "list"}, // cluster-scoped
	}

	g := Generate(readSet, "krateo:cdc:fireworksapps-v1-0-0", "fireworksapps-v1-0-0-restaction")

	// cluster-scoped row → ClusterRole + binding to the GROUP (never the SA).
	if g.ClusterRole == nil || g.ClusterRoleBinding == nil {
		t.Fatalf("expected a ClusterRole + binding for the cluster-scoped row")
	}
	if got := g.ClusterRoleBinding.Subjects[0]; got.Kind != rbacv1.GroupKind || got.Name != "krateo:cdc:fireworksapps-v1-0-0" {
		t.Errorf("binding subject = %+v; want Group krateo:cdc:fireworksapps-v1-0-0", got)
	}

	// one namespaced Role for "apps", bound to the group.
	if len(g.Roles) != 1 || g.Roles[0].Namespace != "apps" {
		t.Fatalf("expected one Role in namespace apps, got %+v", g.Roles)
	}
	if g.RoleBindings[0].Subjects[0].Kind != rbacv1.GroupKind {
		t.Errorf("RoleBinding subject must be the group")
	}

	// per-row verbs preserved, NOT "*": services→list, configmaps→get are distinct rules.
	verbsByResource := map[string]string{}
	for _, rule := range g.Roles[0].Rules {
		for _, res := range rule.Resources {
			if len(rule.Verbs) != 1 {
				t.Fatalf("expected one verb per aggregated rule, got %v", rule.Verbs)
			}
			verbsByResource[res] = rule.Verbs[0]
		}
	}
	if verbsByResource["services"] != "list" {
		t.Errorf("services verb = %q; want list (collection)", verbsByResource["services"])
	}
	if verbsByResource["configmaps"] != "get" {
		t.Errorf("configmaps verb = %q; want get (single object)", verbsByResource["configmaps"])
	}
	for res, v := range verbsByResource {
		if v == "*" {
			t.Errorf("resource %q granted '*'; apiRef RBAC must be least-privilege", res)
		}
	}
}

func TestGenerate_NoClusterRoleWhenAllNamespaced(t *testing.T) {
	g := Generate([]Resource{
		{Group: "", Version: "v1", Resource: "services", Namespace: "apps", Verb: "list"},
	}, "krateo:cdc:x-v1", "x-v1-restaction")
	if g.ClusterRole != nil || g.ClusterRoleBinding != nil {
		t.Errorf("no cluster-scoped rows → expected no ClusterRole/binding")
	}
}
