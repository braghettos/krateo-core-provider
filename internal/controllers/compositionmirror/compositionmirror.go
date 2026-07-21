// Package compositionmirror reflects hub Composition instances of a remote-targeted
// CompositionDefinition onto its spoke, and reads the spoke's status back. It is the hub-side half of
// the "remote composition mirror" model (docs/design/remote-composition-mirror.md): the desired
// Composition is authored on the hub; core-provider mirrors its spec down to the spoke, where the
// projected composition-dynamic-controller renders the release, and mirrors the spoke status up.
//
// v1 is resync-driven. The controller reconciles a remote CompositionDefinition — whose generated
// Kind is created at runtime, so a dynamic per-Kind watch is deferred (design §7) — and, on each
// reconcile and every resync, reconciles the *set* of hub Composition instances of that Kind (in the
// CompositionDefinition's namespace) against the spoke:
//   - down: create-or-update each spoke instance's spec from its hub counterpart (hub wins on drift),
//   - back: copy the spoke instance's status onto the hub instance (best-effort),
//   - gc:   delete spoke instances this reflector created that no longer have a hub counterpart.
//
// Set-difference GC over a management label (rather than a per-instance finalizer) gives
// cross-cluster teardown without a dynamic watch; the finalizer/watch upgrade is a fast-follow.
package compositionmirror

import (
	"context"
	"fmt"
	"time"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	"github.com/krateoplatformops/core-provider/internal/tools/clusterkube"
	"github.com/krateoplatformops/provider-runtime/pkg/controller"
	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// managedByLabel marks spoke Composition instances this reflector created, so garbage collection
	// only ever deletes mirrors it owns — never instances authored directly on the spoke, or created
	// by the RemoteInstall migration shim.
	managedByLabel = "compositionmirror.krateo.io/managed-by"
	managedByValue = "core-provider"

	// requeueInterval is the steady-state resync: because there is no dynamic watch on the generated
	// hub Kind (v1), drift on a hub Composition is healed at most this long after it happens.
	requeueInterval = 30 * time.Second
	// requeuePending is the shorter backoff used while the CompositionDefinition has not yet recorded
	// its generated GVK (the CRD is still being generated).
	requeuePending = 10 * time.Second
	// reconcileTimeout bounds the spoke-reaching work of a single reconcile so a slow or unreachable
	// target cannot pin a worker indefinitely and starve reflection for other CompositionDefinitions.
	reconcileTimeout = 2 * time.Minute
)

// Options configures the compositionmirror reconciler.
type Options struct {
	ControllerOptions controller.Options
}

// Setup wires the reflector into the manager. It watches CompositionDefinitions (a statically-typed
// Kind); the generated composition Kinds are reconciled by enumeration inside Reconcile.
func Setup(mgr ctrl.Manager, o Options) error {
	r := &Reconciler{
		hub:        mgr.GetClient(),
		hubDynamic: dynamic.NewForConfigOrDie(mgr.GetConfig()),
		log:        o.ControllerOptions.Logger.WithValues("controller", "compositionmirror"),
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("compositionmirror").
		WithOptions(o.ControllerOptions.ForControllerRuntime()).
		For(&compositiondefinitionsv1alpha1.CompositionDefinition{}).
		Complete(r)
}

// Reconciler mirrors the hub Composition instances of a remote CompositionDefinition onto its spoke.
type Reconciler struct {
	hub        client.Client
	hubDynamic dynamic.Interface
	log        logging.Logger
}

// Reconcile enumerates and reflects the hub Composition instances of one remote CompositionDefinition.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	cd := &compositiondefinitionsv1alpha1.CompositionDefinition{}
	if err := r.hub.Get(ctx, req.NamespacedName, cd); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Only remote CompositionDefinitions have a hub-desired / spoke-realized split. A local CD's
	// Composition instances are rendered in place by the local cdc — there is nothing to mirror.
	if !clusterkube.IsRemote(cd.Spec.Deploy) {
		return reconcile.Result{}, nil
	}

	// The generated GVK is recorded on the CD status once its CRD exists (on both hub and spoke, see
	// increment 1). Until then there is nothing to author against — retry shortly.
	if cd.Status.ApiVersion == "" || cd.Status.Kind == "" || cd.Status.Resource == "" {
		return reconcile.Result{RequeueAfter: requeuePending}, nil
	}
	gv, err := schema.ParseGroupVersion(cd.Status.ApiVersion)
	if err != nil {
		r.log.Debug("compositionmirror: invalid generated apiVersion", "cd", req.String(), "apiVersion", cd.Status.ApiVersion, "error", err)
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}
	gvr := gv.WithResource(cd.Status.Resource)

	// Bound all spoke-reaching work: an unreachable target fails this reconcile instead of blocking a
	// worker until the client's own (possibly absent) timeouts fire — isolating one bad spoke from the
	// rest. Spoke failures are returned as errors so controller-runtime applies rate-limited backoff
	// (a persistently down spoke stops being retried at the flat resync interval and is surfaced),
	// while the happy path keeps the fixed resync cadence that drives drift healing.
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()

	// The KubernetesTarget is namespaced and resolved in the CompositionDefinition's own namespace.
	spoke, err := clusterkube.Remote(ctx, r.hub, cd.Namespace, cd.Spec.Deploy)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("compositionmirror: building spoke clients for %s: %w", req, err)
	}

	if err := reflectInstances(ctx, reflectParams{
		hub:        r.hubDynamic,
		spoke:      spoke.Dynamic,
		gvr:        gvr,
		apiVersion: cd.Status.ApiVersion,
		kind:       cd.Status.Kind,
		namespace:  cd.Namespace,
		log:        r.log,
	}); err != nil {
		return reconcile.Result{}, fmt.Errorf("compositionmirror: reflecting %s: %w", req, err)
	}

	return reconcile.Result{RequeueAfter: requeueInterval}, nil
}

// reflectParams carries everything reflectInstances needs. Hub and spoke are passed as dynamic
// clients so the engine is unit-testable with fakes, independent of controller wiring.
type reflectParams struct {
	hub        dynamic.Interface
	spoke      dynamic.Interface
	gvr        schema.GroupVersionResource
	apiVersion string
	kind       string
	namespace  string
	log        logging.Logger
}

// reflectInstances mirrors the hub Composition instances of one Kind (in a single namespace) onto the
// spoke and reads their status back, then garbage-collects spoke mirrors with no hub counterpart.
func reflectInstances(ctx context.Context, p reflectParams) error {
	hubRes := p.hub.Resource(p.gvr).Namespace(p.namespace)
	spokeRes := p.spoke.Resource(p.gvr).Namespace(p.namespace)

	hubList, err := hubRes.List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing hub compositions: %w", err)
	}

	desired := make(map[string]struct{}, len(hubList.Items))
	for i := range hubList.Items {
		hubInst := &hubList.Items[i]
		desired[hubInst.GetName()] = struct{}{}

		if err := mirrorDown(ctx, spokeRes, hubInst, p.apiVersion, p.kind, p.namespace); err != nil {
			return fmt.Errorf("mirroring %s/%s to spoke: %w", p.namespace, hubInst.GetName(), err)
		}

		if err := mirrorStatusUp(ctx, hubRes, spokeRes, hubInst.GetName()); err != nil {
			// Status readback is best-effort: a spoke instance not yet rendered has no status, and a
			// composition CRD without a status subresource cannot accept one. Neither is fatal to the
			// desired-state mirror, which is the reflector's primary job.
			p.log.Debug("compositionmirror: status readback skipped", "name", hubInst.GetName(), "error", err)
		}
	}

	return garbageCollect(ctx, spokeRes, desired, p.log)
}

// mirrorDown creates-or-updates the spoke Composition from its hub counterpart. The hub is the source
// of truth for spec (hub wins on drift); the mirror is stamped with the management label so GC can
// recognise it.
func mirrorDown(ctx context.Context, spokeRes dynamic.ResourceInterface, hubInst *unstructured.Unstructured, apiVersion, kind, namespace string) error {
	spec, _, err := unstructured.NestedMap(hubInst.Object, "spec")
	if err != nil {
		return fmt.Errorf("reading hub spec: %w", err)
	}

	live, err := spokeRes.Get(ctx, hubInst.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		mirror := &unstructured.Unstructured{}
		mirror.SetAPIVersion(apiVersion)
		mirror.SetKind(kind)
		mirror.SetNamespace(namespace)
		mirror.SetName(hubInst.GetName())
		mirror.SetLabels(map[string]string{managedByLabel: managedByValue})
		if spec != nil {
			if err := unstructured.SetNestedMap(mirror.Object, spec, "spec"); err != nil {
				return fmt.Errorf("setting mirror spec: %w", err)
			}
		}
		_, err := spokeRes.Create(ctx, mirror, metav1.CreateOptions{})
		return err
	} else if err != nil {
		return fmt.Errorf("reading spoke mirror: %w", err)
	}

	// Ensure the management label is present, then push the desired spec (hub wins on drift).
	labels := live.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[managedByLabel] = managedByValue
	live.SetLabels(labels)
	if spec != nil {
		if err := unstructured.SetNestedMap(live.Object, spec, "spec"); err != nil {
			return fmt.Errorf("updating mirror spec: %w", err)
		}
	} else {
		unstructured.RemoveNestedField(live.Object, "spec")
	}
	_, err = spokeRes.Update(ctx, live, metav1.UpdateOptions{})
	return err
}

// mirrorStatusUp copies the spoke Composition's status onto the hub Composition. The hub instance is
// re-read so the status write carries a current resourceVersion.
func mirrorStatusUp(ctx context.Context, hubRes, spokeRes dynamic.ResourceInterface, name string) error {
	live, err := spokeRes.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading spoke status: %w", err)
	}
	status, found, err := unstructured.NestedMap(live.Object, "status")
	if err != nil {
		return fmt.Errorf("reading spoke status field: %w", err)
	}
	if !found {
		return nil // nothing rendered on the spoke yet
	}

	cur, err := hubRes.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("re-reading hub composition: %w", err)
	}
	if err := unstructured.SetNestedMap(cur.Object, status, "status"); err != nil {
		return fmt.Errorf("setting hub status: %w", err)
	}
	if _, err := hubRes.UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("writing hub status: %w", err)
	}
	return nil
}

// garbageCollect deletes spoke mirrors this reflector owns (management label) that are no longer
// present on the hub. The label is re-checked client-side so the set-difference is correct even if
// the API server does not apply the label selector.
func garbageCollect(ctx context.Context, spokeRes dynamic.ResourceInterface, desired map[string]struct{}, log logging.Logger) error {
	managed, err := spokeRes.List(ctx, metav1.ListOptions{LabelSelector: managedByLabel + "=" + managedByValue})
	if err != nil {
		return fmt.Errorf("listing managed spoke mirrors: %w", err)
	}
	for i := range managed.Items {
		item := &managed.Items[i]
		if item.GetLabels()[managedByLabel] != managedByValue {
			continue // defensive: only ever delete mirrors we own
		}
		name := item.GetName()
		if _, keep := desired[name]; keep {
			continue
		}
		if err := spokeRes.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting orphaned spoke mirror %q: %w", name, err)
		}
		log.Debug("compositionmirror: deleted orphaned spoke mirror", "name", name)
	}
	return nil
}
