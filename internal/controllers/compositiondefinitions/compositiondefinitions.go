package compositiondefinitions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	apiextensionsscheme "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	"github.com/krateoplatformops/core-provider/internal/controllers/compositiondefinitions/conversion"
	"github.com/krateoplatformops/core-provider/internal/controllers/compositiondefinitions/generator"
	"github.com/krateoplatformops/core-provider/internal/tools"
	"github.com/krateoplatformops/core-provider/internal/tools/chartfs"
	"github.com/krateoplatformops/core-provider/internal/tools/clusterkube"
	crdtools "github.com/krateoplatformops/core-provider/internal/tools/crd"
	"github.com/krateoplatformops/core-provider/internal/tools/deploy"
	"github.com/krateoplatformops/core-provider/internal/tools/deployment"
	"github.com/krateoplatformops/core-provider/internal/tools/env"
	"github.com/krateoplatformops/crdgen"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
	"github.com/krateoplatformops/provider-runtime/pkg/controller"
	"github.com/krateoplatformops/provider-runtime/pkg/event"
	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	"github.com/krateoplatformops/provider-runtime/pkg/meta"
	"github.com/krateoplatformops/provider-runtime/pkg/ratelimiter"
	"github.com/krateoplatformops/provider-runtime/pkg/reconciler"
	"github.com/krateoplatformops/provider-runtime/pkg/resource"
	"github.com/pkg/errors"
)

const (
	errNotCR                       = "managed resource is not a Definition custom resource"
	reconcileGracePeriod           = 1 * time.Minute
	reconcileTimeout               = 4 * time.Minute
	compositionStillExistFinalizer = "composition.krateo.io/still-exist-compositions-finalizer"

	cdcImageTagEnvVar            = "CDC_IMAGE_TAG"
	helmRegistryConfigPathEnvVar = "HELM_REGISTRY_CONFIG_PATH"
)

var (
	// Build webhooks used for the various server
	// configuration options
	//
	// These handlers could be also be implementations
	// of the AdmissionHandler interface for more complex
	// implementations.
	mutatingHook = &webhook.Admission{
		Handler: admission.HandlerFunc(func(ctx context.Context, req webhook.AdmissionRequest) webhook.AdmissionResponse {
			unstructuredObj := &unstructured.Unstructured{}
			if err := json.Unmarshal(req.Object.Raw, unstructuredObj); err != nil {
				return webhook.Errored(http.StatusBadRequest, err)
			}
			if unstructuredObj.GetLabels() == nil || len(unstructuredObj.GetLabels()) == 0 {
				return webhook.Patched("mutating webhook called - insert krateo.io/composition-version label",
					webhook.JSONPatchOp{Operation: "add", Path: "/metadata/labels", Value: map[string]string{}},
					webhook.JSONPatchOp{Operation: "add", Path: "/metadata/labels/krateo.io~1composition-version", Value: req.Kind.Version},
				)
			}
			return webhook.Patched("mutating webhook called - insert krateo.io/composition-version label",
				webhook.JSONPatchOp{Operation: "add", Path: "/metadata/labels/krateo.io~1composition-version", Value: req.Kind.Version},
			)
		}),
	}
	compositionConversionWebhook = conversion.NewWebhookHandler(runtime.NewScheme())
	cabundle                     = GetCABundle()
	webhookServiceName           = env.GetEnvOrDefault("CORE_PROVIDER_WEBHOOK_SERVICE_NAME", "core-provider-webhook-service")
	webhookServiceNamespace      = env.GetEnvOrDefault("CORE_PROVIDER_WEBHOOK_SERVICE_NAMESPACE", "default")
	// webhookURL, when set, is the externally reachable base URL of core-provider's
	// conversion endpoint. It is required to enable multi-version conversion for CRDs
	// deployed to remote target clusters, whose API servers cannot resolve the
	// in-cluster webhook Service of the management cluster.
	webhookURL             = env.GetEnvOrDefault("CORE_PROVIDER_WEBHOOK_URL", "")
	helmRegistryConfigPath = env.GetEnvOrDefault(helmRegistryConfigPathEnvVar, chartfs.HelmRegistryConfigPathDefault)
)

func GetCABundle() []byte {
	// CertDir is the directory that contains the server key and certificate. Defaults to
	// <temp-dir>/k8s-webhook-server/serving-certs.
	fb, err := os.ReadFile(path.Join(os.TempDir(), "k8s-webhook-server/serving-certs/tls.crt"))
	if err != nil {
		return nil
	}

	return fb
}

// conversionStrategy describes how the conversion webhook was wired for a CRD.
type conversionStrategy string

const (
	conversionService conversionStrategy = "service" // in-cluster webhook Service (local target)
	conversionURL     conversionStrategy = "url"     // externally reachable URL (remote target)
	conversionNone    conversionStrategy = "none"    // no conversion (remote target, no URL configured)
)

// conversionConfFor builds the CRD conversion configuration for the cluster the CRD is
// being deployed to.
//
//   - local target  -> WebhookConverter via the in-cluster webhook Service.
//   - remote target -> WebhookConverter via CORE_PROVIDER_WEBHOOK_URL when set, so the
//     remote API server can reach the management cluster's conversion endpoint.
//   - remote target without a URL -> NoneConverter. A Service-based webhook would be
//     unreachable from the remote API server and would break every request that needs
//     conversion; skipping conversion is strictly safer. The caller should warn.
func conversionConfFor(remote bool) (*apiextensionsv1.CustomResourceConversion, conversionStrategy) {
	whport := int32(9443)
	whpath := "/convert"
	reviewVersions := []string{"v1", "v1alpha1", "v1alpha2"}

	if remote {
		if webhookURL == "" {
			return &apiextensionsv1.CustomResourceConversion{
				Strategy: apiextensionsv1.NoneConverter,
			}, conversionNone
		}
		url := strings.TrimRight(webhookURL, "/")
		if !strings.HasSuffix(url, whpath) {
			url += whpath
		}
		return &apiextensionsv1.CustomResourceConversion{
			Strategy: apiextensionsv1.WebhookConverter,
			Webhook: &apiextensionsv1.WebhookConversion{
				ConversionReviewVersions: reviewVersions,
				ClientConfig: &apiextensionsv1.WebhookClientConfig{
					URL:      &url,
					CABundle: cabundle,
				},
			},
		}, conversionURL
	}

	return &apiextensionsv1.CustomResourceConversion{
		Strategy: apiextensionsv1.WebhookConverter,
		Webhook: &apiextensionsv1.WebhookConversion{
			ConversionReviewVersions: reviewVersions,
			ClientConfig: &apiextensionsv1.WebhookClientConfig{
				Service: &apiextensionsv1.ServiceReference{
					Namespace: webhookServiceNamespace,
					Name:      webhookServiceName,
					Port:      &whport,
					Path:      &whpath,
				},
				CABundle: cabundle,
			},
		},
	}, conversionService
}

func Setup(mgr ctrl.Manager, o controller.Options) error {
	_ = apiextensionsscheme.AddToScheme(clientsetscheme.Scheme)

	name := reconciler.ControllerName(compositiondefinitionsv1alpha1.CompositionDefinitionGroupKind)

	l := o.Logger.WithValues("controller", name)

	recorder := mgr.GetEventRecorderFor(name)

	mgr.GetWebhookServer().Register("/mutate", mutatingHook)
	mgr.GetWebhookServer().Register("/convert", compositionConversionWebhook)
	cli := mgr.GetClient()

	r := reconciler.NewReconciler(mgr,
		resource.ManagedKind(compositiondefinitionsv1alpha1.CompositionDefinitionGroupVersionKind),
		reconciler.WithExternalConnecter(&connector{
			discovery: discovery.NewDiscoveryClientForConfigOrDie(mgr.GetConfig()),
			dynamic:   dynamic.NewForConfigOrDie(mgr.GetConfig()),
			kube:      cli,
			log:       l,
			recorder:  recorder,
		}),
		reconciler.WithTimeout(reconcileTimeout),
		reconciler.WithCreationGracePeriod(reconcileGracePeriod),
		reconciler.WithPollInterval(o.PollInterval),
		reconciler.WithLogger(l),
		reconciler.WithRecorder(event.NewAPIRecorder(recorder)))

	chartfs.HelmRegistryConfigPath = helmRegistryConfigPath

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&compositiondefinitionsv1alpha1.CompositionDefinition{}).
		Complete(ratelimiter.New(name, r, o.GlobalRateLimiter))

}

type connector struct {
	dynamic   dynamic.Interface
	discovery discovery.DiscoveryInterface
	kube      client.Client
	log       logging.Logger
	recorder  record.EventRecorder
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (reconciler.ExternalClient, error) {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return nil, errors.New(errNotCR)
	}

	if meta.IsVerbose(mg) {
		log.SetOutput(os.Stderr)
	} else {
		log.SetOutput(io.Discard)
	}

	ext := &external{
		mgmt:      c.kube,
		kube:      c.kube,
		dynamic:   c.dynamic,
		discovery: c.discovery,
		log:       c.log,
		rec:       c.recorder,
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
		ext.discovery = tc.Discovery
		if meta.IsVerbose(cr) {
			c.log.Debug("Deploying to remote target cluster",
				"host", tc.Config.Host, "name", cr.Name)
		}
	}

	return ext, nil
}

type external struct {
	// mgmt is the management-cluster client: it holds the CompositionDefinition
	// resource, the chart/credentials secrets, and is where status is persisted.
	mgmt client.Client
	// kube, dynamic and discovery target the cluster where the generated CRD, RBAC and
	// the composition-dynamic-controller are deployed (local == mgmt, or a remote
	// target cluster).
	discovery discovery.DiscoveryInterface
	dynamic   dynamic.Interface
	kube      client.Client
	log       logging.Logger
	rec       record.EventRecorder
}

func (e *external) Observe(ctx context.Context, mg resource.Managed) (reconciler.ExternalObservation, error) {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return reconciler.ExternalObservation{}, errors.New(errNotCR)
	}

	if meta.FinalizerExists(cr, compositionStillExistFinalizer) {
		return reconciler.ExternalObservation{
			ResourceExists:   false,
			ResourceUpToDate: true,
		}, e.Delete(ctx, cr)
	}

	pkg, err := chartfs.ForSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}

	gvk, err := tools.GroupVersionKind(pkg)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}
	gvr := tools.ToGroupVersionResource(gvk)
	log.Printf("[DBG] Observing (gvk: %s, gvr: %s)\n", gvk.String(), gvr.String())

	crdOk, err := crdtools.Lookup(ctx, e.kube, gvr)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}

	if !crdOk {
		log.Printf("[DBG] CRD does not exists yet (gvr: %q)\n", gvr.String())
		cr.SetConditions(rtv1.Unavailable().
			WithMessage(fmt.Sprintf("CRD for '%s' does not exists yet", gvr.String())))
		return reconciler.ExternalObservation{
			ResourceExists:   false,
			ResourceUpToDate: true,
		}, nil
	}

	crd, err := crdtools.Get(ctx, e.kube, gvr)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}

	log.Printf("[DBG] Searching for Dynamic Controller (gvr: %q)\n", gvr.String())

	obj, err := deployment.CreateDeployment(gvr, types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Name,
	}, os.Getenv(cdcImageTagEnvVar))
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}

	deployOk, deployReady, err := deployment.LookupDeployment(ctx, e.kube, &obj)
	if err != nil {
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: true,
		}, err
	}

	if !deployOk {
		if meta.IsVerbose(cr) {
			e.log.Debug("Dynamic Controller not deployed yet",
				"name", obj.Name, "namespace", obj.Namespace, "gvr", gvr.String())
		}

		cr.SetConditions(rtv1.Unavailable().
			WithMessage(fmt.Sprintf("Dynamic Controller '%s' not deployed yet", obj.Name)))

		return reconciler.ExternalObservation{
			ResourceExists:   false,
			ResourceUpToDate: true,
		}, nil
	}

	if meta.IsVerbose(cr) {
		e.log.Debug("Dynamic Controller already deployed",
			"name", obj.Name, "namespace", obj.Namespace,
			"gvr", gvr.String())
	}

	if !deployReady {
		cr.SetConditions(rtv1.Unavailable().
			WithMessage(fmt.Sprintf("Dynamic Controller '%s' not ready yet", obj.Name)))

		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: true,
		}, nil
	}

	// if version is different, Update
	oldGVK := schema.FromAPIVersionAndKind(cr.Status.ApiVersion, cr.Status.Kind)
	if oldGVK.Version != gvk.Version && cr.Status.Kind == gvk.Kind && oldGVK.Group == gvk.Group {
		e.log.Info("Updating CompositionDefinition GVK", "old", oldGVK, "new", gvk)
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	// Sets the status of the CompositionDefinition
	if crd != nil {
		updateVersionInfo(cr, crd, gvr)
		cr.Status.Managed.Group = crd.Spec.Group
		cr.Status.Managed.Kind = crd.Spec.Names.Kind
	}
	cr.Status.ApiVersion, cr.Status.Kind = gvk.ToAPIVersionAndKind()
	cr.Status.PackageURL = pkg.PackageURL()

	if cr.Status.Error != nil {
		cr.SetConditions(rtv1.Unavailable().WithMessage(*cr.Status.Error))
	} else {
		cr.SetConditions(rtv1.Available())
	}
	return reconciler.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

func (e *external) Create(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return errors.New(errNotCR)
	}

	if !meta.IsActionAllowed(cr, meta.ActionCreate) {
		e.log.Debug("External resource should not be updated by provider, skip creating.")
		return nil
	}

	pkg, dir, err := generator.ChartInfoFromSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return err
	}

	gvk, err := generator.ChartGroupVersionKind(pkg, dir)
	if err != nil {
		return err
	}

	gvr := tools.ToGroupVersionResource(gvk)

	crdOk, err := crdtools.Lookup(ctx, e.kube, gvr)
	if err != nil {
		return err
	}

	var crd *apiextensionsv1.CustomResourceDefinition
	if !crdOk {
		crd, err = crdtools.Get(ctx, e.kube, gvr)
		if err != nil {
			return err
		}

		if meta.IsVerbose(cr) {
			e.log.Debug("Generating CRD", "gvr", gvr.String())
		}

		cr.SetConditions(rtv1.Condition{
			Type:               rtv1.TypeReady,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: metav1.Now(),
			Reason:             "GeneratingCRD",
			Message:            fmt.Sprintf("Generating CRD for: %s", gvr),
		})

		res := crdgen.Generate(ctx, crdgen.Options{
			Managed:                true,
			WorkDir:                dir,
			GVK:                    gvk,
			Categories:             []string{"compositions", "comps"},
			SpecJsonSchemaGetter:   generator.ChartJsonSchemaGetter(pkg, dir),
			StatusJsonSchemaGetter: StaticJsonSchemaGetter(),
		})
		if res.Err != nil {
			return res.Err
		}

		newcrd, err := crdtools.Unmarshal(res.Manifest)
		if err != nil {
			return err
		}

		if crd == nil {
			return crdtools.Install(ctx, e.kube, newcrd)
		}

		crd, err = crdtools.AppendVersion(*crd, *newcrd)
		if err != nil {
			return err
		}

		remote := clusterkube.IsRemote(cr.Spec.Deploy)
		conv, strategy := conversionConfFor(remote)
		if strategy == conversionNone {
			e.log.Info("Multi-version conversion disabled for remote target: the remote API server cannot reach the management webhook Service. "+
				"Set CORE_PROVIDER_WEBHOOK_URL to an externally reachable /convert endpoint to enable conversion.",
				"gvr", gvr.String(), "name", cr.Name)
		}

		crd = crdtools.ConversionConf(*crd, conv)
		return crdtools.Update(ctx, e.kube, crd.Name, crd)
	} else {
		crd, err = crdtools.Get(ctx, e.kube, gvr)
		if err != nil {
			return err
		}
		if crd == nil {
			return errors.New("CRD not found")
		}

		if meta.IsVerbose(cr) {
			e.log.Debug("CRD already generated, checking served resources", "gvr", gvr.String())
		}

		err = crdtools.Update(ctx, e.kube, crd.Name, crd)
		if err != nil {
			return err
		}
	}

	if meta.IsVerbose(cr) {
		e.log.Debug("Deploying Dynamic Controller",
			"gvr", gvr.String(),
			"namespace", cr.Namespace,
		)
	}

	opts := deploy.DeployOptions{
		DiscoveryClient: memory.NewMemCacheClient(e.discovery),
		KubeClient:      e.kube,
		NamespacedName: types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      resourceNamer(gvr.Resource, gvr.Version),
		},
		CDCImageTag: os.Getenv(cdcImageTagEnvVar),
		Spec:        cr.Spec.Chart.DeepCopy(),
	}
	if meta.IsVerbose(cr) {
		opts.Log = e.log.Debug
	}

	err, rbacErr := deploy.Deploy(ctx, e.kube, opts)
	if rbacErr != nil {
		strErr := rbacErr.Error()
		cr.Status.Error = &strErr
		e.log.Info("Error deploying Dynamic Controller", "error", rbacErr.Error())
		cr.SetConditions(rtv1.Unavailable().WithMessage(rbacErr.Error()))
	}
	if err != nil {
		return err
	}

	err = e.mgmt.Status().Update(ctx, cr)
	if err != nil {
		return err
	}

	if meta.IsVerbose(cr) {
		e.log.Debug("Dynamic Controller successfully deployed",
			"gvr", gvr.String(),
			"namespace", cr.Namespace,
		)
	}

	return nil
}

func (e *external) Update(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return errors.New(errNotCR)
	}

	if !meta.IsActionAllowed(cr, meta.ActionUpdate) {
		e.log.Debug("External resource should not be updated by provider, skip updating.")
		return nil
	}

	if meta.IsVerbose(cr) {
		e.log.Debug("Updating CompositionDefinition", "name", cr.Name)
	}

	pkg, dir, err := generator.ChartInfoFromSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return err
	}

	gvk, err := generator.ChartGroupVersionKind(pkg, dir)
	if err != nil {
		return err
	}

	gvr := tools.ToGroupVersionResource(gvk)

	crd, err := crdtools.Get(ctx, e.kube, gvr)
	if err != nil {
		return err
	}
	if meta.IsVerbose(cr) {
		e.log.Debug("Updating Compositions", "gvr", gvr.String())
	}

	oldGVK := schema.FromAPIVersionAndKind(cr.Status.ApiVersion, cr.Status.Kind)

	if meta.IsVerbose(cr) {
		e.log.Debug("Updating from GVK", "old", oldGVK, "new", gvk)
	}

	// Undeploy olders versions of the CRD
	for _, vi := range cr.Status.Managed.VersionInfo {
		if oldGVK.Kind == cr.Status.Managed.Kind && oldGVK.Version == vi.Version {
			err = deploy.Undeploy(ctx, e.kube, deploy.UndeployOptions{
				DiscoveryClient: memory.NewMemCacheClient(e.discovery),
				DynamicClient:   e.dynamic,
				Spec:            (*compositiondefinitionsv1alpha1.ChartInfo)(vi.Chart),
				KubeClient:      e.kube,
				NamespacedName: types.NamespacedName{
					Name:      resourceNamer(gvr.Resource, oldGVK.Version),
					Namespace: cr.Namespace,
				},
				SkipCRD: true,
			})
			if err != nil {
				return err
			}
			if meta.IsVerbose(cr) {
				e.log.Debug("Undeployed old version of CRD", "gvr", gvr.String())
			}
		}
	}

	if oldGVK.Version != gvk.Version && cr.Status.Kind == gvk.Kind && oldGVK.Group == gvk.Group {
		err := updateCompositionsVersion(ctx, e.dynamic, e.log, CompositionsInfo{
			GVR: schema.GroupVersionResource{
				Group:    oldGVK.Group,
				Version:  oldGVK.Version,
				Resource: tools.ToGroupVersionResource(oldGVK).Resource,
			},
			Namespace: cr.Namespace,
		}, gvk.Version)
		if err != nil {
			return fmt.Errorf("error updating compositions version: %w", err)
		}

		if meta.IsVerbose(cr) {
			e.log.Debug("Updated compositions version", "gvr", gvr.String())
		}
	}

	// Sets the new version as served in the CRD
	crdtools.SetServedStorage(crd, gvk.Version, true, false)

	err = crdtools.Update(ctx, e.kube, crd.Name, crd)
	if err != nil {
		return err
	}

	cr.Status.ApiVersion, cr.Status.Kind = gvk.ToAPIVersionAndKind()

	err = e.mgmt.Status().Update(ctx, cr)
	if err != nil {
		return err
	}

	return nil
}

func (e *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*compositiondefinitionsv1alpha1.CompositionDefinition)
	if !ok {
		return errors.New(errNotCR)
	}

	e.log.Debug("Deleting CompositionDefinition", "name", cr.Name)

	if !meta.IsActionAllowed(cr, meta.ActionDelete) {
		e.log.Debug("External resource should not be deleted by provider, skip deleting.")
		return nil
	}

	pkg, dir, err := generator.ChartInfoFromSpec(ctx, e.mgmt, cr.Spec.Chart)
	if err != nil {
		return err
	}

	gvk, err := generator.ChartGroupVersionKind(pkg, dir)
	if err != nil {
		return err
	}

	gvr := tools.ToGroupVersionResource(gvk)

	var skipCRD bool
	lst, err := getCompositionDefinitions(ctx, e.mgmt, gvk)
	if err != nil {
		e.log.Debug("Error getting CompositionDefinitions", "error", err)
		return fmt.Errorf("error getting CompositionDefinitions: %w", err)
	}
	if len(lst) > 1 {
		skipCRD = true
	}

	opts := deploy.UndeployOptions{
		DiscoveryClient: memory.NewMemCacheClient(e.discovery),
		Spec:            cr.Spec.Chart.DeepCopy(),
		KubeClient:      e.kube,
		GVR:             gvr,
		NamespacedName: types.NamespacedName{
			Name:      resourceNamer(gvr.Resource, gvr.Version),
			Namespace: cr.Namespace,
		},
		SkipCRD:       skipCRD,
		DynamicClient: e.dynamic,
	}
	if meta.IsVerbose(cr) {
		opts.Log = e.log.Debug
	}

	err = deploy.Undeploy(ctx, e.kube, opts)
	if err != nil {
		if errors.Is(err, deploy.ErrCompositionStillExist) {
			if !meta.FinalizerExists(cr, compositionStillExistFinalizer) {
				e.log.Debug("Adding finalizer to CompositionDefinition", "name", cr.Name)
				meta.AddFinalizer(cr, compositionStillExistFinalizer)
				err = e.mgmt.Update(ctx, cr)
				if err != nil {
					return err
				}
			}
		}
	}

	meta.RemoveFinalizer(cr, compositionStillExistFinalizer)
	return e.mgmt.Update(ctx, cr)
}
