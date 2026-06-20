# Design: Composition status projection (declarative status from inputs + APIs)

> Status: **Draft for discussion** · Author: design exploration · Date: 2026-06-18
>
> Supersedes the retired issue *"Add status subresource to generated Composition CRD
> (health/readiness propagation)"*, which was deleted as mis-framed: the generated CRD
> already has a status subresource (§1.1), and the real need is **declarative status
> projection**, not a fixed health schema — "readiness" is just one expression you can write.
>
> Goal: let a `CompositionDefinition` declare extra `Composition` status fields and have the
> controller populate them each reconcile by **projecting/transforming** values — from the
> instance's own inputs (spec/helm, in-hand) and from **API calls resolved at runtime**
> (Kubernetes objects or external systems) — using the **snowplow/frontend convention**
> (`apiRef` + `${ jq }` templates via `plumbing/jqutil`) so composition status and frontend
> widgets speak one language. All repos are pinned to the **`braghettos`** forks (§9).

---

## 0. TL;DR

1. **The status subresource already exists** (§1.1); this is about making status
   **declarative and chart-customisable**, plus `observedGeneration`.
2. **Two source kinds, no more:**
   - **built-ins** — `self` (the Composition CR; `spec`/`status` are sugar) and `helm`
     (release metadata). In-hand, zero I/O, zero new RBAC.
   - **`apiRef` → RESTAction**, resolved **synchronously at reconcile under snowplow's own
     ServiceAccount**. Because the Kubernetes API server is just another HTTP endpoint, a
     RESTAction covers **both** in-cluster reads (a Service's LB IP, a Deployment's
     `readyReplicas`) **and** external APIs — so there is **no separate `resourcesRefs`**.
3. **Projection = the snowplow convention.** `spec.statusDataTemplate[]` of
   `{ forPath, expression }` with **`${ jq }`** evaluated over one **keyed root** of all
   sources, via `plumbing/jqutil` (`MaybeQuery`/`Eval`/`InferType` — already in production).
4. **The engine is pure and shared.** A `statusprojection` package (in
   `unstructured-runtime`, reusing `jqutil`) takes the CR + a `resolved` map of source data
   + the templates and writes `.status`. The CDC supplies `resolved` (built-ins in-hand;
   `apiRef` via a call to snowplow). RDC reuses the same engine for response→status.
5. **RESTAction resolution is a runtime query, not state** — it is contextual (per
   composition) and ephemeral, so it is **not** persisted on the RESTAction and there is
   **no RESTAction controller**. The only thing persisted is the **Composition's `.status`**
   (last-observed values, refreshed each poll).
6. **Deferred:** kstatus readiness rollup and Helm revision/state (§7).

---

## 1. Where things are today

### 1.1 core-provider already generates a status schema (it is not empty)

- The generated CRD's status comes from a static schema
  (`internal/tools/crd/generation/statics/status.schema.json`): `helmChartUrl`,
  `helmChartVersion`, `digest`, `previousDigest`, `managed[]`.
- `internal/tools/crd/generation/generation.go` stamps it onto **every served version**
  (`UpdateStatus` per-version loop); the CRD has a status subresource.
- It is **hard-coded and identical for every Composition type** — there is no
  `CompositionDefinition` field to extend it (`spec` has only `Chart` and `Deploy`).

### 1.2 The CDC already writes status — but only fixed fields

`composition-dynamic-controller` reconciles via `unstructured-runtime`'s
`controller.ExternalClient` (`internal/composition/composition.go`,
`internal/composition/support.go`). It writes `helmChartUrl`/`helmChartVersion`,
`managed[]`, and `Ready`/`Synced` conditions (`unstructuredtools.SetConditions` +
`tools.UpdateStatus`). Gaps: **no `observedGeneration`**, **no Helm revision/state**, and
**no real readiness rollup** (`Available()` is set unconditionally on reconcile success).

### 1.3 `unstructured-runtime` already owns the runtime status seam

Used by **both** the CDC and the RDC, it provides `SetConditions`/`GetConditions`/
`UpdateStatus`/`IsAvailable`, the `Ready`/`Synced` vocabulary, and `SetFailedObjectRef`. It
does **not** generate CRD schemas and has **no** field-projection engine or
`observedGeneration` helper, and does not import `kstatus`. The CDC's `main` already pins
`unstructured-runtime v1.1.0`, so adopting a new package is a routine tag bump.

### 1.4 Prior art: `oasgen-provider` / RDC `additionalStatusFields`

- `RestDefinition.spec.resource` has `Identifiers []string` and `AdditionalStatusFields
  []string`; `oas2jsonschema` builds the status schema from the OpenAPI response schema.
- At runtime RDC's `populateStatusFields` copies response fields into `status.<field>`.
- Two in-flight `braghettos/rest-dynamic-controller` branches overlap this design:
  **#40** plumbs `AdditionalStatusFields` into the populate loop; **#42** adds type-safe
  conversion (int/float/bool). Both are **bespoke and limited** — top-level only,
  copy-only, response-only. The shared jq engine here **subsumes** them (jq gives nested
  paths, transforms, and native type preservation for free — §3.1).

---

## 2. Problem & goals

**Problem.** A Composition's status is fixed and chart-agnostic. A chart author cannot
surface instance-meaningful values — an endpoint, an ID, a derived URL, a rolled-up
replica count, an external system's health — where `kubectl`, the Portal, dependent
compositions, and GitOps can read them.

**Goals.**

- **G1.** A `CompositionDefinition` declares extra status fields on the generated CRD.
- **G2.** They are populated each reconcile by projecting/transforming from named sources:
  `self`/`helm` (in-hand) and `apiRef`/RESTAction (in-cluster objects or external APIs,
  resolved at runtime).
- **G3.** The runtime engine is **shared** (`unstructured-runtime`, over `plumbing/jqutil`)
  and uses the **snowplow convention**, so the CDC, RDC, and the frontend BFF speak one
  templating language.
- **G4.** Populate `status.observedGeneration`.
- **G5.** Backward compatible: no declarations ⇒ status identical to today.

**Non-goals.** kstatus readiness rollup and Helm revision/state (Phase 2, §7); changing the
conversion/`vacuum` storage machinery; a RESTAction status controller (§3.3 — wrong
primitive).

---

## 3. The model

**A source is data the engine evaluates jq against, keyed by name in one combined root.**
There are exactly two kinds:

| Kind | Resolves to | I/O / RBAC | Phase |
|---|---|---|---|
| **built-in** `self` (`spec`/`status` sugar), `helm` | the Composition CR / its Helm release metadata — already in the CDC's hand | none | 1 |
| **`apiRef` → RESTAction** | result of API calls resolved **at runtime under snowplow's own SA** — kube API reads *or* external APIs | snowplow's SA; **CDC needs none** | 2 |
| `response` (RDC only) | the API GET/FINDBY body RDC already has | none | — |

There is deliberately **no `resourcesRefs`**: the kube API server is an HTTP endpoint, so a
RESTAction whose `endpointRef` targets `https://kubernetes.default.svc` reads in-cluster
objects (`GET`/`LIST ?labelSelector=…`) just as it calls any external API. One mechanism
covers both; the only thing it gives up vs. native informers is event-driven freshness (see
§3.3).

A **Mapping** (snowplow `widgetDataTemplate` shape) is `{ forPath, expression }`: `forPath`
is the dotted path under `.status` to write; `expression` is a `${ jq }` program over the
combined root. A bare path is a copy; jq does transforms, aggregation, and object/array
construction (§4.2). Example roots: `.self.spec.…`, `.helm.…`, `.api.<callName>.…`.

### 3.1 Transform language — jq

`expression` is **jq**, via `plumbing/jqutil` (gojq). This is settled, not a trade study:
jq is **already the Krateo platform language** (snowplow, RestActions, platform-wide value
mapping), `plumbing/jqutil` is **already a core-provider dependency**, and it **preserves
types natively** (`jqutil.InferType`) — which also makes RDC #42's hand-rolled conversion
unnecessary. The `braghettos/plumbing v1.7.6` `jqutil` even carries the int/int32
gojq-panic fix (commit `28c9297`).

```
copy          .self.spec.service.host
derived URL   "https://\(.self.spec.service.host):\(.self.spec.service.port)"
rollup        [ .api.deploys.items[].status.readyReplicas // 0 ] | add
```

CEL and `sprig` were considered and rejected: CEL would add a new dependency and fork the
platform off its established jq convention; `sprig` is stringly-typed.

### 3.2 This is the snowplow convention, reused

The frontend BFF (**snowplow**) already does exactly this for widgets
(`apis/templates/v1`, `internal/resolvers/widgets/…`):

- **`apiRef`** → a `RESTAction` (named HTTP calls, `dependsOn` chains, `${ jq }` payloads),
  resolved and **keyed by call name**.
- **`widgetDataTemplate[]`** → `{ forPath, expression }` with `${ jq }`, written into a base
  structure. The resolver is literally our engine: `jqutil.MaybeQuery` → `jqutil.Eval` →
  `jqutil.InferType`.

We adopt `apiRef` + `widgetDataTemplate` verbatim (we **drop** snowplow's `resourcesRefs` —
§3). The biggest reuse lever is to **lift the shared types + the jqutil-based resolver into
`plumbing`** so snowplow, the CDC, and RDC consume one implementation (§9/§11).

### 3.3 Why there is no RESTAction controller (and what's persisted)

A RESTAction's resolved data is a **runtime query result**: it reflects external/cluster
state at call time and is **contextual** — the same RESTAction resolved for different
compositions (via templated paths/payloads) yields different data. There is therefore no
single "status" of a RESTAction to persist, and a controller writing `RESTAction.status`
would be meaningless. (`RESTAction.status` exists in the type but nothing populates it, and
nothing should.)

So resolution is **synchronous, on demand, per reconcile**. The **only persisted output is
the Composition's `.status`** — last-observed values, refreshed on the CDC's poll. The
freshness model is therefore **poll-based, not watch-based**: a late-arriving LB IP appears
on the next reconcile. This is a feature at fleet scale — it avoids the
N-compositions × M-objects informer fan-out that event-driven freshness would cost; you
trade instant updates for bounded, predictable staleness, which is the right trade for a
status field.

---

## 4. API

```go
// apis/compositiondefinitions/v1alpha1/types.go
type CompositionDefinitionSpec struct {
    Chart  *ChartInfo        `json:"chart,omitempty"`
    Deploy *DeploymentTarget `json:"deploy,omitempty"`

    // ApiRef references a RESTAction whose calls are resolved (under snowplow's SA, §4.3)
    // each reconcile; results are keyed by call name under ".api" in the jq root.
    // Shape copied from snowplow's shipped spec.apiRef (name/namespace + inline extras).
    // +optional
    ApiRef *ApiReference `json:"apiRef,omitempty"`

    // StatusDataTemplate declares the projected status fields (snowplow widgetDataTemplate
    // shape). Each is evaluated over the combined source root and written under .status.
    // +optional
    // +listType=map
    // +listMapKey=forPath
    StatusDataTemplate []StatusFieldMapping `json:"statusDataTemplate,omitempty"`
}

type StatusFieldMapping struct {
    // ForPath is the dotted path under .status to write, e.g. "endpoint" or "network.host".
    // +required
    ForPath string `json:"forPath"`

    // Expression is a ${ jq } program over the combined root (.self/.spec/.status/.helm/.api).
    // A bare path is the trivial copy.
    // +required
    Expression string `json:"expression"`

    // Type optionally pins the generated property's scalar JSON-schema type. For complex
    // outputs use Schema or PreserveUnknownFields (§4.2/§5).
    // +optional
    Type string `json:"type,omitempty"`

    // Schema optionally supplies the full JSON-schema for a complex output (object/array).
    // +optional
    Schema *apiextensionsv1.JSONSchemaProps `json:"schema,omitempty"`

    // PreserveUnknownFields generates the node with x-kubernetes-preserve-unknown-fields:true
    // for dynamic shapes. Mutually exclusive with Type/Schema.
    // +optional
    PreserveUnknownFields bool `json:"preserveUnknownFields,omitempty"`
}

// ApiReference copies snowplow's shipped spec.apiRef shape (inline-extras design P):
// name/namespace of the RESTAction + an inline, free-form `extras` map.
type ApiReference struct {
    // +required
    Name string `json:"name"`
    // +required
    Namespace string `json:"namespace"`

    // Extras are author-declared STATIC values merged into the RESTAction's jq root (snowplow
    // spec.apiRef.extras, read via NestedMap(obj,"spec","apiRef","extras")). Free-form — the
    // generated CRD marks this x-kubernetes-preserve-unknown-fields. They are INPUT-ONLY
    // (never surface in status). The CDC additionally injects per-instance context as the
    // "request extras" equivalent, which MERGE OVER these inline values (request-wins, §4.3).
    // +optional
    // +kubebuilder:pruning:PreserveUnknownFields
    Extras *apiextensionsv1.JSON `json:"extras,omitempty"`
}
```

### 4.1 Worked example — built-ins (Phase 1, no I/O)

`source` is implicit in the jq root (`.self`, `.helm`). Given a chart whose values are the
Composition `spec`:

```yaml
kind: CompositionDefinition
spec:
  chart: { url: oci://…/fireworksapp, version: 1.2.0 }
  statusDataTemplate:
    - forPath: url
      expression: ${ "https://\(.self.spec.service.host):\(.self.spec.service.port)" }
    - forPath: chartVersion
      expression: ${ .helm.version }
    - forPath: ready                  # "readiness" as just an expression
      expression: ${ .helm.status == "deployed" }
      type: boolean
```

For instance `spec: { service: { host: demo, port: 8080 } }` → the CDC writes
`status: { url: "https://demo:8080", chartVersion: "1.2.0", ready: true, observedGeneration: 1 }`
alongside the existing baseline fields (`helmChartUrl`, `managed[]`, conditions).

### 4.2 Complex status structures (objects, arrays, arrays of objects)

jq constructs arbitrary JSON and the engine writes the whole value at `forPath`:

```yaml
statusDataTemplate:
  - forPath: network                                   # object
    expression: ${ { host: .self.spec.service.host, port: .self.spec.service.port } }
    schema: { type: object, properties: { host: {type: string}, port: {type: integer} } }
  - forPath: endpoints                                 # array of objects
    expression: ${ [ .self.spec.ingress[] | { host: .host, tls: (.tls // false) } ] }
    schema: { type: array, items: { type: object, properties: { host: {type: string}, tls: {type: boolean} } } }
  - forPath: raw
    expression: ${ .self.spec.dynamic }
    preserveUnknownFields: true
```

Rules: **`forPath` addresses object locations only** (build arrays by *returning* `[ … ]`);
**one value per mapping** (wrap streams in `[ … ]`); and **output is normalized** to
`DeepCopyJSONValue`-safe types before `SetNestedField` (which accepts only
`map`/`slice`/`string`/`int64`/`float64`/`bool`/`nil` and **panics** on plain `int`). That
normalization is exactly what `jqutil.InferType` already does (and `braghettos/plumbing
v1.7.6` carries the gojq int-panic fix) — so the engine inherits it from `jqutil`.

### 4.3 `apiRef` → RESTAction, resolved synchronously via snowplow (own SA)

In-cluster and external data both come from a `RESTAction` referenced by `apiRef`. A
`RESTAction.spec.api[]` is a list of named calls; each `endpointRef` points at a **Secret**
holding the endpoint URL (and credentials). The kube-API case just points `endpointRef` at
the in-cluster API:

```yaml
# RESTAction (lives where it is resolved — see placement note)
apiVersion: templates.krateo.io/v1
kind: RESTAction
metadata: { name: status-sources, namespace: demo-system }
spec:
  api:
    - name: svc                                         # in-cluster read via the kube API
      path: /api/v1/namespaces/demo/services/web
      endpointRef: { name: kube-local, namespace: demo-system }
    - name: health                                      # external API
      path: /healthz
      endpointRef: { name: backend-endpoint, namespace: demo-system }
---
spec:                                                   # the CompositionDefinition
  apiRef: { name: status-sources, namespace: demo-system }
  statusDataTemplate:
    - forPath: endpoint
      expression: ${ .api.svc.status.loadBalancer.ingress[0].ip }
    - forPath: backendUp
      expression: ${ .api.health.status == "UP" }
      type: boolean
```

**Resolution is delegated to snowplow, under snowplow's own ServiceAccount.** The CDC does
**not** execute RESTActions and **holds no endpoint credentials**. Each reconcile the CDC
asks snowplow to resolve the referenced RESTAction *with this composition's context*, gets
JSON back, and feeds it to the engine as the `api` source. This requires a **new
service-to-service entrypoint in snowplow**:

- runs under `rest.InClusterConfig()` (snowplow's SA), **not** the user-scoped `/call`
  path (which forces `UserConfig`, per-user identity — there is no "user" for a backend
  caller); the execution machinery (`restactions` resolver, `SArc`) already exists,
- accepts **caller context** via the resolver's existing **`Extras map[string]any`** hook
  (`apiref.Resolve` → `restactions.Resolve`): `Extras` seeds the jq root that each call's
  `path`/`payload`/`headers`/`filter` is evaluated against (`restactions/api/resolve.go:80`).

  **Extras have two layers, copying snowplow's shipped "inline-extras design P":**

  1. **Inline `apiRef.extras`** — author-declared **static** values on the
     `CompositionDefinition` (snowplow `spec.apiRef.extras`; a free-form,
     preserve-unknown-fields map). For fixed config: `region: eu-west`, a constant
     namespace, a tier. **Input-only** (never surface in status).
  2. **CDC-injected per-instance context** — the CDC supplies the dynamic identity of the
     reconciled Composition (`compositionId`, `namespace`, `name`, `spec`) as the
     **request-extras** equivalent (snowplow's `?extras=` query param). These **merge over**
     the inline map (**request-wins**, exactly snowplow's merge order).

  The combined map seeds the RESTAction's jq root, so calls read both — a **label-scoped
  kube-API LIST** uses the CDC-supplied identity with no hard-coded (chart-templated) names,
  while the author's static `region` is also available:

  ```yaml
  # CompositionDefinition — inline static extras nested in apiRef:
  apiRef:
    name: fireworksapp-status
    namespace: krateo-system
    extras:
      region: eu-west                      # static author default
  # in the RESTAction, calls read the merged root (.region inline + .compositionId/.namespace from the CDC):
  api:
    - name: deploys
      path: ${ "/apis/apps/v1/namespaces/\(.namespace)/deployments?labelSelector=krateo.io/composition-id=\(.compositionId)" }
      endpointRef: { name: kube-local, namespace: krateo-system }
  ```

  (`apiref.Resolve` returns the in-memory `ra.Status` — it persists nothing, consistent
  with §3.3. The CDC injecting instance identity mirrors how snowplow's frontend passes
  per-request context via `?extras=`.)

**Two consequences to design for:**

- **Security surface.** Resolving under snowplow's own (potentially broad) SA bypasses
  per-user RBAC by design. So a composition author's RESTAction is executed with snowplow's
  privileges. Bound this: constrain what snowplow's SA can reach and/or which RESTActions
  are resolvable in service mode. **This is the real risk to design — more than RBAC
  plumbing.** Note the upside: credentials live in **one** SA (snowplow), never in the N
  CDC instances across N target clusters.
- **Coupling & freshness.** This is a hard runtime dependency: CDC → snowplow on every
  reconcile with an `apiRef`. If snowplow is unavailable, those status fields go stale —
  **degrade the field, never fail the reconcile** (per-mapping error →
  `Synced=False/ReconcileError`). Status reflects last successful resolution.

> **Multi-cluster placement (open).** The CDC may run in a remote target cluster. Decide
> whether snowplow resolution happens on the management cluster (CDC calls back) or whether
> a snowplow resolver + the RESTAction/Secrets must exist in the target. `apiRef` may be
> management-local initially. (§11)

> **Alternative (rejected for now).** The CDC could link snowplow's resolver **in-process**
> under its own SA — no network hop, but credentials return to every CDC. Rejected: keeping
> credentials out of the CDC is the whole point of delegating.

---

## 5. Schema generation (provider-local, core-provider)

The status subresource **prunes** unknown fields, so every `forPath` must exist in the
generated status schema. core-provider extends its static status schema from the
declarations:

- For each mapping add a property at `forPath` (building intermediate objects), typed in
  precedence: **`Schema`** → **`PreserveUnknownFields`** (`x-kubernetes-preserve-unknown-fields`)
  → **`Type`** → **simple-path inference** (a bare `.self.spec.a.b` copied from the chart's
  `values.schema.json`) → **`string`** fallback.
- Enforce **structural-schema** rules (objects need `properties`/`additionalProperties`/
  preserve-unknown; arrays need `items`; no bare untyped nodes).
- Feed the merged schema into the existing `crdgen.Generate(Options{StatusSchema:…})` path;
  the per-version `UpdateStatus` loop stamps it on all served versions (composes with the
  multi-version/`vacuum` machinery).

**Validation on `CompositionDefinition` reconcile:** reject overlaps with baseline fields
(`helmChartUrl`, `managed`, …), duplicate `forPath`s, and `${ … }` that fails `gojq.Parse`
— surfaced as a condition, not deferred to the CDC. A small JSON-schema path helper
(resolve/add-by-path) is worth sharing with oasgen's `oas2jsonschema`; candidate home
`plumbing`.

---

## 6. Runtime engine (shared, `unstructured-runtime`)

New package `pkg/tools/statusprojection` in `braghettos/unstructured-runtime`:

```go
// Mapping mirrors the snowplow widgetDataTemplate item (decoupled from any provider CRD).
type Mapping struct { ForPath, Expression string }

// Project evaluates each Mapping's ${ jq } over `resolved` and writes the typed, normalized
// result into cr's .status at ForPath. `resolved` is the combined source root: built-ins
// ("self"/"spec"/"status" from cr; "helm") are injected by the caller, and any I/O-bound
// source ("api" — RESTAction results from snowplow) is resolved by the CALLER and passed
// in. Project performs no client calls and no RESTAction execution. jq runs via
// plumbing/jqutil (MaybeQuery/Eval/InferType), compiled-and-cached per expression; results
// are normalized to DeepCopyJSONValue-safe types before writing.
func Project(cr *unstructured.Unstructured, resolved map[string]any, mappings []Mapping) error

// SetObservedGeneration writes status.observedGeneration = metadata.generation.
func SetObservedGeneration(cr *unstructured.Unstructured)
```

Properties: pure (no I/O); nested `forPath` + arbitrary jq navigation; type-preserving via
`jqutil.InferType`; transforms/aggregation/complex-shape construction built in; per-mapping
errors aggregated (a bad mapping degrades that field, never the whole reconcile). RDC reuses
`Project` with `resolved["response"] = body`, retiring its bespoke `populateStatusFields`
(#40/#42).

### 6.1 CDC wiring

In the CDC's `Observe`/`Create`/`Update`, after the existing baseline writes:

```go
resolved := map[string]any{
    "helm": map[string]any{"revision": rel.Version, "name": rel.Name,
                           "version": pkg.Version, "status": rel.Info.Status.String()},
}
if cd.Spec.ApiRef != nil {                                  // Phase 2
    // request-extras = per-instance context, merged OVER the inline apiRef.extras by snowplow
    reqExtras := map[string]any{
        "compositionId": mg.GetUID(), "namespace": mg.GetNamespace(),
        "name": mg.GetName(), "spec": mg.Object["spec"],
    }
    api, err := snowplow.Resolve(ctx, cd.Spec.ApiRef, reqExtras)  // own-SA call; inline extras come from the RESTAction-side apiRef
    if err == nil { resolved["api"] = api } else { /* degrade, set ReconcileError */ }
}
_ = statusprojection.Project(mg, resolved, mappings)        // "self"/"spec"/"status" from mg
statusprojection.SetObservedGeneration(mg)
_, err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
```

`mappings` reach the (CompositionDefinition-decoupled) CDC via the **ConfigMap** core-provider
already renders for it (preferred over CRD annotations), extended with the serialized
`statusDataTemplate` + `apiRef`.

---

## 7. Deferred (Phase 2+)

- **kstatus readiness rollup.** A real `Ready` derived from managed-object health. With the
  RESTAction model this is expressible as a RESTAction that `LIST`s the managed objects via
  the kube API and a jq rollup — or a dedicated `pkg/tools/readiness` helper over kstatus
  (`sigs.k8s.io/cli-utils`). Decide which; both are post-Phase-1.
- **Helm metadata as baseline.** Surface `release.revision/state/lastDeployed` (already on
  the CDC's release object) as baseline status fields, so they exist without any
  declaration.

---

## 8. Phased plan

1. **Phase 0 — engine.** `pkg/tools/statusprojection` (+ `SetObservedGeneration`) in
   `braghettos/unstructured-runtime` over `plumbing/jqutil`; unit tests; tag.
2. **Phase 1a — core-provider API + schema.** Add `statusDataTemplate` (+ `Schema`/
   `PreserveUnknownFields`) to `CompositionDefinition`; generate status properties; validate;
   ship mappings to the CDC via the ConfigMap. (`apiRef` accepted but inert until 1c.)
3. **Phase 1b — CDC populate (built-ins).** Bump `unstructured-runtime`; call `Project` +
   `SetObservedGeneration` with `self`/`helm`; e2e on scalar/derived/object/array fields.
   No new I/O, no new RBAC.
4. **Phase 1c — RDC convergence.** Replace RDC `populateStatusFields` with `Project`
   (`resolved["response"]=body`); coordinate with branches #40/#42 (jq makes #42 moot).
5. **Phase 2 — `apiRef` via snowplow.** Add snowplow's own-SA service-mode resolve
   entrypoint; CDC calls it and projects `.api`; design the security surface + multi-cluster
   placement. Optionally express readiness rollup as a kube-API RESTAction.
6. **Phase 3 — readiness/Helm baseline** (§7).

Phase 0+1 deliver the headline capability (spec/helm-derived status) with no I/O, no new
watches, no new RBAC. Phase 2 unlocks in-cluster and external values through the single
RESTAction mechanism.

---

## 9. Repository / fork impact

All work on the **`braghettos`** forks (origin), `krateoplatformops` = upstream; plumbing is
synced with upstream and all forks pin `braghettos/plumbing v1.7.6` (§ alignment audit
2026-06-18).

| Repo | Role |
|---|---|
| core-provider | `statusDataTemplate`/`apiRef` API; status-schema generation; ship mappings to CDC; CDC↔snowplow wiring config |
| composition-dynamic-controller | call `Project`; resolve `apiRef` via snowplow; write `.status` |
| unstructured-runtime | `pkg/tools/statusprojection` engine (over `jqutil`) |
| snowplow | **new own-SA service-mode RESTAction resolve entrypoint** (reuse `restactions` resolver + `SArc`); candidate home for the shared `apiRef`/`widgetDataTemplate` types + resolver to lift into `plumbing` |
| rest-dynamic-controller | adopt `Project` for response→status (#40/#42) |
| plumbing | `jqutil` (engine dep); candidate home for shared types + resolver + schema-path helper |

> **Shared-code lever (§3.2).** Lifting snowplow's `apiRef`/`widgetDataTemplate` types and
> the jqutil resolver into `plumbing` lets snowplow/CDC/RDC share one implementation
> (reconcile the snowplow `plumbing v0.6.2` vs forks' `v1.7.6` skew).

---

## 10. Alternatives considered

- **Keep `resourcesRefs` (snowplow's direct-K8s-read source).** Redundant: a kube-API
  RESTAction reads in-cluster objects too. Dropping it gives one mechanism and a smaller
  shared-types lift; cost is poll-only freshness (§3.3) — acceptable, even preferable at
  scale.
- **A RESTAction controller writing `RESTAction.status`.** Wrong primitive (§3.3): resolved
  data is a contextual runtime query, not persistable state.
- **CDC executes RESTActions in-process.** No new snowplow endpoint, but credentials return
  to every CDC instance/cluster — rejected.
- **CEL / sprig instead of jq.** Rejected — jq is the settled platform language (§3.1).
- **Local-only engine (no `unstructured-runtime`).** Loses the RDC/snowplow convergence —
  rejected.

---

## 11. Open questions

- **Snowplow service-mode entrypoint** (§4.3): the own-SA resolve API shape (context is
  already handled — pass it via the resolver's existing `Extras` hook); and the **security
  bounding** of what it may resolve/reach under snowplow's SA.
- **Multi-cluster `apiRef` placement** (§4.3): resolve on management cluster vs. project a
  resolver + RESTAction/Secrets into the target.
- **Shared-types home** (§9): lift snowplow's types + resolver into `plumbing` vs. duplicate;
  reconcile the plumbing version skew.
- **Engine dependency boundary**: `statusprojection` imports `plumbing/jqutil` directly
  (simplest) vs. takes an injected evaluator so `unstructured-runtime` doesn't pull a
  `plumbing` dependency.
- **RDC sequencing**: land the shared engine before or after #40/#42 merge.
- **Versioning interaction**: behaviour of projected fields across the full/parallel/
  selective migration patterns (mappings are per-`CompositionDefinition`-version → ride the
  existing per-version status stamping; needs an explicit test).
- **Extras shape — RESOLVED (snowplow shipped, 2026-06-20).** Copied snowplow's
  "inline-extras design P": `apiRef.extras` is a free-form, preserve-unknown-fields map of
  **static** author values (input-only); the CDC injects per-instance context
  (`compositionId`/`namespace`/…) as **request-extras** that merge over inline (request-wins).
  Remaining sub-question: the exact set of instance-context keys the CDC auto-injects (and
  whether `.spec`/`.helm` are included).

---

## 12. Worked example (end to end)

Exercises every part: built-ins (`self`/`helm`), author-declared `extras`, an `apiRef`
RESTAction doing `Extras`-driven label-scoped **kube-API** reads + an **external** call, and
the full range of `statusDataTemplate` expressions (copy / derived / aggregate / complex /
readiness).

**(1) `CompositionDefinition`:**

```yaml
apiVersion: core.krateo.io/v1alpha1
kind: CompositionDefinition
metadata: { name: fireworksapp, namespace: krateo-system }
spec:
  chart: { url: oci://registry.krateo.io/charts/fireworksapp, version: 1.2.0 }

  apiRef:                                       # snowplow shape: name/namespace + inline extras
    name: fireworksapp-status
    namespace: krateo-system
    extras:                                     # author-declared STATIC values (input-only)
      region: eu-west
  # the CDC also injects { compositionId, namespace, name, spec } as request-extras,
  # merged OVER apiRef.extras (request-wins) — the RESTAction reads both

  statusDataTemplate:
    # built-ins (.self/.helm) — Phase 1, no I/O
    - forPath: url
      expression: ${ "https://\(.self.spec.service.host):\(.self.spec.service.port)" }
    - forPath: chartVersion
      expression: ${ .helm.version }
    # managed objects via the kube-API RESTAction calls (.api.*) — Phase 2
    - forPath: endpoint
      expression: ${ .api.svc.items[0].status.loadBalancer.ingress[0].ip }
      type: string
    - forPath: readyReplicas
      expression: ${ [ .api.deploys.items[].status.readyReplicas // 0 ] | add }
      type: integer
    - forPath: desiredReplicas
      expression: ${ [ .api.deploys.items[].spec.replicas // 0 ] | add }
      type: integer
    # external API (.api.health)
    - forPath: backendUp
      expression: ${ .api.health.status == "UP" }
      type: boolean
    # complex (array of objects) built with jq
    - forPath: endpoints
      expression: ${ [ .api.svc.items[] | { name: .metadata.name, ip: (.status.loadBalancer.ingress[0].ip // null) } ] }
      schema:
        type: array
        items: { type: object, properties: { name: {type: string}, ip: {type: string} } }
    # readiness as an expression — no kstatus needed for the common case
    - forPath: ready
      expression: ${ (.helm.status == "deployed") and ([ .api.deploys.items[] | .status.readyReplicas == .spec.replicas ] | all) }
      type: boolean
```

**(2) The referenced `RESTAction` (+ endpoint Secrets):**

```yaml
apiVersion: templates.krateo.io/v1
kind: RESTAction
metadata: { name: fireworksapp-status, namespace: krateo-system }
spec:
  api:
    - name: svc                                 # kube API is just an HTTP endpoint
      path: ${ "/api/v1/namespaces/\(.namespace)/services?labelSelector=krateo.io/composition-id=\(.compositionId)" }
      endpointRef: { name: kube-local, namespace: krateo-system }
    - name: deploys
      path: ${ "/apis/apps/v1/namespaces/\(.namespace)/deployments?labelSelector=krateo.io/composition-id=\(.compositionId)" }
      endpointRef: { name: kube-local, namespace: krateo-system }
    - name: health                              # external API
      path: /healthz
      endpointRef: { name: backend-endpoint, namespace: krateo-system }
---
apiVersion: v1
kind: Secret
metadata: { name: kube-local, namespace: krateo-system }
stringData: { server-url: https://kubernetes.default.svc, token: "<snowplow-SA token: get/list services+deployments>" }
---
apiVersion: v1
kind: Secret
metadata: { name: backend-endpoint, namespace: krateo-system }
stringData: { server-url: https://api.backend.internal, token: "<backend api token>" }
```

**(3) A `Composition` instance (what a user applies):**

```yaml
apiVersion: composition.krateo.io/v1-2-0
kind: FireworksApp
metadata: { name: demo, namespace: apps }
spec:                                           # == chart values (== .self.spec)
  service: { host: demo.example.com, port: 8080 }
  replicas: 3
```

**(4) Resolution at reconcile.** The CDC injects per-instance request-extras
`{ compositionId: "9b1c-…", namespace: "apps", name: "demo", spec: {…} }`, which snowplow
merges over the inline `apiRef.extras` (`{ region: "eu-west" }`) → the RESTAction's jq root
`{ region, compositionId, namespace, name, spec }`; each call then merges its response in by
name. Resolution runs under snowplow's own SA. The combined `resolved` root the engine sees:

```jsonc
{ "self": { "spec": { "service": { "host": "demo.example.com", "port": 8080 }, "replicas": 3 } },
  "helm": { "version": "1.2.0", "status": "deployed", "revision": 1 },
  "api":  { "svc":     { "items": [ { "metadata": { "name": "demo-web" }, "status": { "loadBalancer": { "ingress": [ { "ip": "34.120.55.10" } ] } } } ] },
            "deploys": { "items": [ { "spec": { "replicas": 3 }, "status": { "readyReplicas": 3 } } ] },
            "health":  { "status": "UP" } } }
```

**(5) Resulting `Composition` `.status`:**

```yaml
status:
  url: https://demo.example.com:8080
  chartVersion: 1.2.0
  endpoint: 34.120.55.10
  readyReplicas: 3
  desiredReplicas: 3
  backendUp: true
  endpoints: [ { name: demo-web, ip: 34.120.55.10 } ]
  ready: true
  observedGeneration: 1                         # set by SetObservedGeneration
  # baseline fields the CDC already writes today:
  helmChartUrl: oci://registry.krateo.io/charts/fireworksapp
  helmChartVersion: 1.2.0
  managed: [ … ]
  conditions: [ { type: Ready, status: "True", … }, { type: Synced, status: "True", … } ]
```

**(6) Generated CRD status schema (added from `statusDataTemplate`):**

```yaml
status:
  type: object
  properties:
    url:             { type: string }
    chartVersion:    { type: string }
    endpoint:        { type: string }
    readyReplicas:   { type: integer }
    desiredReplicas: { type: integer }
    backendUp:       { type: boolean }
    endpoints:       { type: array, items: { type: object, properties: { name: {type: string}, ip: {type: string} } } }
    ready:           { type: boolean }
    # + baseline helmChartUrl/helmChartVersion/managed/conditions/observedGeneration
```

Notes: **Phase 1 alone** (drop `apiRef`/`extras` and the `.api.*` fields) already yields
`url`, `chartVersion`, `ready`-from-helm — cheap, no I/O, no new RBAC. The only credential
holder is **snowplow's SA** (`kube-local`/`backend-endpoint` tokens); the CDC just calls
snowplow and projects. `endpoints`/`readyReplicas`/`ready` show jq doing construction,
aggregation, and rollup with no separate readiness machinery.
