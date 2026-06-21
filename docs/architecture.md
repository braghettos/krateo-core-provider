# core-provider — architecture

How the service is built. This is the internals view; the deployment/chart view lives in
`braghettos/krateo-core-provider-chart` `docs/`. Every claim is traced to the current tree at
`file:line`; if a comment or README disagrees with the code, the code wins.

> **Read this as a map.** core-provider is a controller-runtime manager hosting exactly one
> controller. The interesting logic is in the `internal/tools/*` packages it calls during reconcile.

## What core-provider is

A Krateo provider that makes a Helm chart a first-class Kubernetes API. For each
`CompositionDefinition` custom resource (a chart reference) it:

1. fetches the chart and derives a `GroupVersionKind` from its `Chart.yaml`;
2. generates a versioned CRD from the chart's `values.schema.json`;
3. deploys a per-version **composition-dynamic-controller (CDC)** — a Deployment plus a config
   ConfigMap, a JSON-schema ConfigMap, optional Service, and RBAC — that reconciles instances
   ("Compositions") of the generated CRD.

It is a fork of `krateoplatformops/core-provider` (`go.mod:1`, `git remote upstream`). It is real
service code (~10k LOC of Go), not a thin wrapper.

## Entry point (`main.go`)

`main()` is a standard controller-runtime program (`main.go:38`):

- Flags/env are prefixed `CORE_PROVIDER_*` (`main.go:39`): `-debug`, `-sync` (cache resync, default
  1h), `-poll` (drift poll, default 3m), `-max-reconcile-rate` (default 5), `-leader-election`,
  exponential retry bounds, and `OTEL_*` telemetry toggles.
- Logs are emitted as one JSON object per line for logs-ingester compatibility
  (`loghandler.NewJSONHandler`, `main.go:64`).
- OpenTelemetry metrics are set up via `telemetry.Setup` and exported only when `OTEL_ENABLED`
  (`main.go:83`, `main.go:46`).
- The manager binds a metrics server on `:8080` (`main.go:113`) and uses a priority work queue
  (`main.go:116`). **There is no webhook server or serving certificate** — core-provider hosts no
  admission webhooks since 2.0.0 (`main.go:104-105`).
- It registers the APIs (`apis.AddToScheme`, `main.go:133`) and wires the single controller via
  `compositiondefinitions.Setup` (`main.go:137`), passing a non-fake `pluralizer.New(false)`.

## The API types (`apis/compositiondefinitions/v1alpha1/types.go`)

Two CRDs live in this group:

- **`CompositionDefinition`** (namespaced, `types.go:244`): `spec.chart` (`ChartInfo`: `url`,
  `version`, `repo`, `insecureSkipVerifyTLS`, optional `credentials`, `types.go:35`) and optional
  `spec.deploy.targetRef` selecting a remote target (`types.go:101-118`). Since 2.3.0 it also
  carries two optional **status-projection** fields: `spec.statusDataTemplate`
  (`[]StatusFieldMapping`, `types.go:135`) — snowplow `widgetDataTemplate`-shaped
  `{forPath, expression, type?, schema?, preserveUnknownFields?}` entries whose `${ jq }`
  expressions are written under `.status` at `forPath` — and `spec.apiRef` (`ApiReference`,
  `types.go:125`) — a `name`/`namespace` reference to a `RESTAction` plus inline static `extras`,
  mirroring snowplow's `spec.apiRef`. Its `status`
  (`types.go:204`) carries the conditioned status, last-applied `apiVersion`/`kind`/`resource`, a
  `managed.versionInfo` list of served CRD versions, the chart `packageUrl`, a `target` block
  (mode/connection/version), and a **`digest`** of the rendered CDC resources.
- **`KubernetesTarget`** (cluster-scoped, `types.go:133`): `spec.kubeconfigRef` — a `SecretKeySelector`
  pointing at a Secret key holding a target cluster's kubeconfig (`types.go:121-126`). It is the
  credential-rotation seam.

The provider's own two CRD manifests are checked into `crds/`.

## The controller (`internal/controllers/compositiondefinitions/`)

`Setup` (`compositiondefinitions.go:69`) builds a `provider-runtime` reconciler over
`CompositionDefinition` and:

- on startup, removes an obsolete finalizer label for backward compatibility
  (`cleanupObsoleteFinalizerLabels`, `compositiondefinitions.go:88`, `:218`);
- watches **Secrets** and **KubernetesTargets** so a rotated chart credential or repointed
  kubeconfig re-reconciles the affected `CompositionDefinition`s promptly
  (`compositiondefinitions.go:125-128`, mappers at `:135` and `:173`).

`Connect` (`compositiondefinitions.go:262`) builds the `external` client. By default it targets the
local management cluster; when `clusterkube.IsRemote(cr.Spec.Deploy)` it resolves the remote
clients and flips `kube`/`dynamic`/`client` to the target cluster while keeping `mgmt` local
(`compositiondefinitions.go:284-295`). The `external.mgmt` client always holds the
`CompositionDefinition` and its secrets; `external.kube`/`dynamic`/`client` are where the CRD, RBAC
and CDC land (`compositiondefinitions.go:300-317`).

The four reconcile methods are detailed in [behavior.md](behavior.md).

## The key flows (the `internal/tools/*` packages)

The controller is thin; the work is in these packages:

- **`chart`** (`internal/tools/chart/chart.go`): fetches the chart bytes with bounded retry
  (`ChartInfoFromSpec`, `chart.go:41`; retryability classifier at `:100`), unpacks the single-root
  tgz (`ChartInfoFromBytes`, `:139`), derives the GVK — group is the fixed `composition.krateo.io`,
  version is `v<chart-version-with-dots-as-dashes>`, kind is the Pascalized chart name
  (`ChartGroupVersionKind`, `:159-181`) — and reads `values.schema.json` (`ChartJsonSchema`, `:183`).
  `chartfs/` provides the same over an `fs.FS` view.
- **`crd/generation`** (`generation.go`): `GenerateCRD` turns the chart's spec schema + a static
  status schema into a CRD via `plumbing/crdgen` (`generation.go:123`, `:205`). `AppendVersion`
  (`:34`) adds a new served version to an existing CRD and injects a permissive **`vacuum`** storage
  version for lossless multi-version storage (`:54-85`). `StatusEqual` (`:135`) compares only the
  status sub-schema by FNV hash. `statusfields.go` extends the generated status schema from the
  `CompositionDefinition`'s `statusDataTemplate`: `ValidateStatusFields` checks each declared
  field (non-empty `forPath`, no collision with the reserved baseline status keys, no duplicates,
  type/schema/`preserveUnknownFields` mutual exclusion, parseable `${ jq }`), and
  `InjectStatusFields` writes each declared `forPath` as a (possibly nested) property under the
  status schema of **every** version, so the CDC's projected writes survive status-subresource
  pruning (`statusfields.go:39`, `:77`). The controller calls both around `GenerateCRD` on
  Create/Update and re-injects on Observe (`compositiondefinitions.go:645-654`, `:815`, `:899`).
- **`crd`** (`crd.go`): `ApplyOrUpdateCRD` (`crd.go:143`) creates the CRD if absent, else
  status-only-updates, else appends a version; it always sets **`None` conversion**
  (`setNoneConversion`, `:247`) and waits for the CRD to be Established.
- **`deploy`** (`deploy.go`): `Deploy` (`deploy.go:305`) renders and applies the CDC's RBAC, the two
  ConfigMaps, the Deployment and optional Service, hashing every object into a single FNV **digest**;
  `Lookup` (`:618`) recomputes that digest from the live cluster; `Undeploy` (`:490`) tears it down.
  Resources are named `<plural>-<version>-controller` etc. (`resourceNamer`, `:77`). The
  status-projection config rides to the CDC through its config ConfigMap as
  `COMPOSITION_CONTROLLER_STATUS_DATA_TEMPLATE` and `COMPOSITION_CONTROLLER_API_REF_NAME` /
  `_NAMESPACE` / `_EXTRAS` (`configmap.yaml`, encoded at `compositiondefinitions.go:492`, `:514`).
  When `apiRef` is declared, the Deployment additionally mounts a projected `authn`-audience
  ServiceAccount token at `/var/run/secrets/krateo.io/serviceaccount/token` (1h expiry, gated on
  `api_ref_name`, `deployment.yaml:53-67`), and `authnmapping.go` auto-provisions an authn
  allowlist mapping (`serviceaccount.authn.krateo.io/ServiceAccount`) in the authn operator
  namespace (`AuthnNamespace`, env `COMPOSITION_AUTHN_NAMESPACE`, default `krateo-system`) that
  grants the per-composition group `krateo:cdc:<resource>-<apiVersion>`; the mapping is hashed into
  the digest and deleted on `Undeploy` (`authnmapping.go:58`, `:108`, `deploy.go:621`). At runtime
  the CDC presents that token to authn to obtain a service JWT, resolves the `RESTAction` via
  snowplow under that identity each reconcile, and exposes the result under `.api` for
  `statusDataTemplate` to read — so the issued group, scoped by ordinary Kubernetes RBAC, bounds
  what the projection may read. See the how-to `docs/how-to/apiref-status-projection-authn.md`.
- **`clusterkube`** (`clusterkube.go`): resolves the local-or-remote target clients from a
  `KubernetesTarget` + kubeconfig Secret, re-read every reconcile so external rotation is picked up
  (`Remote`, `clusterkube.go:47`).
- Supporting: `pluralizer` (GVK→GVR via discovery), `objects` (render a template into a typed k8s
  object), `kube` (apply/uninstall helpers), `kube/watcher` (wait-for-ready), `retry`, `hash`
  (FNV object hasher), `deployment` (CDC restart + readiness), `resolvers` (Secret lookup), `tgzfs`,
  `strutil`, `context` (logger in ctx).

## Build & runtime shape

- `Dockerfile` builds a static `manager` binary (Go 1.25, `Dockerfile`); `.ko.yaml` defines the `ko`
  build for dev images.
- The CDC image it deploys is `ghcr.io/krateoplatformops/composition-dynamic-controller`
  (`internal/controllers/compositiondefinitions/testdata/manifests/deployment.yaml:29`) — a SEPARATE
  component, not built here.
- `telemetry/` ships an OTel collector config, a Grafana dashboard and the metric catalog
  (`telemetry/metrics-reference.md`).
