# core-provider — gotchas

Real runtime pitfalls, each grounded in code/config at `file:line`. If a note here ever disagrees
with the code at the deployed tag, the code wins.

## Requires Kubernetes >= 1.36 on every cluster a composition CRD lives in
Since 2.0.0 core-provider hosts no admission webhooks. Generated CRDs use `None` conversion
(`crd.go:247`) and the per-object `krateo.io/composition-version` label (`deploy.go:34`) is stamped
by a **`MutatingAdmissionPolicy`** (GA `admissionregistration.k8s.io/v1`), which needs k8s >= 1.36
(`compositiondefinitions.go:94-97`, README "Requirements"). This applies to **remote targets too** —
not just the management cluster.

## core-provider does NOT install the MutatingAdmissionPolicy
The policy that stamps the composition-version label is shipped **declaratively by the Helm chart**,
not by this binary (`compositiondefinitions.go:95-96`). If you run the binary without the chart's
policy (or deploy to a remote target that lacks it), composition objects won't get the version
label. The chart owns it — see `braghettos/krateo-core-provider-chart`.

## CDC asset templates are read from disk at runtime, not embedded
The controller loads its CDC templates from `os.TempDir()/assets/...` — deployment, configmap,
RBAC folder, json-schema configmap, service (`compositiondefinitions.go:55-59`). These files are
**not in this repo**; they are provided by the chart/runtime image. If those paths are missing or
stale, `objects.CreateK8sObject` fails during Deploy. The **Service** in particular is only rendered
when its template file exists on disk — `os.Stat(opts.ServiceTemplatePath)` gates it
(`deploy.go:437`, `:751`, `:555`). No service template → no CDC Service, silently.

## The CDC image is a separate component pinned to `:latest`
The deployment template runs `ghcr.io/krateoplatformops/composition-dynamic-controller:latest` with
`imagePullPolicy: IfNotPresent` (`.../testdata/manifests/deployment.yaml:29-30`). core-provider's
own version says nothing about the CDC version; pin/override the CDC image via the chart, and beware
`:latest` + `IfNotPresent` pinning a stale node-cached image.

## A shared generated CRD is reference-counted across CompositionDefinitions
Delete only removes the generated CRD when it is the **last** `CompositionDefinition` for that
group/kind; otherwise `SkipCRD` is set and the CRD is left in place (`compositiondefinitions.go:756-769`).
Likewise it only deletes a version's Compositions when it is the last definition *for that version*
(`:729`). Deleting one definition will NOT remove a CRD another definition still uses. (Per project
history, mismatches here have caused "stops emitting Compositions" symptoms — verify the
reference-count branch when CRDs vanish or linger.)

## Delete blocks until Compositions are gone (by design)
If Compositions of the version still exist, Delete returns an error to requeue rather than force-
removing them (`compositiondefinitions.go:750-752`, and `ErrCompositionStillExist` at `:788`). A
`CompositionDefinition` stuck "Deleting" usually means live Compositions remain — delete those
first.

## The chart `version` field is `v<dots-as-dashes>`, and capped
The generated CRD version is `fmt.Sprintf("v%s", strings.ReplaceAll(chartVersion, ".", "-"))`
(`chart.go:178`) — e.g. chart `1.2.3` → CRD version `v1-2-3`. `spec.chart.version` is validated
`MaxLength=20` (`types.go:41`); long/odd version strings will be rejected or produce surprising CRD
version names.

## The chart MUST be a single-root tgz with a `values.schema.json`
`ChartInfoFromBytes` rejects archives whose top level isn't exactly one directory
(`chart.go:150-151`), and `ChartJsonSchema` opens `values.schema.json` directly — a chart without
that file fails CRD generation (`chart.go:183-191`). Charts must ship a JSON schema for their values.

## Chart-fetch retry classifies some errors as permanent
`ChartInfoFromSpec` retries up to 5 times, but treats 401/403/404/422/400 (and the apimachinery
equivalents) as **non-retryable** (`chart.go:100-137`, `chartRetryAttempts` `:33`). A bad
credential or a missing chart fails fast — it will not be masked by retries.

## Secret/Target watches drive credential rotation — don't expect only poll-based pickup
core-provider watches Secrets and KubernetesTargets and re-enqueues every affected
`CompositionDefinition` (`compositiondefinitions.go:125-128`). Rotating a chart-credential Secret or
repointing a `KubernetesTarget.spec.kubeconfigRef` triggers a reconcile promptly; clients are
rebuilt from the kubeconfig **every reconcile** (`clusterkube.Remote`, `clusterkube.go:47`), so
External-Secrets-style rotation is picked up without a restart. The kubeconfig Secret's
`resourceVersion` is recorded in `status.target` for traceability (`compositiondefinitions.go:335`).

## Idempotency depends on the digest — beware changing rendered templates
Whether a reconcile is a no-op is decided by comparing `status.digest` to the dry-run render digest
AND the live-lookup digest (`compositiondefinitions.go:479-497`). The digest hashes object
identity + spec (`deploy.go`, FNV hasher). Any change to the asset templates (or to fields the
hasher includes) flips every existing definition to "not up to date" and re-applies + restarts the
CDC Deployment (`deploy.go:458-485`) — which can churn running controllers fleet-wide. Treat
template changes as a fleet-wide reconcile event.

## No webhook server / no serving cert — don't look for one
Unlike pre-2.0.0, the manager starts with no webhook server and no serving certificate
(`main.go:104-118`). There is nothing on a webhook port; the only server is the `:8080` metrics
endpoint (`main.go:113`). Debugging "webhook not reachable" against 2.0.x is a category error.
