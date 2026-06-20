package compositiondefinitions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/krateoplatformops/plumbing/kubeutil/event"
	"github.com/krateoplatformops/plumbing/kubeutil/eventrecorder"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	"github.com/krateoplatformops/core-provider/internal/controllers/compositiondefinitions/helpers/getters"
	"github.com/krateoplatformops/core-provider/internal/controllers/compositiondefinitions/helpers/status"
	"github.com/krateoplatformops/core-provider/internal/tools/chart"
	"github.com/krateoplatformops/core-provider/internal/tools/chart/chartfs"
	"github.com/krateoplatformops/core-provider/internal/tools/clusterkube"
	contexttools "github.com/krateoplatformops/core-provider/internal/tools/context"
	crdclient "github.com/krateoplatformops/core-provider/internal/tools/crd"
	crdutils "github.com/krateoplatformops/core-provider/internal/tools/crd/generation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/krateoplatformops/core-provider/internal/tools/deploy"
	"github.com/krateoplatformops/core-provider/internal/tools/kube"
	pluralizerlib "github.com/krateoplatformops/core-provider/internal/tools/pluralizer"
	"github.com/krateoplatformops/core-provider/internal/tools/policy"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
	"github.com/krateoplatformops/provider-runtime/pkg/controller"

	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	"github.com/krateoplatformops/provider-runtime/pkg/meta"
	"github.com/krateoplatformops/provider-runtime/pkg/ratelimiter"
	"github.com/krateoplatformops/provider-runtime/pkg/reconciler"
	"github.com/krateoplatformops/provider-runtime/pkg/resource"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	record "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	errNotCR         = "managed resource is not a Definition custom resource"
	reconcileTimeout = 4 * time.Minute
)

var (
	CDCtemplateDeploymentPath       = filepath.Join(os.TempDir(), "assets/cdc-deployment/deployment.yaml")
	CDCtemplateConfigmapPath        = filepath.Join(os.TempDir(), "assets/cdc-configmap/configmap.yaml")
	CDCrbacConfigFolder             = filepath.Join(os.TempDir(), "assets/cdc-rbac/")
	JSONSchemaTemplateConfigmapPath = filepath.Join(os.TempDir(), "assets/json-schema-configmap/configmap.yaml")
	ServiceTemplatePath             = filepath.Join(os.TempDir(), "assets/cdc-service/service.yaml")
)

type Options struct {
	ControllerOptions controller.Options
	// Metrics records reconcile telemetry for the CompositionDefinition controller.
	Metrics    reconciler.MetricsRecorder
	Pluralizer pluralizerlib.PluralizerInterface
}

func Setup(mgr ctrl.Manager, o Options) error {
	name := reconciler.ControllerName(compositiondefinitionsv1alpha1.CompositionDefinitionGroupKind)

	l := o.ControllerOptions.Logger.WithValues("controller", name)

	throttledRecorder, err := eventrecorder.CreateWithThrottle(context.Background(), mgr.GetConfig(), name, nil)
	if err != nil {
		return fmt.Errorf("error creating event recorder: %w", err)
	}

	recorder, err := eventrecorder.Create(context.Background(), mgr.GetConfig(), name, nil)
	if err != nil {
		return fmt.Errorf("error creating event recorder: %w", err)
	}

	cli := mgr.GetClient()

	// Cleanup: Remove obsolete label for backward compatibility on startup
	// This handles CompositionDefinitions created before the removal of the still-exist-compositions-finalizer
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cleanupCancel()
	if err := cleanupObsoleteFinalizerLabels(cleanupCtx, cli, l); err != nil {
		l.Debug("Failed to cleanup obsolete finalizer labels on startup", "error", err)
	}

	// core-provider hosts no admission webhooks: generated CRDs use None conversion, and
	// the composition-version label is stamped by a MutatingAdmissionPolicy that must exist
	// in every cluster a composition CRD lives in (requires Kubernetes >= 1.36). On the
	// management cluster the chart ships it; for remote targets core-provider projects it
	// into the target during bootstrap (see external.ensureCompositionVersionPolicy).

	r := reconciler.NewReconciler(mgr,
		resource.ManagedKind(compositiondefinitionsv1alpha1.CompositionDefinitionGroupVersionKind),
		reconciler.WithExternalConnecter(&connector{
			client:     kubernetes.NewForConfigOrDie(mgr.GetConfig()),
			dynamic:    dynamic.NewForConfigOrDie(mgr.GetConfig()),
			kube:       cli,
			log:        l,
			recorder:   recorder,
			pluralizer: o.Pluralizer,
		}),
		reconciler.WithTimeout(reconcileTimeout),
		reconciler.WithPollInterval(o.ControllerOptions.PollInterval),
		reconciler.WithLogger(l),
		reconciler.WithMetrics(o.Metrics),
		reconciler.WithRecorder(event.NewAPIRecorder(recorder)),
		reconciler.WithThrottledRecorder(event.NewAPIRecorder(throttledRecorder)),
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ControllerOptions.ForControllerRuntime()).
		For(&compositiondefinitionsv1alpha1.CompositionDefinition{}).
		// Re-reconcile a CompositionDefinition when a Secret it references changes (its
		// chart credentials, or a kubeconfig Secret behind its KubernetesTarget), so
		// credentials rotated out-of-band (e.g. by External Secrets Operator) are picked
		// up promptly instead of waiting for the next poll.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(enqueueForReferencedSecret(cli))).
		// Re-reconcile CompositionDefinitions when the KubernetesTarget they reference
		// changes (e.g. its kubeconfigRef is repointed).
		Watches(&compositiondefinitionsv1alpha1.KubernetesTarget{}, handler.EnqueueRequestsFromMapFunc(enqueueForKubernetesTarget(cli))).
		Complete(ratelimiter.New(name, r, o.ControllerOptions.GlobalRateLimiter))
}

// enqueueForReferencedSecret maps a Secret event to reconcile requests for every
// CompositionDefinition that references it - directly as chart credentials, or
// transitively via a KubernetesTarget whose kubeconfigRef points at the Secret.
func enqueueForReferencedSecret(cli client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}

		// Names of KubernetesTargets whose kubeconfig lives in this Secret.
		targetNames := map[string]bool{}
		var targets compositiondefinitionsv1alpha1.KubernetesTargetList
		if err := cli.List(ctx, &targets); err == nil {
			for i := range targets.Items {
				ref := targets.Items[i].Spec.KubeconfigRef
				if ref.Namespace == secret.Namespace && ref.Name == secret.Name {
					targetNames[targets.Items[i].Name] = true
				}
			}
		}

		var list compositiondefinitionsv1alpha1.CompositionDefinitionList
		if err := cli.List(ctx, &list); err != nil {
			return nil
		}

		var reqs []reconcile.Request
		for i := range list.Items {
			cd := &list.Items[i]
			if compositionReferencesChartSecret(cd, secret.Namespace, secret.Name) ||
				compositionReferencesTargetIn(cd, targetNames) {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cd)})
			}
		}
		return reqs
	}
}

// enqueueForKubernetesTarget maps a KubernetesTarget event to reconcile requests for
// every CompositionDefinition referencing it.
func enqueueForKubernetesTarget(cli client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		kt, ok := obj.(*compositiondefinitionsv1alpha1.KubernetesTarget)
		if !ok {
			return nil
		}

		var list compositiondefinitionsv1alpha1.CompositionDefinitionList
		if err := cli.List(ctx, &list); err != nil {
			return nil
		}

		var reqs []reconcile.Request
		for i := range list.Items {
			cd := &list.Items[i]
			if compositionReferencesTargetIn(cd, map[string]bool{kt.Name: true}) {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cd)})
			}
		}
		return reqs
	}
}

// compositionReferencesChartSecret reports whether cd uses the Secret ns/name as its
// chart repository credentials.
func compositionReferencesChartSecret(cd *compositiondefinitionsv1alpha1.CompositionDefinition, ns, name string) bool {
	c := cd.Spec.Chart
	if c == nil || c.Credentials == nil {
		return false
	}
	return c.Credentials.PasswordRef.Namespace == ns && c.Credentials.PasswordRef.Name == name
}

// compositionReferencesTargetIn reports whether cd's deploy.targetRef names one of the
// given KubernetesTargets.
func compositionReferencesTargetIn(cd *compositiondefinitionsv1alpha1.CompositionDefinition, targetNames map[string]bool) bool {
	d := cd.Spec.Deploy
	if d == nil || d.TargetRef == nil {
		return false
	}
	return targetNames[d.TargetRef.Name]
}

// cleanupObsoleteFinalizerLabels removes the obsolete "composition.krateo.io/still-exist-compositions-finalizer" label
// from all CompositionDefinitions for backward compatibility. This handles resources created before the label was removed.
func cleanupObsoleteFinalizerLabels(ctx context.Context, kube client.Client, log logging.Logger) error {
	const obsoleteLabel = "composition.krateo.io/still-exist-compositions-finalizer"

	list := &compositiondefinitionsv1alpha1.CompositionDefinitionList{}
	if err := kube.List(ctx, list); err != nil {
		return fmt.Errorf("error listing CompositionDefinitions: %w", err)
	}

	if len(list.Items) == 0 {
		log.Debug("No CompositionDefinitions found for cleanup")
		return nil
	}

	cleaned := 0
	for i := range list.Items {
		cr := &list.Items[i]
		if cr.Labels != nil {
			if _, exists := cr.Labels[obsoleteLabel]; exists {
				delete(cr.Labels, obsoleteLabel)
				if err := kube.Update(ctx, cr); err != nil {
					log.Debug("Failed to remove obsolete finalizer label", "name", cr.Name, "namespace", cr.Namespace, "error", err)
					continue
				}
				cleaned++
				log.Debug("Removed obsolete finalizer label", "name", cr.Name, "namespace", cr.Namespace)
			}
		}
	}

	if cleaned > 0 {
		log.Info("Cleanup completed", "removed_labels", cleaned)
	}
	return nil
}

type connector struct {
	dynamic    dynamic.Interface
	client     kubernetes.Interface
	kube       client.Client
	log        logging.Logger
	recorder   record.EventRecorder
	pluralizer pluralizerlib.PluralizerInterface
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (reconciler.ExternalClient, error) {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return nil, fmt.Errorf(errNotCR)
	}

	log := c.log.WithValues("name", cr.Name, "namespace", cr.Namespace)

	ext := &external{
		mgmt:       c.kube,
		kube:       c.kube,
		dynamic:    c.dynamic,
		client:     c.client,
		log:        log,
		rec:        c.recorder,
		pluralizer: c.pluralizer,
	}

	// When the CompositionDefinition targets a remote cluster, the generated CRD, its
	// RBAC and the composition-dynamic-controller are deployed there. The
	// CompositionDefinition resource and its referenced secrets stay in the management
	// cluster, so mgmt keeps pointing at the local cluster.
	if clusterkube.IsRemote(cr.Spec.Deploy) {
		tc, err := clusterkube.Remote(ctx, c.kube, cr.Spec.Deploy)
		if err != nil {
			return nil, err
		}
		ext.kube = tc.Kube
		ext.dynamic = tc.Dynamic
		ext.client = tc.Clientset
		ext.remote = true
		ext.secretResourceVersion = tc.SecretResourceVersion
		log.Debug("Deploying to remote target cluster", "host", tc.Config.Host)
	}

	return ext, nil
}

type external struct {
	// mgmt is the management-cluster client: it holds the CompositionDefinition
	// resource, the chart/credentials secrets, and is where status is persisted.
	mgmt client.Client
	// kube, dynamic and client target the cluster where the generated CRD, RBAC and the
	// composition-dynamic-controller are deployed (local == mgmt, or a remote target).
	dynamic    dynamic.Interface
	kube       client.Client
	client     kubernetes.Interface
	log        logging.Logger
	rec        record.EventRecorder
	pluralizer pluralizerlib.PluralizerInterface

	// remote is true when the target is a remote cluster; secretResourceVersion is the
	// resourceVersion of the kubeconfig Secret used to reach it.
	remote                bool
	secretResourceVersion string
}

// setTargetStatus records where the controller is deployed and whether that cluster is
// reachable, by probing the target cluster's discovery endpoint.
func (e *external) setTargetStatus(cr *compositiondefinitionsv1alpha1.CompositionDefinition) {
	mode := compositiondefinitionsv1alpha1.DeploymentModeLocal
	if clusterkube.IsRemote(cr.Spec.Deploy) {
		mode = compositiondefinitionsv1alpha1.DeploymentModeRemote
	}

	ts := &compositiondefinitionsv1alpha1.TargetStatus{Mode: string(mode)}
	if v, err := e.client.Discovery().ServerVersion(); err == nil {
		ts.ConnectionStatus = "Healthy"
		ts.Version = v.GitVersion
	} else {
		ts.ConnectionStatus = "Down"
	}
	if e.remote {
		ts.KubeconfigSecretResourceVersion = e.secretResourceVersion
	}

	cr.Status.Target = ts
}

// ensureCompositionVersionPolicy projects the cluster-wide composition-version
// MutatingAdmissionPolicy into a remote target. The label it stamps is what per-version
// listing/migration and safe deletion rely on; for local targets the management chart
// already ships the policy, so this only acts on remote targets. Create-if-absent, so it
// never fights a chart- or operator-managed policy already present in the target.
func (e *external) ensureCompositionVersionPolicy(ctx context.Context) error {
	if !e.remote {
		return nil
	}
	if err := policy.EnsureCompositionVersionPolicy(ctx, e.kube); err != nil {
		return fmt.Errorf("error projecting composition-version policy into target: %w", err)
	}
	return nil
}

// versionReferencedByAnotherDefinition reports whether any CompositionDefinition OTHER than
// (selfName/selfNamespace) currently targets the given generated GVK (group+kind+version). It
// is the reference count behind safe version retirement: a per-(CRD,version) dynamic controller
// is shared by every definition on that version, so it may only be torn down once no other
// definition still needs it.
func (e *external) versionReferencedByAnotherDefinition(ctx context.Context, gvk schema.GroupVersionKind, selfName, selfNamespace string) (bool, error) {
	cds, err := getters.GetCompositionDefinitionsWithVersion(ctx, e.mgmt, gvk)
	if err != nil {
		return false, err
	}
	for i := range cds {
		if cds[i].Name == selfName && cds[i].Namespace == selfNamespace {
			continue
		}
		return true, nil
	}
	return false, nil
}

// encodeStatusDataTemplate serializes the CompositionDefinition's statusDataTemplate to the
// engine's JSON wire format ([{forPath,expression}]) for delivery to the CDC via its
// ConfigMap (COMPOSITION_CONTROLLER_STATUS_DATA_TEMPLATE). Empty when nothing is declared.
func encodeStatusDataTemplate(cr *compositiondefinitionsv1alpha1.CompositionDefinition) string {
	if len(cr.Spec.StatusDataTemplate) == 0 {
		return ""
	}
	type wire struct {
		ForPath    string `json:"forPath"`
		Expression string `json:"expression"`
	}
	ws := make([]wire, 0, len(cr.Spec.StatusDataTemplate))
	for i := range cr.Spec.StatusDataTemplate {
		m := &cr.Spec.StatusDataTemplate[i]
		ws = append(ws, wire{ForPath: m.ForPath, Expression: m.Expression})
	}
	b, err := json.Marshal(ws)
	if err != nil {
		return ""
	}
	return string(b)
}

// encodeApiRefExtras serializes the CompositionDefinition's apiRef.extras (the inline,
// author-declared static map, snowplow spec.apiRef.extras) to a compact JSON object for
// delivery to the CDC via COMPOSITION_CONTROLLER_API_REF_EXTRAS. Empty when no apiRef or no
// extras are declared.
func encodeApiRefExtras(cr *compositiondefinitionsv1alpha1.CompositionDefinition) string {
	if cr.Spec.ApiRef == nil || cr.Spec.ApiRef.Extras == nil {
		return ""
	}
	// Extras is an apiextensionsv1.JSON; its Raw is already the JSON encoding.
	return string(cr.Spec.ApiRef.Extras.Raw)
}

// apiRefName / apiRefNamespace return the referenced RESTAction coordinates, or "" when no
// apiRef is declared (which disables ".api" resolution in the CDC).
func apiRefName(cr *compositiondefinitionsv1alpha1.CompositionDefinition) string {
	if cr.Spec.ApiRef == nil {
		return ""
	}
	return cr.Spec.ApiRef.Name
}

func apiRefNamespace(cr *compositiondefinitionsv1alpha1.CompositionDefinition) string {
	if cr.Spec.ApiRef == nil {
		return ""
	}
	return cr.Spec.ApiRef.Namespace
}

// statusFieldsFromSpec maps the CompositionDefinition's statusDataTemplate declarations to
// the generation package's decoupled StatusField list (used to validate and to inject the
// declared properties into the generated CRD's status schema).
func statusFieldsFromSpec(cr *compositiondefinitionsv1alpha1.CompositionDefinition) []crdutils.StatusField {
	fields := make([]crdutils.StatusField, 0, len(cr.Spec.StatusDataTemplate))
	for i := range cr.Spec.StatusDataTemplate {
		m := &cr.Spec.StatusDataTemplate[i]
		fields = append(fields, crdutils.StatusField{
			ForPath:               m.ForPath,
			Expression:            m.Expression,
			Type:                  m.Type,
			Schema:                m.Schema,
			PreserveUnknownFields: m.PreserveUnknownFields,
		})
	}
	return fields
}

func (e *external) Observe(ctx context.Context, mg resource.Managed) (reconciler.ExternalObservation, error) {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return reconciler.ExternalObservation{}, fmt.Errorf(errNotCR)
	}
	log := e.log.WithValues("operation", "observe")
	ctx = contexttools.CtxWithLogger(ctx, log)
	deleted := meta.WasDeleted(cr)

	log.Info("Observing CompositionDefinition")

	// Record where the controller is deployed and whether that cluster is reachable.
	e.setTargetStatus(cr)

	pkgInfo, dir, err := chart.ChartInfoFromSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error getting chart info: %w", err)
	}

	pkg, err := chartfs.ForSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}

	chartGVK, err := chartfs.GroupVersionKind(pkg)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}
	specSchemaBytes, err := chart.ChartJsonSchema(pkgInfo, dir)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error getting spec schema: %w", err)
	}

	gvr, err := e.pluralizer.GVKtoGVR(chartGVK)
	if err != nil {
		if deleted {
			if apierrors.IsNotFound(err) {
				log.Debug("Plural not found, CRD not found, external resource no longer exists", "gvk", chartGVK.String())
			} else {
				log.Debug("Unable to resolve GVR for deleted CompositionDefinition, treating external resource as gone", "gvk", chartGVK.String(), "err", err)
			}
			return reconciler.ExternalObservation{
				ResourceExists:   false,
				ResourceUpToDate: false,
			}, nil
		}

		if apierrors.IsNotFound(err) {
			gvr, err = crdutils.GetGVRFromGeneratedCRD(specSchemaBytes, chartGVK)
			if err != nil {
				return reconciler.ExternalObservation{}, fmt.Errorf("error getting GVR from generated CRD for GVR fallback: %w", err)
			}
		} else {
			return reconciler.ExternalObservation{}, fmt.Errorf("error converting GVK to GVR: %w - GVK: %s", err, chartGVK.String())
		}
	} else if deleted {
		log.Debug("CompositionDefinition was deleted, CRD still resolves; continuing observation", "gvr", gvr.String())
	}

	crd, err := crdclient.Get(ctx, e.kube, gvr.GroupResource())
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error getting CRD: %w", err)
	}
	if crd == nil {
		log.Debug("CRD not found", "gvr", gvr.String())
		cr.SetConditions(rtv1.Unavailable().
			WithMessage(fmt.Sprintf("crd for '%s' does not exists yet", gvr.String())))
		return reconciler.ExternalObservation{
			ResourceExists:   false,
			ResourceUpToDate: false,
		}, nil
	}

	existVersion, err := crdclient.Lookup(ctx, e.kube, gvr)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error looking up existing CRD version: %w", err)
	}
	if !existVersion {
		log.Debug("CRD version not found", "gvr", gvr.String())
		cr.SetConditions(rtv1.Unavailable().
			WithMessage(fmt.Sprintf("crd for '%s' does not exists yet", gvr.String())))
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	genCRD, err := crdutils.GenerateCRD(specSchemaBytes, chartGVK)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error generating CRD: %w", err)
	}

	statusFields := statusFieldsFromSpec(cr)
	if err := crdutils.ValidateStatusFields(statusFields); err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("invalid statusDataTemplate: %w", err)
	}
	if err := crdutils.InjectStatusFields(genCRD, statusFields); err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error injecting declared status fields: %w", err)
	}

	statusChanged, err := crdutils.StatusEqual(crd, genCRD)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error comparing CRD status: %w", err)
	}

	if !statusChanged {
		log.Debug("CRD status changed", "gvr", gvr.String())
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	// Certificate management is now handled by a separate CertificateReconciler
	// that runs independently on a periodic schedule.

	ul, err := getters.GetCompositions(ctx, e.dynamic, gvr)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error getting compositions: %w", err)
	}
	if len(ul.Items) > 0 {
		log.Debug("Compositions exist for this definition", "count", len(ul.Items))
	}

	log.Debug("Searching for Dynamic Controller", "gvr", gvr)

	opts := deploy.DeployOptions{
		RBACFolderPath:         CDCrbacConfigFolder,
		DiscoveryClient:        memory.NewMemCacheClient(e.client.Discovery()),
		KubeClient:             e.kube,
		Namespace:              cr.Namespace,
		GVR:                    gvr,
		Spec:                   cr.Spec.Chart.DeepCopy(),
		DeploymentTemplatePath: CDCtemplateDeploymentPath,
		ConfigmapTemplatePath:  CDCtemplateConfigmapPath,
		JsonSchemaTemplatePath: JSONSchemaTemplateConfigmapPath,
		JsonSchemaBytes:        specSchemaBytes,
		ServiceTemplatePath:    ServiceTemplatePath,
		DynClient:              e.dynamic,
		StatusDataTemplate:     encodeStatusDataTemplate(cr),
		ApiRefName:             apiRefName(cr),
		ApiRefNamespace:        apiRefNamespace(cr),
		ApiRefExtras:           encodeApiRefExtras(cr),
		DryRunServer:           true,
	}
	dig, err := deploy.Deploy(ctx, e.kube, opts)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error deploying dynamic controller in dry-run mode: %w", err)
	}

	if cr.Status.Digest != dig {
		log.Debug("Rendered resources digest changed", "status", cr.Status.Digest, "rendered", dig)
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	dig, err = deploy.Lookup(ctx, e.kube, opts)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error looking up deployed resources digest: %w", err)
	}
	if cr.Status.Digest != dig {
		log.Debug("Deployed resources digest changed", "status", cr.Status.Digest, "deployed", dig)
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	// Drive version migration to completion: if any composition still carries a previous
	// served version's label, report not-up-to-date so Update re-runs and re-stamps it.
	// The composition-version policy keys off the write endpoint, so a straggler can
	// survive a prior transition; without this the migration would be one-shot and the
	// straggler stays orphaned (the new label-scoped controller never selects it).
	observeOwner := getters.DefinitionRef{Name: cr.Name, Namespace: cr.Namespace}
	for _, vi := range cr.Status.Managed.VersionInfo {
		if vi.Version == gvr.Version {
			continue
		}
		// List THROUGH the current served endpoint, selecting by the old version LABEL
		// (the label, not the served apiVersion, identifies the owning controller) and scoped
		// to THIS definition — another definition's instances on this version are not ours.
		stale, err := getters.GetOwnedCompositionsByVersionLabel(ctx, e.dynamic, gvr, vi.Version, observeOwner)
		if err != nil {
			return reconciler.ExternalObservation{}, fmt.Errorf("error checking compositions on version %s: %w", vi.Version, err)
		}
		if len(stale.Items) > 0 {
			log.Debug("Compositions pending version migration", "fromVersion", vi.Version, "toVersion", gvr.Version, "count", len(stale.Items))
			return reconciler.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: false,
			}, nil
		}
	}

	if err := status.RefreshCompositionDefinitionStatus(cr, crd, gvr, chartGVK, pkg.PackageURL()); err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("error refreshing CompositionDefinition status: %w", err)
	}

	cr.SetConditions(rtv1.Available())

	return reconciler.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

func (e *external) Create(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return fmt.Errorf(errNotCR)
	}

	log := e.log.WithValues("operation", "create")
	ctx = contexttools.CtxWithLogger(ctx, log)

	log.Info("Creating CompositionDefinition")

	pkg, dir, err := chart.ChartInfoFromSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return err
	}

	gvk, err := chart.ChartGroupVersionKind(pkg, dir)
	if err != nil {
		return err
	}

	specSchemaBytes, err := chart.ChartJsonSchema(pkg, dir)
	if err != nil {
		return fmt.Errorf("error getting JSON schema: %w", err)
	}
	crd, err := crdutils.GenerateCRD(specSchemaBytes, gvk)
	if err != nil {
		return fmt.Errorf("error generating CRD: %w", err)
	}
	if crd == nil {
		return fmt.Errorf("error generating CRD: crd is nil")
	}
	if err := crdutils.InjectStatusFields(crd, statusFieldsFromSpec(cr)); err != nil {
		return fmt.Errorf("error injecting declared status fields: %w", err)
	}

	gvr, err := crdclient.ApplyOrUpdateCRD(ctx, e.kube, e.dynamic, crd)
	if err != nil {
		return fmt.Errorf("error applying or updating CRD: %w", err)
	}

	if err := e.ensureCompositionVersionPolicy(ctx); err != nil {
		return err
	}

	opts := deploy.DeployOptions{
		RBACFolderPath:         CDCrbacConfigFolder,
		DiscoveryClient:        memory.NewMemCacheClient(e.client.Discovery()),
		KubeClient:             e.kube,
		Namespace:              cr.Namespace,
		GVR:                    gvr,
		Spec:                   cr.Spec.Chart.DeepCopy(),
		DeploymentTemplatePath: CDCtemplateDeploymentPath,
		ConfigmapTemplatePath:  CDCtemplateConfigmapPath,
		JsonSchemaTemplatePath: JSONSchemaTemplateConfigmapPath,
		ServiceTemplatePath:    ServiceTemplatePath,
		JsonSchemaBytes:        specSchemaBytes,
		DynClient:              e.dynamic,
		StatusDataTemplate:     encodeStatusDataTemplate(cr),
		ApiRefName:             apiRefName(cr),
		ApiRefNamespace:        apiRefNamespace(cr),
		ApiRefExtras:           encodeApiRefExtras(cr),
	}

	dig, err := deploy.Deploy(ctx, e.kube, opts)
	if err != nil {
		return err
	}

	log.Debug("Dynamic Controller successfully deployed",
		"gvr", gvr.String(),
		"namespace", cr.Namespace,
	)

	cr.Status.Digest = dig

	return nil
}

func (e *external) Update(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return fmt.Errorf(errNotCR)
	}

	log := e.log.WithValues("operation", "update")
	ctx = contexttools.CtxWithLogger(ctx, log)

	log.Info("Updating CompositionDefinition")

	pkg, dir, err := chart.ChartInfoFromSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return fmt.Errorf("error getting chart info: %w", err)
	}
	pkgFS, err := chartfs.ForSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return err
	}

	gvk, err := chart.ChartGroupVersionKind(pkg, dir)
	if err != nil {
		return err
	}

	specSchemaBytes, err := chart.ChartJsonSchema(pkg, dir)
	if err != nil {
		return fmt.Errorf("error getting JSON schema: %w", err)
	}
	crd, err := crdutils.GenerateCRD(specSchemaBytes, gvk)
	if err != nil {
		return fmt.Errorf("error generating CRD: %w", err)
	}
	if crd == nil {
		return fmt.Errorf("error generating CRD: crd is nil")
	}
	if err := crdutils.InjectStatusFields(crd, statusFieldsFromSpec(cr)); err != nil {
		return fmt.Errorf("error injecting declared status fields: %w", err)
	}

	gvr, err := crdclient.ApplyOrUpdateCRD(ctx, e.kube, e.dynamic, crd)
	if err != nil {
		return fmt.Errorf("error applying or updating CRD: %w", err)
	}

	if err := e.ensureCompositionVersionPolicy(ctx); err != nil {
		return err
	}

	opts := deploy.DeployOptions{
		RBACFolderPath:         CDCrbacConfigFolder,
		DiscoveryClient:        memory.NewMemCacheClient(e.client.Discovery()),
		KubeClient:             e.kube,
		Namespace:              cr.Namespace,
		GVR:                    gvr,
		Spec:                   cr.Spec.Chart.DeepCopy(),
		DeploymentTemplatePath: CDCtemplateDeploymentPath,
		ConfigmapTemplatePath:  CDCtemplateConfigmapPath,
		JsonSchemaTemplatePath: JSONSchemaTemplateConfigmapPath,
		ServiceTemplatePath:    ServiceTemplatePath,
		JsonSchemaBytes:        specSchemaBytes,
		DynClient:              e.dynamic,
		StatusDataTemplate:     encodeStatusDataTemplate(cr),
		ApiRefName:             apiRefName(cr),
		ApiRefNamespace:        apiRefNamespace(cr),
		ApiRefExtras:           encodeApiRefExtras(cr),
	}

	dig, err := deploy.Deploy(ctx, e.kube, opts)
	if err != nil {
		return fmt.Errorf("error deploying dynamic controller: %w", err)
	}

	cr.Status.Digest = dig

	log.Debug("Dynamic Controller successfully updated",
		"gvr", gvr.String(),
		"namespace", cr.Namespace,
	)
	oldGVK := schema.FromAPIVersionAndKind(cr.Status.ApiVersion, cr.Status.Kind)
	oldGVR := oldGVK.GroupVersion().WithResource(cr.Status.Resource)
	// Undeploy olders versions of the CRD
	if oldGVK != gvk {
		for _, vi := range cr.Status.Managed.VersionInfo {
			if oldGVK.Kind == cr.Status.Managed.Kind && oldGVK.Version == vi.Version {
				// Reference-counted retirement: one CRD/Kind can be shared by multiple
				// CompositionDefinitions at different versions, and a per-(CRD,version)
				// controller is shared across them. Only retire this version's controller when
				// NO OTHER definition still targets it — otherwise we'd tear a controller out
				// from under another definition's instances.
				referenced, refErr := e.versionReferencedByAnotherDefinition(ctx, schema.GroupVersionKind{
					Group: oldGVK.Group, Kind: oldGVK.Kind, Version: vi.Version,
				}, cr.Name, cr.Namespace)
				if refErr != nil {
					return fmt.Errorf("error checking references for version %s: %w", vi.Version, refErr)
				}
				if referenced {
					log.Debug("Skipping controller retirement: version still referenced by another CompositionDefinition", "version", vi.Version)
					continue
				}
				err = deploy.Undeploy(ctx, e.kube, deploy.UndeployOptions{
					DiscoveryClient:        memory.NewMemCacheClient(e.client.Discovery()),
					RBACFolderPath:         CDCrbacConfigFolder,
					DeploymentTemplatePath: CDCtemplateDeploymentPath,
					ConfigmapTemplatePath:  CDCtemplateConfigmapPath,
					JsonSchemaTemplatePath: JSONSchemaTemplateConfigmapPath,
					ServiceTemplatePath:    ServiceTemplatePath,
					DynamicClient:          e.dynamic,
					Spec:                   (*compositiondefinitionsv1alpha1.ChartInfo)(vi.Chart),
					GVR:                    oldGVR,
					KubeClient:             e.kube,
					Namespace:              cr.Namespace,
					SkipCRD:                true,
				})
				if err != nil {
					return fmt.Errorf("error undeploying older version of dynamic controller: %w", err)
				}
				log.Debug("Undeployed older versions of dynamic controller", "gvr", oldGVR.String())
			}
		}
	}
	// Migrate any composition still carrying a previous served version's label onto the
	// current version. Driven by the managed served-versions list rather than a single
	// status.apiVersion comparison so it is idempotent and self-healing: stragglers from a
	// prior transition — or a composition (re)written through an old endpoint after status
	// advanced — are migrated on a later reconcile instead of being orphaned forever (the
	// new label-scoped controller never selects an old-labelled CR). UpdateCompositionsVersion
	// is a no-op when nothing carries the old label, so this is safe to run every transition.
	log.Debug("Migrating Compositions to current version", "gvr", gvr.String())
	owner := getters.DefinitionRef{Name: cr.Name, Namespace: cr.Namespace}
	for _, vi := range cr.Status.Managed.VersionInfo {
		if vi.Version == gvk.Version {
			continue
		}
		// List + re-stamp THROUGH the current served endpoint (gvr, whose version is
		// gvk.Version): the composition-version policy stamps the request's served version,
		// so writing via the current endpoint makes it agree with the relabel instead of
		// re-stamping the old version. Scoped to THIS definition so a shared CRD's other
		// definitions (legitimately on an older version) are never touched.
		if err := getters.UpdateCompositionsVersion(ctx, e.dynamic, gvr, vi.Version, gvk.Version, owner); err != nil {
			return fmt.Errorf("error migrating compositions from version %s: %w", vi.Version, err)
		}
	}

	if err := status.RefreshCompositionDefinitionStatus(cr, crd, gvr, gvk, pkgFS.PackageURL()); err != nil {
		return fmt.Errorf("error refreshing CompositionDefinition status: %w", err)
	}

	return nil
}

func (e *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return fmt.Errorf(errNotCR)
	}
	log := e.log.WithValues("operation", "delete")
	ctx = contexttools.CtxWithLogger(ctx, log)

	cr.SetConditions(rtv1.Deleting())

	pkg, dir, err := chart.ChartInfoFromSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return fmt.Errorf("error getting chart info: %w", err)
	}

	gvk, err := chart.ChartGroupVersionKind(pkg, dir)
	if err != nil {
		return fmt.Errorf("error getting chart GVK: %w", err)
	}

	var gvr schema.GroupVersionResource
	crdExist := true
	gvr, err = e.pluralizer.GVKtoGVR(gvk)
	if apierrors.IsNotFound(err) {
		crdExist = false
		log.Debug("Plural not found, CRD not found, skipping deletion", "gvk", gvk.String())
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error converting GVK to GVR: %w - GVK: %s", err, gvk.String())
	}
	if crdExist {
		lst, err := getters.GetCompositionDefinitionsWithVersion(ctx, e.mgmt, schema.GroupVersionKind{
			Group:   gvk.Group,
			Kind:    gvk.Kind,
			Version: gvk.Version,
		})
		if err != nil {
			return fmt.Errorf("error getting CompositionDefinitions: %w", err)
		}
		if len(lst) == 1 {
			log.Debug("Deleting Compositions of this version", "gvk", gvk.String())

			// Delete compositions of this version manually
			ul, err := getters.GetCompositions(ctx, e.dynamic, gvr)
			if err != nil {
				return fmt.Errorf("error getting compositions: %w", err)
			}

			for i := range ul.Items {
				log.Debug("Deleting composition", "name", ul.Items[i].GetName(), "namespace", ul.Items[i].GetNamespace())
				err := kube.Uninstall(ctx, e.kube, &ul.Items[i], kube.UninstallOptions{})
				if err != nil {
					return err
				}
			}

			ul, err = getters.GetCompositions(ctx, e.dynamic, gvr)
			if err != nil {
				return fmt.Errorf("error getting compositions: %w", err)
			}
			if len(ul.Items) > 0 {
				return fmt.Errorf("error undeploying CompositionDefinition: waiting for composition deletion")
			}
		}

		var skipCRD bool
		lst, err = getters.GetCompositionDefinitions(ctx, e.mgmt, schema.GroupKind{
			Group: gvk.Group,
			Kind:  gvk.Kind,
		})
		if err != nil {
			return fmt.Errorf("error getting CompositionDefinitions: %w", err)
		}
		if len(lst) > 1 {
			skipCRD = true
			log.Debug("Skipping CRD deletion, other CompositionDefinitions exist", "gvk", gvk.String())
		} else {
			skipCRD = false
			log.Debug("Deleting CRD", "gvk", gvk.String())
		}

		opts := deploy.UndeployOptions{
			DiscoveryClient:        memory.NewMemCacheClient(e.client.Discovery()),
			Spec:                   cr.Spec.Chart.DeepCopy(),
			KubeClient:             e.kube,
			GVR:                    gvr,
			Namespace:              cr.Namespace,
			SkipCRD:                skipCRD,
			DynamicClient:          e.dynamic,
			RBACFolderPath:         CDCrbacConfigFolder,
			DeploymentTemplatePath: CDCtemplateDeploymentPath,
			ServiceTemplatePath:    ServiceTemplatePath,
			ConfigmapTemplatePath:  CDCtemplateConfigmapPath,
			JsonSchemaTemplatePath: JSONSchemaTemplateConfigmapPath,
		}

		err = deploy.Undeploy(ctx, e.kube, opts)
		if err != nil {
			if errors.Is(err, deploy.ErrCompositionStillExist) {
				return fmt.Errorf("error undeploying CompositionDefinition: waiting for composition deletion")
			}
			return fmt.Errorf("error undeploying CompositionDefinition: %w", err)

		}
	} else {
		log.Debug("CRD not found, deletion has already been completed", "gvk", gvk.String())
	}

	return nil
}
