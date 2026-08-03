package dynamic

import (
	xenv "github.com/krateo-platformops/plumbing/env"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"

	"k8s.io/client-go/restmapper"
)

func NewRESTMapper(rc *rest.Config) (meta.RESTMapper, error) {
	if rc == nil && !xenv.TestMode() {
		var err error
		rc, err = rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		return nil, err
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(
		memory.NewMemCacheClient(discoveryClient),
	)

	return mapper, nil
}

func IsNamespaced(mapper meta.RESTMapper, gvk schema.GroupVersionKind) (bool, error) {
	if mapper == nil {
		return false, nil
	}
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		if dm, ok := mapper.(interface{ Reset() }); ok {
			dm.Reset()
			mapping, err = mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				return false, err
			}
		} else {
			return false, err
		}
	}

	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		return false, nil
	}

	return true, nil
}
