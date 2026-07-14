// Package remoteinstalls reconciles RemoteInstall objects (Phase 2). A RemoteInstall is the
// one-object intent "install this chart on this spoke": the controller owns a remote-targeted
// CompositionDefinition (chart + deploy.targetRef → the KubernetesTarget) and rolls its readiness,
// plus the target's reachability, up into the RemoteInstall status. Deleting the RemoteInstall
// garbage-collects the CompositionDefinition (owner ref), whose own Delete tears the spoke down.
package remoteinstalls

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	"github.com/krateoplatformops/core-provider/internal/tools/clusterkube"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
	"github.com/krateoplatformops/provider-runtime/pkg/controller"
	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	phasePending    = "Pending"
	phaseInstalling = "Installing"
	phaseReady      = "Ready"
	phaseFailed     = "Failed"

	requeueInterval = 30 * time.Second
)

// Options configures the RemoteInstall reconciler.
type Options struct {
	ControllerOptions controller.Options
}

// Setup wires the reconciler into the manager, watching RemoteInstalls and the CompositionDefinitions
// they own.
func Setup(mgr ctrl.Manager, o Options) error {
	r := &Reconciler{
		client: mgr.GetClient(),
		scheme: mgr.GetScheme(),
		log:    o.ControllerOptions.Logger.WithValues("controller", "remoteinstall"),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&compositiondefinitionsv1alpha1.RemoteInstall{}).
		Owns(&compositiondefinitionsv1alpha1.CompositionDefinition{}).
		Named("remoteinstall").
		Complete(r)
}

// Reconciler owns a CompositionDefinition per RemoteInstall and mirrors its status.
type Reconciler struct {
	client client.Client
	scheme *runtime.Scheme
	log    logging.Logger
}

// Reconcile ensures the owned CompositionDefinition matches the RemoteInstall intent and rolls its
// status up.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	ri := &compositiondefinitionsv1alpha1.RemoteInstall{}
	if err := r.client.Get(ctx, req.NamespacedName, ri); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Desired CompositionDefinition: same name/namespace as the RemoteInstall, wiring the chart to
	// the remote target. Owned by the RemoteInstall so it is garbage-collected on delete.
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: ri.Name, Namespace: ri.Namespace},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.client, cd, func() error {
		cd.Spec.Chart = ri.Spec.Chart
		cd.Spec.Deploy = &compositiondefinitionsv1alpha1.DeploymentTarget{
			TargetRef: &compositiondefinitionsv1alpha1.TargetReference{Name: ri.Spec.TargetRef.Name},
		}
		return controllerutil.SetControllerReference(ri, cd, r.scheme)
	}); err != nil {
		return r.fail(ctx, ri, fmt.Sprintf("reconciling CompositionDefinition: %v", err))
	}

	// Mirror the target's reachability (best-effort — a missing target still lets the CD reconcile
	// and surface its own error). The KubernetesTarget is namespaced and resolved in the
	// RemoteInstall's own namespace.
	kt := &compositiondefinitionsv1alpha1.KubernetesTarget{}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: ri.Namespace, Name: ri.Spec.TargetRef.Name}, kt); err == nil {
		ri.Status.TargetConnection = kt.Status.ConnectionStatus
	}

	// Roll the CompositionDefinition's readiness up into a coarse phase.
	ri.Status.CompositionDefinition = cd.Namespace + "/" + cd.Name
	ready := cd.Status.GetCondition(rtv1.TypeReady)
	switch ready.Status {
	case metav1.ConditionTrue:
		// The CompositionDefinition is Ready: the generated CRD + cdc are on the spoke. Apply the
		// composition instance there from spec.values to complete the install.
		if err := r.applyInstance(ctx, ri, cd); err != nil {
			return r.fail(ctx, ri, fmt.Sprintf("applying composition instance on target: %v", err))
		}
		ri.Status.Phase = phaseReady
		ri.Status.SetConditions(rtv1.Available())
	case metav1.ConditionFalse:
		ri.Status.Phase = phaseInstalling
		ri.Status.SetConditions(rtv1.Creating().WithMessage(string(ready.Message)))
	default:
		ri.Status.Phase = phasePending
		ri.Status.SetConditions(rtv1.Creating())
	}
	if err := r.client.Status().Update(ctx, ri); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: requeueInterval}, nil
}

// applyInstance creates or updates the composition instance on the spoke from spec.values. The
// generated GVK comes from the CompositionDefinition status (apiVersion/kind/resource, recorded once
// the CRD is generated); the instance is named/namespaced like the RemoteInstall. Cleanup is handled
// by the CompositionDefinition's own Delete (the RemoteInstall owns the CD), which removes the
// generated CRD and cascades its instances — so no finalizer is needed here.
func (r *Reconciler) applyInstance(ctx context.Context, ri *compositiondefinitionsv1alpha1.RemoteInstall, cd *compositiondefinitionsv1alpha1.CompositionDefinition) error {
	if cd.Status.ApiVersion == "" || cd.Status.Kind == "" || cd.Status.Resource == "" {
		return fmt.Errorf("CompositionDefinition status has no generated GVK yet")
	}
	gv, err := schema.ParseGroupVersion(cd.Status.ApiVersion)
	if err != nil {
		return fmt.Errorf("parsing generated apiVersion %q: %w", cd.Status.ApiVersion, err)
	}
	gvr := gv.WithResource(cd.Status.Resource)

	var spec map[string]interface{}
	if ri.Spec.Values != nil && len(ri.Spec.Values.Raw) > 0 {
		if err := json.Unmarshal(ri.Spec.Values.Raw, &spec); err != nil {
			return fmt.Errorf("decoding spec.values: %w", err)
		}
	}

	// Build clients for the spoke (same path the CompositionDefinition reconcile uses). The
	// KubernetesTarget is namespaced and resolved in the RemoteInstall's own namespace.
	dt := &compositiondefinitionsv1alpha1.DeploymentTarget{
		TargetRef: &compositiondefinitionsv1alpha1.TargetReference{Name: ri.Spec.TargetRef.Name},
	}
	clients, err := clusterkube.Remote(ctx, r.client, ri.Namespace, dt)
	if err != nil {
		return fmt.Errorf("building target clients: %w", err)
	}
	res := clients.Dynamic.Resource(gvr).Namespace(ri.Namespace)

	live, err := res.Get(ctx, ri.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		inst := &unstructured.Unstructured{}
		inst.SetAPIVersion(cd.Status.ApiVersion)
		inst.SetKind(cd.Status.Kind)
		inst.SetNamespace(ri.Namespace)
		inst.SetName(ri.Name)
		if spec != nil {
			if err := unstructured.SetNestedMap(inst.Object, spec, "spec"); err != nil {
				return fmt.Errorf("setting instance spec: %w", err)
			}
		}
		if _, err := res.Create(ctx, inst, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating composition instance: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("reading composition instance: %w", err)
	}

	// Update the desired spec, preserving anything the spoke controller set elsewhere.
	if spec != nil {
		if err := unstructured.SetNestedMap(live.Object, spec, "spec"); err != nil {
			return fmt.Errorf("updating instance spec: %w", err)
		}
		if _, err := res.Update(ctx, live, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating composition instance: %w", err)
		}
	}
	return nil
}

func (r *Reconciler) fail(ctx context.Context, ri *compositiondefinitionsv1alpha1.RemoteInstall, msg string) (reconcile.Result, error) {
	ri.Status.Phase = phaseFailed
	ri.Status.SetConditions(rtv1.Unavailable().WithMessage(msg))
	if err := r.client.Status().Update(ctx, ri); err != nil {
		return reconcile.Result{}, err
	}
	r.log.Debug("remoteinstall failed", "name", ri.Name, "reason", msg)
	return reconcile.Result{RequeueAfter: requeueInterval}, nil
}
