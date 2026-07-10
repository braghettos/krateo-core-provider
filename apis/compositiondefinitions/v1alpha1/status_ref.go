package v1alpha1

import "k8s.io/apimachinery/pkg/runtime/schema"

// The current generated-CRD reference is stored on the status as the projection fields
// ApiVersion / Kind / Resource (the "last applied" GVK+resource). CurrentGVK and CurrentGVR are the
// single authoritative way to reconstruct that reference, so callers (version migration, the
// per-version reference count) no longer each re-derive it from the raw string fields — one place
// owns the projection (#234A).

// CurrentGVK returns the GroupVersionKind the CompositionDefinition currently resolves to, derived
// from status.apiVersion + status.kind. It is the zero GVK until the definition has been reconciled.
func (s CompositionDefinitionStatus) CurrentGVK() schema.GroupVersionKind {
	return schema.FromAPIVersionAndKind(s.ApiVersion, s.Kind)
}

// CurrentGVR returns the GroupVersionResource for the current version, derived from CurrentGVK plus
// status.resource.
func (s CompositionDefinitionStatus) CurrentGVR() schema.GroupVersionResource {
	return s.CurrentGVK().GroupVersion().WithResource(s.Resource)
}
