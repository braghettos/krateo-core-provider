# core-provider — runtime behavior

What the running service does: the CRDs it owns, the reconcile lifecycle, what it deploys, and its
integration contracts. Traced at `file:line` against the current tree.

## CRDs it owns

- **`CompositionDefinition`** (group `core.krateo.io`, plural `compositiondefinitions`, namespaced):
  the input — a reference to a Helm chart (`spec.chart`) and an optional remote deploy target
  (`spec.deploy.targetRef`). See `apis/.../types.go:244` and `crds/core.krateo.io_compositiondefinitions.yaml`.
- **`KubernetesTarget`** (`core.krateo.io`, cluster-scoped): names a Secret key holding a target
  cluster's kubeconfig (`types.go:133`, `crds/core.krateo.io_kubernetestargets.yaml`).

Per `CompositionDefinition`, core-provider **generates a third CRD** at runtime — the one derived
from the referenced chart (group `composition.krateo.io`, version `v<chart-version-dashed>`, kind =
Pascalized chart name; `chart.go:159-181`). Instances of THAT generated CRD are "Compositions".

## The reconcile lifecycle (`internal/controllers/compositiondefinitions/compositiondefinitions.go`)

The provider-runtime reconciler drives the standard Observe / Create / Update / Delete cycle. Each
method first fetches+unpacks the chart, derives the GVK, and reads `values.schema.json`.

### Observe (`compositiondefinitions.go:341`)
1. Sets `status.target` by probing the target cluster's discovery endpoint — `Healthy`+k8s version
   or `Down` (`setTargetStatus`, `:321`).
2. Resolves the GVR via the pluralizer; if the CRD's plural isn't discoverable yet it falls back to
   the GVR computed from the generated CRD (`:374-395`). For a deleted CR with no resolvable GVR it
   reports the external resource gone (`:376-386`).
3. If the CRD or the requested version is missing → `ResourceExists:false/true, UpToDate:false` with
   an `Unavailable` condition (`:404-426`).
4. Compares the status sub-schema (`StatusEqual`, `:433`) — drift → not-up-to-date.
5. Counts existing Compositions (`getters.GetCompositions`, `:449`).
6. Runs `deploy.Deploy` in **server dry-run** (`DryRunServer:true`, `:472`) to compute the digest of
   what *would* be rendered, and `deploy.Lookup` (`:487`) to compute the digest of what *is*
   deployed. If either differs from `status.digest` → not-up-to-date (`:479-497`).
7. Otherwise refreshes status and sets `Available` (`:499-503`).

### Create (`compositiondefinitions.go:511`)
Generates the CRD, `ApplyOrUpdateCRD`, then `deploy.Deploy` (real apply), and stores the returned
digest in `status.digest` (`:544`, `:564`, `:574`).

### Update (`compositiondefinitions.go:579`)
Same as Create, plus version migration: if the chart GVK changed, it **undeploys the CDC for the
old version** (keeping the CRD, `SkipCRD:true`, `:653-666`) and, when only the version changed for
the same kind/group, **rewrites live Compositions to the new apiVersion**
(`getters.UpdateCompositionsVersion`, `:676`).

### Delete (`compositiondefinitions.go:690`)
Sets `Deleting`. If this is the **last** `CompositionDefinition` for that version, it deletes that
version's Compositions first and waits for them to be gone (returning an error to requeue if any
remain, `:729-752`). It only deletes the CRD when **no other** `CompositionDefinition` shares the
group/kind (`SkipCRD` otherwise, `:763-769`), then `deploy.Undeploy`. `ErrCompositionStillExist`
also requeues (`:788`).

## What it deploys per version (the CDC) — `internal/tools/deploy/deploy.go`

`Deploy` (`deploy.go:305`) renders and applies, in this order, hashing each into one FNV digest:
1. **RBAC**: a ServiceAccount, ClusterRole/ClusterRoleBinding, Role/RoleBinding (`createRBACResources`,
   `:81`); plus a Secret-scoped Role/RoleBinding when the chart uses private-repo credentials
   (`:325-371`).
2. **JSON-schema ConfigMap** holding the chart's `values.schema.json` (`:378`).
3. **CDC config ConfigMap** carrying the SA name/namespace (`:395`).
4. **Deployment** running `ghcr.io/krateoplatformops/composition-dynamic-controller` with
   `-group/-version/-resource/-namespace` args (template at
   `.../testdata/manifests/deployment.yaml`).
5. **Service** — only if the service template file exists on disk (`:437`).

In non-dry-run mode it waits for the Deployment to be Ready, restarts it so it picks up the new
ConfigMap, then waits again (`:458-485`). Resource names follow `<plural>-<version>-controller`,
`-configmap`, `-jsonschema-configmap` (`resourceNamer`, `:77`; suffix helpers `:779-810`).

## The idempotency / digest contract

core-provider is **digest-driven**, which is what keeps it from churning stateful components on
no-op reconciles: `status.digest` is an order-stable FNV hash over every rendered object's
identity + spec. Observe compares the *would-render* digest (dry-run `Deploy`) AND the *live* digest
(`Lookup`) against `status.digest`; only a real difference triggers Create/Update. A reconcile that
changes nothing computes an identical digest and is a no-op.

## Generated-CRD behavior

- **Multi-version**: when a new chart version is applied for an existing kind, `ApplyOrUpdateCRD`
  (`crd.go:143`) appends the new version (served) and demotes the others to served-not-storage,
  with a permissive **`vacuum`** version as the single storage version for lossless cross-version
  storage (`generation.go:34-97`).
- **No conversion webhook**: generated CRDs always use `Strategy: None` (`crd.go:247`). The
  per-object `krateo.io/composition-version` label (`deploy.go:34`) is stamped in-apiserver by a
  **`MutatingAdmissionPolicy`** shipped by the chart — NOT by core-provider (`compositiondefinitions.go:94-97`).
- **Status-only updates**: if only the status schema differs, core-provider updates just the status
  sub-schema across versions to avoid disturbing the dynamically generated spec (`crd.go:186-206`).

## Local vs remote targets

With no `spec.deploy.targetRef`, everything (generated CRD, RBAC, CDC) is deployed into the
management cluster (`DeploymentModeLocal`). With a `targetRef`, `clusterkube.Remote`
(`clusterkube.go:47`) reads the cluster-scoped `KubernetesTarget` → its kubeconfig Secret → builds
target-cluster clients; the `CompositionDefinition` and secrets stay local. `status.target` reports
`mode`, `connectionStatus`, the target's k8s `version`, and the kubeconfig Secret's
`resourceVersion` for rotation traceability (`types.go:182-201`, set at `compositiondefinitions.go:321`).

## Integration contracts (endpoints)

- **`:8080`** — controller-runtime metrics server (`main.go:113`). No `/call`-style content API;
  core-provider is a controller, not an HTTP service.
- **OTLP metrics** — opt-in via `OTEL_ENABLED`; service name `core-provider`, default 30s export
  (`main.go:46`, `:80-87`). Catalog and example queries in `telemetry/metrics-reference.md`.
- **The CDC** it deploys is the runtime that actually reconciles Compositions; its image/version is
  a separate component (`ghcr.io/krateoplatformops/composition-dynamic-controller`).
