package kube

import (
	"github.com/krateo-platformops/plumbing/kubeutil/objectclient"
)

// ApplyOptions and UninstallOptions are aliases (not copies) of plumbing/kubeutil/objectclient's
// identically-shaped types, so every existing kube.ApplyOptions{...}/kube.UninstallOptions{...} call site
// keeps compiling unchanged.
type (
	ApplyOptions     = objectclient.ApplyOptions
	UninstallOptions = objectclient.UninstallOptions
)
