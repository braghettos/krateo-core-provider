package handlers

import (
	"log/slog"

	helmconfig "github.com/krateo-platformops/plumbing/helm"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

type pluralizer interface {
	GVKtoGVR(gvk schema.GroupVersionKind) (schema.GroupVersionResource, error)
}

type HandlerOptions struct {
	Log             *slog.Logger
	DynamicClient   dynamic.Interface
	KrateoNamespace string
	Plurarizer      pluralizer
	RestConfig      *rest.Config
	HelmClient      helmconfig.Client
}
