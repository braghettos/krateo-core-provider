package restactionrbac

import (
	"sort"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Generated is the RBAC that authorizes a per-composition group to perform the
// reads a RESTAction's apiRef resolves. The subject is always the group
// krateo:cdc:<resource>-<apiVersion> (NOT the CDC ServiceAccount): snowplow
// resolves the RESTAction under the per-user clientconfig bound to that group.
type Generated struct {
	ClusterRole        *rbacv1.ClusterRole        // nil when no cluster-scoped rows
	ClusterRoleBinding *rbacv1.ClusterRoleBinding  // nil when no cluster-scoped rows
	Roles              []rbacv1.Role               // one per namespace with rows
	RoleBindings       []rbacv1.RoleBinding        // paired with Roles
}

// Generate turns a read-set into group-bound RBAC. name is the metadata name for
// the generated objects (e.g. "<plural>-<version>-restaction"); group is the
// subject group. Cluster-scoped rows (Namespace=="") produce a ClusterRole;
// namespaced rows produce one Role per namespace. Within each role, rows are
// aggregated by (apiGroup, verb) and canonicalised (sorted), so an unchanged
// read-set yields byte-identical objects (digest stability).
//
// Unlike chart rbacgen (one "*" rule per resource bound to the SA), the verb
// comes from the read-set row — precise least privilege.
func Generate(readSet []Resource, group, name string) Generated {
	var clusterRows []Resource
	byNamespace := map[string][]Resource{}
	for _, r := range readSet {
		if r.Namespace == "" {
			clusterRows = append(clusterRows, r)
			continue
		}
		byNamespace[r.Namespace] = append(byNamespace[r.Namespace], r)
	}

	groupSubject := rbacv1.Subject{
		Kind:     rbacv1.GroupKind,
		Name:     group,
		APIGroup: rbacv1.GroupName,
	}

	var out Generated

	if len(clusterRows) > 0 {
		out.ClusterRole = &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Rules:      buildRules(clusterRows),
		}
		out.ClusterRoleBinding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Subjects:   []rbacv1.Subject{groupSubject},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     name,
			},
		}
	}

	for _, ns := range sortedKeys(byNamespace) {
		out.Roles = append(out.Roles, rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Rules:      buildRules(byNamespace[ns]),
		})
		out.RoleBindings = append(out.RoleBindings, rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Subjects:   []rbacv1.Subject{groupSubject},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "Role",
				Name:     name,
			},
		})
	}

	return out
}

// buildRules aggregates rows by (apiGroup, verb) into PolicyRules, sorted for
// determinism. Each row contributes its resource under its own verb.
func buildRules(rows []Resource) []rbacv1.PolicyRule {
	// key = apiGroup + "\x00" + verb → set of resources
	type key struct{ group, verb string }
	agg := map[key]map[string]struct{}{}
	for _, r := range rows {
		k := key{group: r.Group, verb: r.Verb}
		if agg[k] == nil {
			agg[k] = map[string]struct{}{}
		}
		agg[k][r.Resource] = struct{}{}
	}

	keys := make([]key, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].group != keys[j].group {
			return keys[i].group < keys[j].group
		}
		return keys[i].verb < keys[j].verb
	})

	rules := make([]rbacv1.PolicyRule, 0, len(keys))
	for _, k := range keys {
		resources := make([]string, 0, len(agg[k]))
		for res := range agg[k] {
			resources = append(resources, res)
		}
		sort.Strings(resources)
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{k.group},
			Resources: resources,
			Verbs:     []string{k.verb},
		})
	}
	return rules
}

func sortedKeys(m map[string][]Resource) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
