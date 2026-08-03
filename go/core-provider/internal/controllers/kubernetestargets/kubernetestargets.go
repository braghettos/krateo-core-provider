// Package kubernetestargets reconciles KubernetesTarget objects into a first-class, status-bearing
// registration of a remote spoke (Phase 2). It periodically probes each target's API and records
// reachability (Healthy/Down), the reported Kubernetes version, and the kubeconfig Secret
// resourceVersion the probe used — turning the passive reference into an observable spoke.
package kubernetestargets

import (
	"context"
	"fmt"
	"time"

	compositiondefinitionsv1alpha1 "github.com/krateo-platformops/core-provider/apis/compositiondefinitions/v1alpha1"
	"github.com/krateo-platformops/core-provider/internal/tools/clusterkube"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/krateo-platformops/provider-runtime/pkg/controller"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	connHealthy = "Healthy"
	connDown    = "Down"

	// defaultPollInterval is how often a target is re-probed when its status was last observed.
	defaultPollInterval = 2 * time.Minute
	// downRetryInterval re-probes an unreachable target more eagerly so recovery surfaces quickly.
	downRetryInterval = 30 * time.Second
)

// Options configures the KubernetesTarget reachability reconciler.
type Options struct {
	ControllerOptions controller.Options
	// PollInterval overrides how often a reachable target is re-probed (default 2m).
	PollInterval time.Duration
}

// Setup wires the reconciler into the manager.
func Setup(mgr ctrl.Manager, o Options) error {
	poll := o.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	r := &Reconciler{
		client: mgr.GetClient(),
		log:    o.ControllerOptions.Logger.WithValues("controller", "kubernetestarget"),
		poll:   poll,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&compositiondefinitionsv1alpha1.KubernetesTarget{}).
		Named("kubernetestarget").
		Complete(r)
}

// Reconciler probes a KubernetesTarget's reachability and records it in status.
type Reconciler struct {
	client client.Client
	log    logging.Logger
	poll   time.Duration
}

// Reconcile probes the target cluster's API and records reachability, version and the credential
// resourceVersion. It always requeues so status tracks the target over time (poll cadence for
// healthy targets, a shorter retry for down ones).
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	kt := &compositiondefinitionsv1alpha1.KubernetesTarget{}
	if err := r.client.Get(ctx, req.NamespacedName, kt); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Probe: build remote clients from the target's kubeconfig Secret and query its discovery API.
	// The KubernetesTarget is namespaced, so it resolves in its own namespace.
	dt := &compositiondefinitionsv1alpha1.DeploymentTarget{
		TargetRef: &compositiondefinitionsv1alpha1.TargetReference{Name: kt.Name},
	}
	clients, err := clusterkube.Remote(ctx, r.client, kt.Namespace, dt)
	if err != nil {
		return r.markDown(ctx, kt, fmt.Sprintf("cannot build target clients: %v", err))
	}
	ver, err := clients.Clientset.Discovery().ServerVersion()
	if err != nil {
		return r.markDown(ctx, kt, fmt.Sprintf("target discovery failed: %v", err))
	}

	now := metav1.Now()
	kt.Status.ConnectionStatus = connHealthy
	kt.Status.Version = ver.GitVersion
	kt.Status.KubeconfigSecretResourceVersion = clients.SecretResourceVersion
	kt.Status.LastProbeTime = &now
	kt.Status.SetConditions(rtv1.Available())
	if err := r.client.Status().Update(ctx, kt); err != nil {
		return reconcile.Result{}, err
	}
	r.log.Debug("target healthy", "name", kt.Name, "version", ver.GitVersion)
	return reconcile.Result{RequeueAfter: r.poll}, nil
}

func (r *Reconciler) markDown(ctx context.Context, kt *compositiondefinitionsv1alpha1.KubernetesTarget, msg string) (reconcile.Result, error) {
	now := metav1.Now()
	kt.Status.ConnectionStatus = connDown
	kt.Status.LastProbeTime = &now
	kt.Status.SetConditions(rtv1.Unavailable().WithMessage(msg))
	if err := r.client.Status().Update(ctx, kt); err != nil {
		return reconcile.Result{}, err
	}
	r.log.Debug("target down", "name", kt.Name, "reason", msg)
	return reconcile.Result{RequeueAfter: downRetryInterval}, nil
}
