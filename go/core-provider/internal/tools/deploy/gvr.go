package deploy

import "k8s.io/apimachinery/pkg/runtime/schema"

// Well-known built-in GVRs, shared across this package's kube.Apply/Get/Uninstall call sites: unlike
// client.Client (which resolves a GVR from an object's GVK via its own REST mapper), the dynamic.Interface
// kube.Apply/Get/Uninstall now delegate to requires the caller to supply it explicitly.
var (
	namespaceGVR          = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	serviceAccountGVR     = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
	configMapGVR          = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	secretGVR             = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	serviceGVR            = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	deploymentGVR         = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	clusterRoleGVR        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	roleGVR               = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	roleBindingGVR        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	crdGVR                = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)
