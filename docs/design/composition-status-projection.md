# Design: Composition status projection (input/response → status field transforms)

> Status: **Draft for discussion** · Author: design exploration · Date: 2026-06-18
>
> Tracks: [krateo-core-provider#14](https://github.com/braghettos/krateo-core-provider/issues/14)
> — "Add status subresource to generated Composition CRD (health/readiness propagation)".
>
> Goal: let a `CompositionDefinition` declare **additional status fields** on the
> generated `Composition` CRD, and have the controller **populate them at reconcile
> time by projecting/transforming values** from the instance's own inputs (spec /
> chart values) — generalising the `additionalStatusFields` mechanism that
> `oasgen-provider` already applies to API responses, and lifting the shared runtime
> machinery into **`unstructured-runtime`** so the CDC and the rest-dynamic-controller
> (RDC) use one engine.

---

## 0. TL;DR of the recommendation

1. **The status subresource already exists** on generated Composition CRDs; this work
   is *not* about adding one. It is about making the status **declarative and
   chart-customisable**, plus closing small gaps (`observedGeneration`).
2. **Reuse the snowplow/frontend convention (§3.2, recommended):** declare data sources as
   **`resourcesRefs`** (GVR refs with an `id`) and the projection as a **`*DataTemplate`**
   list of `{ forPath, expression }` with **`${ jq }`** — the exact shape and engine
   (`plumbing/jqutil`, incl. `MaybeQuery`/`InferType`) the frontend BFF already uses. So
   composition status and frontend widgets speak one templating language. (§4 shows an
   earlier bespoke `additionalStatusFields` framing for reference.) core-provider injects
   matching properties into the generated CRD's status schema (so they survive subresource
   pruning); the CDC fills them each reconcile (worked example §4.1).
3. Put the **generic projection engine in `unstructured-runtime`** (a new
   `pkg/tools/statusprojection` package). It takes a CR, one or more **named sources**
   (the CR's own `spec`; the Helm metadata; and — crucially — the **live managed
   resources** the composition created, for extraction/aggregation; or, in RDC, the API
   response), a list of mappings, and writes `status`. The engine is pure; the CDC does any
   I/O (fetching managed objects) and passes them in. Both the CDC and RDC consume it;
   `oasgen-provider`'s `additionalStatusFields` becomes the "source = response" case.
   Objects, arrays and arrays-of-objects are all supported (§4.2); `managed`-derived values
   and aggregations are §4.3; and — fully snowplow-aligned — **external APIs via
   `apiRef`/RESTAction** are §4.4 (reusing snowplow's executor). One keyed jq root covers
   `self`/`helm`, in-cluster GVRs, and external APIs alike.
   - **Transform language: two proposals documented — jq vs CEL (§3.1), with examples.**
     Recommendation is **jq** (`plumbing/jqutil`): already a Krateo dependency, platform
     convention, and native type preservation that subsumes RDC's in-flight #42 work.
4. **Readiness rollup (kstatus) and Helm revision/state are deferred** to a Phase 2 —
   they are orthogonal to projection and much larger (they touch the CDC's
   managed-resource watching). This doc sketches them but does not commit them.

All repositories involved are pinned to the **`braghettos`** forks (see §9).

---

## 1. Where things are today

### 1.1 core-provider generates the status schema (it is not empty)

The issue's premise ("currently exposes no status") is **out of date**. core-provider
already ships a status schema and the CDC already writes to it.

- The generated CRD's status comes from a static schema:
  `internal/tools/crd/generation/statics/status.schema.json` — `helmChartUrl`,
  `helmChartVersion`, `digest`, `previousDigest`, and a `managed[]` array of installed
  resources (`apiVersion`, `resource`, `name`, `namespace`, `path`).
- `internal/tools/crd/generation/generation.go` applies this status schema to **every
  served version** (`UpdateStatus(...)`, the per-version loop) and the CRD carries a
  status subresource.
- The status schema is **hard-coded and identical for every Composition type**. There
  is **no** `CompositionDefinition` field to extend or customise it (cf.
  `apis/compositiondefinitions/v1alpha1/types.go`: `spec` has only `Chart` and
  `Deploy`).

### 1.2 The CDC already writes status — but only fixed fields

`composition-dynamic-controller` reconciles `Composition` instances through
`unstructured-runtime`'s `controller.ExternalClient` (`Observe/Create/Update/Delete`,
`internal/composition/composition.go`). It writes:

- `status.helmChartUrl`, `status.helmChartVersion`
- `status.managed[]` (parsed from the Helm release manifest)
- conditions `Ready` / `Synced` via `unstructured-runtime`'s `condition` package and
  `tools.UpdateStatus`.

Gaps vs. issue #14: **no `observedGeneration`**; **no Helm revision/state/deploy-time**;
**no real readiness rollup** (`condition.Available()` is set unconditionally on reconcile
success, not derived from the health of `managed[]`).

### 1.3 `unstructured-runtime` already owns the runtime status seam

`unstructured-runtime` (used by **both** the CDC and the RDC) provides
`SetConditions`, `GetConditions`, `UpdateStatus`, `IsAvailable`, the `Ready`/`Synced`
condition vocabulary, and `SetFailedObjectRef`. It does **not** generate CRD schemas and
has **no** field-projection/transform engine, and **no** `observedGeneration` helper. It
does not import `kstatus`.

> **No version skew.** The CDC's `main` already pins `unstructured-runtime v1.1.0`
> (`composition-dynamic-controller/go.mod`), which is exactly where the status helpers
> above live — the CDC uses `unstructuredtools.SetConditions` + `tools.UpdateStatus`
> today (`internal/composition/composition.go`, `internal/composition/support.go`). So
> adopting a new `statusprojection` package only requires a routine bump to the next
> `unstructured-runtime` tag, not a major-version jump.

### 1.4 Prior art: `oasgen-provider` / RDC `additionalStatusFields`

- `RestDefinition.spec.resource` has `Identifiers []string` and
  `AdditionalStatusFields []string`
  (`braghettos-oasgen-provider/apis/restdefinitions/v1alpha1/types.go`).
- `oas2jsonschema` (`internal/tools/oas2jsonschema/status_builder.go`) builds the
  generated CRD's **status schema** by resolving each declared path against the **OpenAPI
  GET/FINDBY response schema** (`composeStatusSchema`), falling back to `string` when a
  path is absent.
- At runtime, RDC's `populateStatusFields` (`internal/controllers/helpers.go`) copies
  values **from the API response by name** into `status.<field>`.

**RDC is actively closing its populate gap on the `braghettos` fork** — two in-flight
branches directly overlap this design (confirmed by reading them, 2026-06-18):

- `40-add-management-of-additionalstatusfield-of-restdefinition`
  (commit `c282e36`): adds `AdditionalStatusFields []string` to RDC's
  `definitiongetter.Resource` and makes `populateStatusFields` iterate **both**
  `Identifiers` and `AdditionalStatusFields` (merged into one set, single pass over the
  response `body`). This fixes "declared but never populated".
- `42-updates-for-the-management-of-type-safe-status-of-oasgen-provider`
  (builds on #40): replaces the blanket `text.GenericToString(v)` with a **type-preserving
  conversion** (int/float/bool via a type switch, with float handling to avoid CRD
  warnings, `Sprintf` fallback) — so a status field typed `integer` in the schema is
  written as a number, not a string.

Even with #40 + #42, the RDC loop is still **limited in exactly the ways this design must
beat**:

- **Top-level only.** It matches `body[k]` against field *names* and writes
  `status.<k>` (`unstructured.SetNestedField(..., "status", k)`). Nested paths like
  `metadata.id` → `status.network.host` are **not** supported at runtime, even though the
  oasgen *schema* side already resolves nested paths.
- **Copy-only.** No transform / derived values.
- **Single source.** Reads only the API response `body`; no spec→status (and obviously no
  Helm-release source).

**Takeaway:** the two providers share the *shape* of the feature (declare field paths →
generate status schema → copy at runtime), and RDC is now hand-rolling its second and
third iteration of the populate loop (#40 then #42). That is precisely the divergence to
stop: a shared engine should **subsume #40 + #42** — with jq (the recommended dialect,
§3.1) native type preservation makes #42's hand-rolled conversion intrinsic, while nested
paths, transforms, and multiple sources are added on top — so RDC adopts it instead of
growing a fourth bespoke variant. This design unifies the runtime half and adds
transforms.

---

## 2. Problem & goals

**Problem.** A Composition's status is fixed and chart-agnostic. A chart author cannot
surface instance-meaningful values (an endpoint, an ID, a derived URL, a tier label)
onto `.status` where `kubectl get`, the Portal, dependent compositions, and GitOps health
checks can read them. The only knobs are the five hard-coded fields.

**Goals.**

- **G1.** A `CompositionDefinition` can declare extra status fields on the Composition
  CRD it generates.
- **G2.** Those fields are populated each reconcile by **projecting/transforming** from
  named sources: the instance's `spec` (chart values) and Helm metadata (Phase 1), and the
  **live managed resources the composition created** — including **aggregations across
  them** (Phase 2, §4.3) — plus, in RDC, the API response.
- **G3.** The runtime projection/transform engine is **shared** (lives in
  `unstructured-runtime`), so the CDC and RDC use one implementation and `oasgen`'s
  `additionalStatusFields` is re-expressed as one case of it.
- **G4.** Populate `status.observedGeneration` (cheap, asked for by #14).
- **G5.** Backward compatible: no declarations ⇒ status identical to today.

**Non-goals (this phase).**

- **N1.** kstatus-based readiness rollup of `managed[]` into `Ready` (Phase 2, §7).
- **N2.** Helm release revision/state/deploy-time surfacing (Phase 2, §7).
- **N3.** Changing the conversion/`vacuum` storage-version machinery.
- **N4.** Reading the composition's **own** managed resources is in scope but deferred to
  Phase 2 (§4.3/§7). Out of scope entirely: lookups of **unrelated** objects, or objects
  in **other clusters**, as a projection source.

---

## 3. The unifying model

> **Core abstraction: a source is a GVR-referenced object (or set), and the Composition is
> just the `self` source.** Everything — reading a spec field, a managed Service's IP,
> summing over Deployments — is "run a jq program against the live content of some
> object(s) identified by a GVR". There are no special-cased sources; `spec`/`status` are
> simply views of the `self` GVR.

A status projection is a pure function:

```
project(cr, resolved: map[string]any, mappings: []Mapping) -> patch applied to cr .status
```

`resolved` maps a **source name** to the **already-fetched document** for that source — a
single object's content, or an array for a set/selector source. The provider (CDC/RDC)
resolves each declared source's GVR ref into `resolved` and hands it in; the engine itself
does **no** cluster I/O and just evaluates jq.

A **Source** identifies what to read:

```
Source {
  Name       string   // referenced by Mapping.source
  // a GVR reference:
  APIVersion string    // e.g. "v1", "apps/v1"
  Kind       string    // e.g. "Service", "Deployment"
  // exactly one selection mode:
  Self       bool             // the Composition CR itself (its own GVR) — no I/O, the CDC already holds it
  Name_      string           // a single object by name (often label-matched instead, since chart names are templated)
  Selector   *LabelSelector   // a SET -> the resolved doc is an array (enables aggregation)
}
```

and a **Mapping**:

```
Mapping {
  To         string   // dotted path under status to write, e.g. "endpoint" or "network.host"
  Source     string   // a source name; built-ins: "self" (== the Composition), "spec"/"status"
                       //   (sugar for self|.spec / self|.status), "helm"; default "spec"
  Expression string   // a jq program evaluated against the resolved source (plumbing/jqutil)
  Type       string   // OPTIONAL scalar type pin (complex shapes: Schema / PreserveUnknownFields)
}
```

A jq program subsumes "which field" and "how to transform it", so there is no
`from`/`transform` split:

- **plain copy** — `source: spec`, `expression: .service.host`;
- **derived** — `expression: "https://\(.service.host):\(.service.port)"`;
- **from another object** — `source: web` (a `Service` GVR), `expression: .status.loadBalancer.ingress[0].ip`;
- **aggregate over a set** — `source: deploys` (a selector), `expression: '[ .[].status.readyReplicas // 0 ] | add'`.

This is also exactly oasgen's `additionalStatusFields: [id, uuid]` re-expressed as
`[{to: id, expression: .id}, {to: uuid, expression: .uuid}]` with the API response as the
source.

### Built-in vs declared sources

| Source | Resolves to | I/O / cost | Phase |
|---|---|---|---|
| `self` (and `spec`/`status` sugar) | the Composition CR the CDC is already reconciling | none | 1 |
| `helm` | Helm release metadata the CDC holds (the one non-GVR, synthetic source) | none | 1 |
| **declared GVR source** (a named object or a selector set) | live managed object(s) the composition created | fetch + watch | 2 (§4.3/§7) |
| **`apiRef` → RESTAction** | external REST API response(s), keyed by call (snowplow `apiRef`) | execute REST + poll | 2/3 (§4.4) |
| `response` (RDC only) | the API GET/FINDBY body RDC already has | none | — |

The engine is identical across providers; only which sources are resolved differs. This is
the core of G3: response→status, spec→status, managed-object→status, and external-API→status
are all the **same operation over a different source object**.

### 3.1 Transform language — two proposals: jq vs CEL

`Expression` needs an evaluation language. Two are on the table; both are sandboxed
(no I/O, deterministic) and both reduce "copy" to a trivial program. They are presented
here side by side, with a recommendation at the end. (If we ever wanted both, the
`Mapping` could carry a `language: jq|cel` discriminator defaulting to the chosen one —
but a single dialect is strongly preferred for a coherent author experience.)

**Same three cases in each dialect** — sources are the named docs (`spec`, `helm`, …);
under jq the program runs *against* the selected source (so its root `.` is that source);
under CEL the sources are bound as variables (`spec`, `helm`, `self`):

| Case | **Proposal A — jq** (`plumbing/jqutil`, gojq) | **Proposal B — CEL** (`cel-go`) |
|---|---|---|
| plain copy of `spec.service.host` | `.service.host` | `spec.service.host` |
| derived URL | `"https://\(.service.host):\(.service.port)"` | `"https://" + spec.service.host + ":" + string(spec.service.port)` |
| numeric rollup (max) | `[.replicas, 1] \| max` | `spec.replicas > 1 ? spec.replicas : 1` |
| helm revision (source `helm`) | `.revision` | `helm.revision` |

**Proposal A — jq (`github.com/krateoplatformops/plumbing/jqutil`, `itchyny/gojq v0.12.17`)**

- **Ecosystem standard, already a dependency.** jq is *the* Krateo transform language
  (RestActions, snowplow, platform-wide value mapping), and `plumbing/jqutil` is **already
  imported by core-provider** (the `plumbing` module, pinned to the `braghettos` fork). No
  new dependency, no new language for authors.
- **Native type preservation.** gojq evaluates over `any` and keeps numbers/bools/objects
  as themselves — a field typed `integer` is written as a number with zero conversion
  code. This makes RDC branch #42's hand-rolled int/float/bool type-switch (§1.4)
  *unnecessary* rather than something to port.
- `jqutil` already wraps parse/compile/eval (`Eval`, `Extract`, `ForEach`, `MaybeQuery`).
- Cons: jq's static return type is unknowable, so the generated schema type needs `Type`
  or a string fallback (§5); jq syntax is terse for non-jq users.

**Proposal B — CEL (`github.com/google/cel-go`)**

- Same language as the in-apiserver `MutatingAdmissionPolicy` this repo adopted and as CRD
  validation rules — consistent with Kubernetes-native tooling; type-checkable at
  compile time (better schema-type inference than jq).
- Cons: a **new dependency** for core-provider/CDC and **not** the established Krateo
  transform convention (the platform and RDC already speak jq); weaker at list/data
  reshaping than jq.

**Recommendation: Proposal A (jq).** Reinforced by §3.2: jq via `plumbing/jqutil` is not
just *a* Krateo convention, it is the **exact mechanism the frontend/snowplow already use**
for the same job (shaping data into a target structure), down to the `${ }` wrapper and the
`jqutil.InferType` typing helper. CEL would fork the platform off its established
data-templating language. `sprig`/Go templates are a distant third (stringly-typed).

### 3.2 Recommended API shape — reuse the snowplow/frontend `resourcesRefs` + `*DataTemplate` convention

> **This is the recommended surface; §4's field names are an earlier framing kept for
> reference.** Krateo's frontend BFF (**snowplow**) already solves *exactly this problem* —
> "fetch some objects/APIs, then project/transform their data into a target structure" —
> with a battle-tested convention. We should reuse it verbatim so composition status and
> frontend widgets speak one language.

Snowplow widgets declare (`snowplow/apis/templates/v1`):

- **`resourcesRefs.items[]`** — a list of **GVR references each with an `id`**
  (`{id, apiVersion, resource, name, namespace, verb}`); snowplow fetches them and exposes
  each under its `id`.
- **`apiRef`** — a reference to a `RESTAction` (named API calls) whose responses are
  likewise keyed.
- **`widgetData`** — the base/default data structure.
- **`widgetDataTemplate[]`** — `{ forPath, expression }` where `expression` is a
  **`${ jq }`** program evaluated against the **combined keyed root** of all
  resourcesRefs/apiRef results, written into `widgetData` at `forPath`.

The resolver (`internal/resolvers/widgets/widgetdatatemplate/resolve.go`) is literally our
engine: `jqutil.MaybeQuery` (strip `${ }`) → `jqutil.Eval` → **`jqutil.InferType`** (typed
value). So the "normalization/type-inference" §4.2 calls for is **already implemented in
`plumbing` and in production**.

**Applied to composition status**, the `CompositionDefinition` would carry:

```yaml
spec:
  chart: { url: oci://…/fireworksapp, version: 1.2.0 }
  # GVR sources, snowplow shape — each id becomes a key in the jq root.
  # "self" (the Composition) and "helm" are implicit built-ins.
  resourcesRefs:
    items:
      - id: web
        apiVersion: v1
        resource: services
        selector: { app.kubernetes.io/name: web }   # label-scoped; names are templated
        verb: GET
      - id: deploys
        apiVersion: apps/v1
        resource: deployments
        selector: { krateo.io/composition-id: "{{ .metadata.uid }}" }
        verb: LIST                                   # a set -> array under .deploys
  # projection, snowplow widgetDataTemplate shape:
  statusDataTemplate:
    - forPath: url                                   # status.url
      expression: ${ "https://\(.self.spec.service.host):\(.self.spec.service.port)" }
    - forPath: endpoint                              # from the Service GVR
      expression: ${ .web.status.loadBalancer.ingress[0].ip }
    - forPath: readyReplicas                         # aggregate across the Deployment set
      expression: ${ [ .deploys[].status.readyReplicas // 0 ] | add }
```

**Mapping from §3/§4's framing to this convention:**

| §3/§4 (earlier framing) | snowplow convention (recommended) |
|---|---|
| `statusSources[]` (`name`+GVR) | **`resourcesRefs.items[]`** (`id`+GVR+`verb`) — same idea, existing schema |
| per-mapping `source` field | **dropped** — jq addresses each source by its `id` key in one combined root (`.web`, `.deploys`, `.self`, `.helm`) |
| `additionalStatusFields[].{to, expression}` | **`statusDataTemplate[].{forPath, expression}`** with `${ }`-wrapped jq |
| engine normalization/`Type` inference | **`jqutil.InferType`** (already exists) + optional `Schema`/`type` pin for complex shapes |

**Why this is the right call:**

- **One platform language.** A Krateo author who writes widget `widgetDataTemplate`s
  already knows how to write composition `statusDataTemplate`s — same `${ jq }`, same keyed
  root, same `resourcesRefs`.
- **Maximal reuse, minimal new code.** The schema types and the resolver already exist in
  `snowplow`/`plumbing`. The clean move is to **lift the shared types
  (`Reference`/`ResourceRef`/`*DataTemplate`) and the resolver into `plumbing`** (or a small
  shared module) so snowplow (frontend BFF), the CDC, and RDC all consume one
  implementation — the `unstructured-runtime` `statusprojection` engine becomes a thin
  adapter over it.
- **Validates every earlier decision** (jq, keyed multi-source root, GVR sources, type
  inference) by showing they already exist and interoperate in production.

The remaining design content (schema generation §5, the pure-engine/CDC-fetches split
§4.3/§6, phasing §8 — `self`/`helm` in Phase 1, `resourcesRefs` GVR fetch/watch in Phase 2)
is **unchanged**; only the *spelling* of the API moves to the snowplow convention.

---

## 4. API (earlier framing — see §3.2 for the recommended snowplow-convention shape)

```go
// apis/compositiondefinitions/v1alpha1/types.go
type CompositionDefinitionSpec struct {
    Chart  *ChartInfo        `json:"chart,omitempty"`
    Deploy *DeploymentTarget `json:"deploy,omitempty"`

    // StatusSources declares the object(s) status fields may be projected from, beyond the
    // built-in "self"/"spec"/"status"/"helm". Each is a GVR reference resolved to a live
    // object (by name/labels) or a set (selector). Resolving these requires fetching +
    // watching the referenced resources (Phase 2). Sources must stay within the
    // composition's own managed resources (RBAC; §4.3/§11).
    // +optional
    // +listType=map
    // +listMapKey=name
    StatusSources []StatusSource `json:"statusSources,omitempty"`

    // AdditionalStatusFields declares extra fields projected onto the generated
    // Composition CRD's status and populated by the controller each reconcile.
    // +optional
    // +listType=map
    // +listMapKey=to
    AdditionalStatusFields []StatusFieldMapping `json:"additionalStatusFields,omitempty"`
}

// StatusSource is a GVR reference the projection can read. The Composition itself is the
// built-in "self" source and needs no declaration.
type StatusSource struct {
    // Name is how mappings reference this source.
    // +required
    Name string `json:"name"`
    // APIVersion + Kind identify the GVR, e.g. {apiVersion: v1, kind: Service}.
    // +required
    APIVersion string `json:"apiVersion"`
    // +required
    Kind string `json:"kind"`
    // Selector matches a SET of objects (resolved doc is an array → aggregation). Prefer
    // this over a hard-coded name: chart-rendered names are templated, and the CDC already
    // labels managed resources (e.g. krateo.io/composition-id) to scope to this instance.
    // +optional
    Selector *metav1.LabelSelector `json:"selector,omitempty"`
    // Name optionally selects a single object by name (mutually exclusive with Selector).
    // +optional
    ResourceName string `json:"resourceName,omitempty"`
}

type StatusFieldMapping struct {
    // To is the dotted path under .status to write, e.g. "endpoint" or "network.host".
    // +required
    To string `json:"to"`

    // Source names the object Expression is evaluated against: a built-in ("self", or its
    // "spec"/"status" sugar, "helm") or a StatusSources[].name. Default "spec".
    // (RDC additionally has the built-in "response".)
    // +optional
    // +kubebuilder:default=spec
    Source string `json:"source,omitempty"`

    // Expression is the transform program (jq — recommended Proposal A, §3.1) evaluated
    // against Source. A bare path is the trivial copy, e.g. ".service.host".
    // +required
    Expression string `json:"expression"`

    // Type optionally pins the JSON-schema type of the generated status property
    // ("string"|"integer"|"number"|"boolean"|"object"|"array"). When omitted the type is
    // inferred from the chart values schema for simple-path expressions (string fallback).
    // For scalars only — for objects/arrays use Schema or PreserveUnknownFields (§4.2).
    // +optional
    Type string `json:"type,omitempty"`

    // Schema optionally supplies the full JSON-schema for a COMPLEX output (object, array,
    // array of objects, …) that the expression constructs. Required to get structural
    // typing/validation when the shape can't be inferred from a simple path (§4.2/§5).
    // +optional
    Schema *apiextensionsv1.JSONSchemaProps `json:"schema,omitempty"`

    // PreserveUnknownFields generates the status node with
    // x-kubernetes-preserve-unknown-fields: true, so the apiserver retains an arbitrary
    // (possibly dynamic) structure without pruning — the escape hatch when neither a
    // simple-path inference nor an explicit Schema is practical. Loses per-field
    // typing/validation. Mutually exclusive with Type/Schema. (§4.2)
    // +optional
    PreserveUnknownFields bool `json:"preserveUnknownFields,omitempty"`
}
```

Example `CompositionDefinition` (jq — Proposal A):

```yaml
spec:
  chart: { url: oci://…/fireworksapp, version: 1.2.0 }
  additionalStatusFields:
    - to: endpoint                       # status.endpoint
      expression: .service.host          # plain copy of spec.service.host
    - to: url                            # status.url (derived)
      source: spec
      expression: '"https://\(.service.host):\(.service.port)"'
    - to: replicas                       # status.replicas (typed number, jq keeps the int)
      source: spec
      expression: '[.replicas, 1] | max'
      type: integer
    - to: release.revision               # status.release.revision (nested), from helm source
      source: helm
      expression: .revision
```

The same definition under **CEL (Proposal B)** would read e.g.
`expression: 'spec.service.host'`, `expression: '"https://" + spec.service.host + ":" +
string(spec.service.port)'`, etc. — see the §3.1 table.

### 4.1 Worked example — end to end (where the CDC reads inputs)

The inputs are **not fetched from anywhere new**: `source: spec` is the
`Composition` instance's **own `.spec`**, which *is* the set of Helm values the user
supplied — the CDC already has the object in hand each reconcile. `source: helm` is the
Helm release metadata the CDC already holds after install/upgrade. No extra reads, no
cross-cluster lookups.

**(1) Chart** `values.schema.json` (defines the spec the user fills in):

```jsonc
{ "type": "object", "properties": {
    "service": { "type": "object", "properties": {
        "host": { "type": "string" }, "port": { "type": "integer" } } },
    "replicas": { "type": "integer" } } }
```

**(2) `CompositionDefinition`** declares the projection (the YAML in §4 above).
core-provider generates the `FireworksApp` CRD whose `status` now also has
`endpoint`, `url`, `replicas`, `release.revision` (plus the baseline fields).

**(3) A `Composition` instance** — `.spec` is the chart values:

```yaml
apiVersion: composition.krateo.io/v1-2-0
kind: FireworksApp
metadata: { name: demo, namespace: apps }
spec:                      # <-- this is "source: spec"
  service: { host: demo.example.com, port: 8080 }
  replicas: 3
```

**(4) CDC reconcile.** The CDC installs the chart, then builds the sources and projects:

```go
sources := map[string]any{
    // "spec" is injected automatically from mg.Object["spec"] (the values above)
    "helm": map[string]any{"revision": rel.Version, "name": rel.Name,
                           "version": pkg.Version, "status": rel.Info.Status.String()},
}
_ = statusprojection.Project(mg, sources, mappings)   // mappings delivered via §6.1
statusprojection.SetObservedGeneration(mg)
```

Evaluating each mapping: `.service.host` → `demo.example.com`;
`"https://\(.service.host):\(.service.port)"` → `https://demo.example.com:8080`;
`[.replicas,1]|max` → `3` (kept as a number); `helm`/`.revision` → `1`.

**(5) Resulting `Composition` `.status`:**

```yaml
status:
  # --- projected from spec/helm by the mappings ---
  endpoint: demo.example.com
  url: https://demo.example.com:8080
  replicas: 3
  release: { revision: 1 }
  observedGeneration: 1
  # --- baseline fields the CDC already writes today ---
  helmChartUrl: oci://…/fireworksapp
  helmChartVersion: 1.2.0
  managed: [ … ]
  conditions: [ { type: Ready, status: "True", … }, { type: Synced, … } ]
```

### 4.2 Complex status structures (objects, arrays, arrays of objects)

A status field is not limited to a scalar. jq constructs arbitrary JSON, and the engine
writes the **whole constructed value** at `to`, so objects/arrays/arrays-of-objects/nested
arrays all work in **one mapping**:

```yaml
additionalStatusFields:
  # object value
  - to: network
    source: spec
    expression: '{ host: .service.host, port: .service.port }'
    schema:                              # explicit shape (§5)
      type: object
      properties: { host: {type: string}, port: {type: integer} }

  # array of objects
  - to: endpoints
    source: spec
    expression: '[ .ingress[] | { host: .host, tls: (.tls // false) } ]'
    schema:
      type: array
      items:
        type: object
        properties: { host: {type: string}, tls: {type: boolean} }

  # dynamic / unknown shape — escape hatch, no per-field typing
  - to: raw
    source: spec
    expression: '.somethingDynamic'
    preserveUnknownFields: true
```

Two rules and one runtime caveat govern this:

- **`to` addresses object locations only.** Dotted `to` builds nested *objects*
  (`a.b.c`); you never index arrays in `to`. To produce an array at `status.endpoints`,
  the **expression yields the array** (`[ … ]`). This keeps writes unambiguous.
- **Single value per mapping.** A jq program can emit a *stream*; a mapping must resolve to
  one value. Wrap multi-output in `[ … ]` to make it an explicit array; a bare stream is a
  validation error (§5).
- **Normalization is mandatory (runtime).** `unstructured.SetNestedField` →
  `DeepCopyJSONValue` accepts only `map[string]interface{}`, `[]interface{}`, `string`,
  `int64`, `float64`, `bool`, `nil`, `json.Number` and **panics** on anything else — and
  gojq emits plain `int` for integers (and other non-JSON-native Go types). So the engine
  **must normalize** every jq result (recursively: `int`→`int64`, ensure containers are
  `map[string]interface{}`/`[]interface{}`) before writing. **`braghettos/plumbing v1.7.6`
  already carries this fix** in `jqutil` (commit `28c9297` "handle int64/int32 in jqutil
  encoder (prevents gojq panic)"), and `jqutil.InferType` produces typed values — so the
  engine inherits the normalization from `jqutil` rather than reimplementing it. (This fix
  is **fork-only**, not yet upstreamed — see §9 on plumbing alignment.) This is one more
  reason the engine is a thin adapter over shared `plumbing` code, not per-provider.

### 4.3 GVR sources: the composition itself, and its managed resources

The general rule (§3) is that **a source is a GVR reference**. The Composition is the
built-in `self` source (its own GVR — already in hand, no I/O); the most valuable status
values, though, live on the **managed resources the composition created** (a `Service`'s
allocated LB IP, a `Deployment`'s `readyReplicas`, a generated `Secret` field) — possibly
**aggregated across several**. Those are declared as `statusSources` (§4) and resolved by
the CDC into the object(s) jq runs against:

```yaml
spec:
  statusSources:
    web:                                   # a single Service (label-scoped, names are templated)
      apiVersion: v1
      kind: Service
      selector: { matchLabels: { app.kubernetes.io/name: web } }
    deploys:                               # a SET of Deployments -> resolved doc is an array
      apiVersion: apps/v1
      kind: Deployment
      selector: { matchLabels: { krateo.io/composition-id: "{{ .metadata.uid }}" } }
  additionalStatusFields:
    # from the composition itself (self/spec) — Phase 1, no I/O
    - to: url
      source: spec
      expression: '"https://\(.service.host):\(.service.port)"'

    # from a single managed object — Phase 2
    - to: endpoint
      source: web                          # the Service GVR source above
      type: string
      expression: '.status.loadBalancer.ingress[0].ip'

    # AGGREGATE across a managed set — Phase 2
    - to: readyReplicas
      source: deploys                      # resolved doc is the array of Deployments
      type: integer
      expression: '[ .[].status.readyReplicas // 0 ] | add'

    - to: hosts
      source: deploys
      schema: { type: array, items: { type: string } }
      expression: '[ .[].spec.template.metadata.annotations["host"] ] | map(select(.)) | unique'
```

A built-in `managed` set source (the union of everything in `status.managed[]`) is offered
as a convenience for "aggregate across all my resources" without declaring a selector.

**The "pure engine" rule holds: the CDC does the I/O, the engine does not.** The CDC
resolves each source's GVR ref (GVR→GVK, fetch by name or list-by-selector — scoped to the
composition's own resources via the labels the CDC already stamps) and passes the resolved
documents in; Phase 2's readiness rollup needs the *same* fetch (§7):

```go
resolved := map[string]any{"helm": …}
for _, s := range sources {               // declared StatusSources
    resolved[s.Name] = h.resolve(ctx, mg, s)   // single obj or []any for a selector
}
_ = statusprojection.Project(mg, resolved, mappings)  // "self"/"spec"/"status" come from mg
```

**Cost — the real trade-off.** `self`/`spec`/`status`/`helm` are free (already in hand).
A GVR source requires **fetching the referenced object(s) every reconcile**, and to keep
values *fresh* the CDC must **watch** them (otherwise a late-arriving LB IP only appears on
the next resync) — precisely the watch-cost in #14's open questions and shared with
readiness rollup. So:

- **Phase 1** ships the in-hand sources (`self`/`spec`/`status`/`helm`) only — cheap, no
  new watches.
- **Declared GVR sources (incl. the `managed` convenience) ship in Phase 2** (§7/§8),
  sharing the fetch+watch machinery with readiness rollup. `statusSources` and
  `source: <gvr>` are defined now (forward compatible) but rejected by validation until
  that phase lands.

### 4.4 External sources via `apiRef` → RESTAction

The snowplow convention (§3.2) has a second source kind we should carry through to
compositions: **`apiRef`**, a reference to a **`RESTAction`** CR
(`templates.krateo.io`, `apis/templates/v1/restactions.go`) describing named REST calls.
This lets a Composition surface status from an **external system** — a provisioning/health
endpoint, a cloud API, a billing/quota service — not just from its own spec or its
in-cluster managed resources.

It is the exact mechanism snowplow already uses: `apiref.Resolve`
(`internal/resolvers/widgets/apiref/resolve.go`) fetches the referenced `RESTAction`,
executes its calls, and returns a `map[string]any` of responses **keyed by call name**.
Those keys join the same combined jq root, so a `statusDataTemplate` reads them like any
other source:

```yaml
spec:
  apiRef:                                  # snowplow shape: name/namespace of a RESTAction
    name: backend-health
    namespace: demo-system
  statusDataTemplate:
    - forPath: backendReady                 # status.backendReady, from an external API
      expression: ${ .api.health.status == "UP" }
      type: boolean
    - forPath: externalId
      expression: ${ .api.provision.id }
```

(`.api` is the keyed apiRef root; `.api.health`, `.api.provision` are individual RESTAction
calls.) The same source could equally feed a `resourcesRefs` extraction — they coexist in
one root, exactly as in a snowplow widget.

**Design implications (heavier than the other sources, hence later phase):**

- **Executor.** Something must *run* the RESTAction each reconcile. The clean move is to
  **reuse snowplow's `apiref` resolver / RESTAction executor** (another shared-code lever,
  §3.2/§9) rather than build a second one — the CDC calls it and passes the keyed responses
  into `Project`. The engine stays pure (no I/O).
- **Secrets & RBAC.** RESTActions reference credentials (Secrets) for the target API. The
  CDC needs read access to those Secrets and the `RESTAction` CR — scope via the same
  reference pattern used elsewhere; no plaintext in the Composition or logs.
- **Cost & freshness.** External calls every reconcile + a poll interval to refresh — the
  heaviest source. Treat external failures as a degraded field (per-mapping error →
  `Synced=False/ReconcileError`), never a hard reconcile failure.
- **Phasing.** Ships **after** the managed-resource source — Phase 2/3 (§8). The API
  (`apiRef`) is defined now for forward-compatibility but validation-rejected until then.

This makes the source model complete and fully snowplow-aligned: **`self`/`helm`**
(in-hand), **`resourcesRefs`** (in-cluster GVRs), and **`apiRef`/RESTAction** (external
APIs) — one keyed jq root, one `*DataTemplate`, one engine.

---

## 5. Schema generation (provider-local, core-provider)

The status subresource **prunes** unknown fields, so every declared `to` path must exist
in the generated CRD's status schema or the controller's writes are silently dropped.
core-provider therefore extends its status schema from the declarations:

- Start from the static `status.schema.json` (unchanged baseline).
- For each `StatusFieldMapping`, add a property at `to` (building intermediate objects for
  dotted paths). Its sub-schema is chosen, **in precedence order**:
  1. **`Schema`** if given — used verbatim for complex shapes (object / array /
     array-of-objects, §4.2). The author owns the structure.
  2. **`PreserveUnknownFields`** — emit the node as `{ x-kubernetes-preserve-unknown-fields:
     true }` so the apiserver retains arbitrary structure without pruning (no per-field
     typing). The escape hatch for dynamic shapes.
  3. **`Type`** if pinned — a scalar type (and, for `array`/`object` without `Schema`, must
     fall back to preserve-unknown since a structural schema needs `items`/`properties`).
  4. **simple-path inference** — for a bare `.a.b.c` against `source: spec`, copy that
     field's **full sub-schema** (including nested object/array structure) from the chart's
     `values.schema.json` — so "copy this whole object/array from spec→status" is typed for
     free, mirroring `oas2jsonschema.composeStatusSchema`.
  5. **`string` fallback** — jq's output type is not statically known for non-trivial
     programs; pin `Type`/`Schema` when the field must be non-string. (jq's one ergonomic
     cost vs. CEL, §3.1.)
- **Structural-schema constraints (must enforce).** CRD status schemas are *structural*:
  every `object` needs `properties` or `additionalProperties` or preserve-unknown; every
  `array` needs `items`; no bare untyped nodes. The generator rejects a `Schema` that
  violates this, and never emits an untyped object/array (falls to preserve-unknown).
- Feed the merged status schema into the existing `crdgen.Generate(Options{StatusSchema:…})`
  path (`generation.go`), then the existing per-version `UpdateStatus` loop stamps it onto
  all served versions — so this composes with the multi-version / `vacuum` machinery
  already in place.

> A small reusable **schema helper** (resolve path in a JSON schema, add property by
> dotted path, with string fallback) is worth extracting; oasgen's `oas2jsonschema`
> already has the equivalent. Candidate home: `plumbing` (already a `braghettos` fork and
> already core-provider's crdgen dependency) so both providers share it. Optional — can
> stay duplicated initially.

**Validation at admission time** (core-provider's reconcile of the `CompositionDefinition`):
reject overlaps with the reserved baseline fields (`helmChartUrl`, `managed`, …),
duplicate `to`s, unknown `source`, and an `expression` that fails to parse/compile
(`gojq.Parse` for jq; `cel.Compile` under Proposal B) — surfaced as a condition on the
`CompositionDefinition`, not deferred to the CDC.

---

## 6. Runtime engine (shared, `unstructured-runtime`)

New package `pkg/tools/statusprojection` in **`braghettos/unstructured-runtime`**:

```go
package statusprojection

// Mapping mirrors the declarative field mapping (decoupled from any provider CRD).
type Mapping struct {
    To, Source, Expression string
}

// Project evaluates each mapping's Expression against its resolved Source and writes the
// (typed, normalized) result into cr's .status at To. `resolved` maps a source name to its
// already-fetched document (a single object's content, or an array for a selector source);
// the built-ins "self"/"spec"/"status" are derived from cr directly. Any source needing
// cluster I/O (a declared GVR source) is resolved by the CALLER and passed in — Project
// performs no client calls. Expressions are jq programs run via plumbing/jqutil (gojq),
// compiled-and-cached per expression. Each result is normalized to DeepCopyJSONValue-safe
// types (int->int64, etc.) before writing (§4.2).
func Project(cr *unstructured.Unstructured, resolved map[string]any, mappings []Mapping) error

// SetObservedGeneration writes status.observedGeneration = metadata.generation.
func SetObservedGeneration(cr *unstructured.Unstructured)
```

Properties (note how each beats the RDC `populateStatusFields` limits from §1.4):

- **Pure & side-effect free** (no client calls); the caller persists via the existing
  `tools.UpdateStatus`.
- **Nested `to` paths and arbitrary source navigation** — jq reads any depth of the
  source and the engine writes nested status paths (building intermediate objects). Beats
  #40's top-level-only matching.
- **Type-preserving writes** through `unstructured.SetNestedField`, using gojq's raw
  output value (`any`) — numbers/bools/objects are kept as themselves, so values match the
  generated schema's types. This makes #42's hand-rolled int/float/bool conversion
  **unnecessary** (type-safety is intrinsic to jq), rather than something to port.
- **Transform built in** — the `Expression` *is* the transform; a bare path is the
  trivial copy; it can construct **objects/arrays/arrays-of-objects** and **aggregate**
  across a source array (`add`, `unique`, `group_by`…) — see §4.2 / §4.3. Beats copy-only.
- **Output normalization** — every jq result is normalized to DeepCopyJSONValue-safe types
  (`int`→`int64`, JSON-native containers; integral floats re-narrowed) before
  `SetNestedField`, so complex values write without panics and integers stay integers
  (§4.2). **Reuse `jqutil.InferType` / `jqutil.MaybeQuery`** (already used by snowplow,
  §3.2) rather than reimplementing — this subsumes RDC #42's type work and means the engine
  is a thin adapter over existing `plumbing` code.
- Errors are per-mapping and aggregated; a bad mapping degrades that field (and raises a
  `Synced=False/ReconcileError` reason) rather than failing the whole reconcile.

> Under **Proposal B (CEL)** the engine would instead bind sources as CEL variables and
> evaluate compiled `cel.Program`s; the package shape (`Project`/`Mapping`) is identical,
> so the dialect is an internal detail of the engine.

Living here means RDC can **replace** its `populateStatusFields` loop (the #40/#42 work)
with a `Project` call where `sources["response"] = body` — gaining nested paths +
transforms + native type-safety in one engine, and ending the per-provider divergence
(see §1.4).

### 6.1 CDC wiring

In `composition-dynamic-controller`'s `Observe`/`Create`/`Update`
(`internal/composition/composition.go`), after the existing status writes:

```go
sources := map[string]any{
    "helm": map[string]any{
        "revision": rel.Version, "name": rel.Name,
        "version":  pkg.Version, "status": rel.Info.Status.String(),
    },
}
_ = statusprojection.Project(mg, sources, mappings) // spec injected automatically
statusprojection.SetObservedGeneration(mg)
_, err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
```

**How does the CDC learn `mappings`?** The declarations live on the
`CompositionDefinition` (management cluster), but the CDC runs against the
Composition CRD (possibly a remote target) and is intentionally decoupled from the
`CompositionDefinition`. Two options:

- **(a) Project into the CDC ConfigMap** core-provider already renders for the CDC
  (`internal/tools/deploy`), as a serialized mapping list. *Preferred* — keeps the CDC
  free of any cross-cluster read of the `CompositionDefinition`, consistent with the
  multi-cluster design where the CDC uses only target-local inputs.
- **(b) Annotate the generated CRD** with the mappings and have the CDC read its own CRD.
  Simpler to plumb but couples the CDC to CRD annotations.

Recommendation: **(a)**, extending the existing CDC ConfigMap template.

---

## 7. Deferred: managed-source projection, readiness rollup & Helm metadata (Phase 2)

Not built in Phase 1; recorded so the API above stays forward-compatible. These three
share one piece of machinery — **fetching (and watching) the `status.managed[]` set** —
which is why they land together.

- **`managed` source projection (§4.3).** Extract/aggregate status values from the live
  managed resources. Needs the managed-object fetch (and a watch, for freshness). The CDC
  fetches and passes them to `Project` as `sources["managed"]`; the engine stays pure.
- **Readiness rollup.** Iterate `status.managed[]`, fetch each via the dynamic client (the
  *same fetch* as above), compute per-object status with **kstatus**
  (`sigs.k8s.io/cli-utils/pkg/kstatus`), and roll up into `Ready` (`Current`⇒True; any
  `Failed`⇒False/reason; else `InProgress`). Needs the CDC to **watch** managed objects
  (cost noted in #14's open questions) and a generic helper — a natural second
  `unstructured-runtime` addition (`pkg/tools/readiness`) reusable by RDC. An extensibility
  hook handles kinds kstatus can't assess.
- **Helm metadata.** Surface `release.revision/name/state/lastDeployed` — already
  available from the Helm release object the CDC holds; can ship as **baseline status
  fields** (extend `status.schema.json`) independently of projection, or be reached via
  `source: helm` mappings (§4 example). Recommend baseline fields for the common ones so
  they exist without any declaration.

---

## 8. Phased plan

1. **Phase 0 — shared engine.** `pkg/tools/statusprojection` (+ `SetObservedGeneration`)
   in `braghettos/unstructured-runtime`, with unit tests; tag a release.
2. **Phase 1a — core-provider API + schema.** Add `additionalStatusFields` to
   `CompositionDefinition` (incl. `Schema`/`PreserveUnknownFields` for complex shapes,
   §4.2); merge declared properties into the generated status schema; validate declarations
   on reconcile (reject `source: managed` until Phase 2); pass mappings to the CDC
   (ConfigMap route).
3. **Phase 1b — CDC populate (`spec` + `helm`).** Bump CDC's `unstructured-runtime` to the
   tag carrying `statusprojection` (routine, already on `v1.1.0`); call `Project` +
   `SetObservedGeneration`; e2e on a chart that declares scalar, derived, and
   object/array fields. No new watches.
4. **Phase 1c — RDC convergence.** Replace RDC's `populateStatusFields` with `Project`
   (`sources["response"] = body`). **Coordinate with the in-flight
   `braghettos/rest-dynamic-controller` branches #40 and #42** (§1.4): with jq the engine
   gives type-safety intrinsically, so #42's hand-rolled conversion is *dropped*, not
   ported. If #40/#42 merge first, Phase 1c becomes a refactor that deletes
   `populateStatusFields` in favour of the shared call. Decide ordering with whoever owns
   #40/#42.
5. **Phase 2 — `managed` source + readiness rollup + Helm metadata** (§7): add the
   managed-object fetch/watch once, then light up `source: managed` projection (§4.3) and
   the kstatus `Ready` rollup on top of it.
6. **Phase 3 — external sources via `apiRef`/RESTAction** (§4.4): reuse snowplow's `apiref`
   resolver/executor in the CDC, feed keyed responses into `Project`; secrets/RBAC + poll
   freshness. Heaviest source, lands last.

Each phase is independently shippable; Phase 0+1 deliver the headline capability for
`spec`/`helm`-derived status. Phase 2 unlocks the managed-resource-derived values that, as
noted in §4.3, are often the most useful — at the cost of watching the managed set. Phase 3
extends the same model to external systems with no new concepts — just another keyed source.

---

## 9. Repository / fork impact

All work targets the **`braghettos`** forks (origin), with `krateoplatformops` kept as
`upstream`:

| Repo | Fork (origin) | Status of pointer |
|---|---|---|
| core-provider | `braghettos/krateo-core-provider` | already pinned |
| composition-dynamic-controller | `braghettos/composition-dynamic-controller` | **repointed** (was `krateoplatformops`); `upstream` added |
| unstructured-runtime | `braghettos/unstructured-runtime` | **forked + cloned** (did not exist before); `upstream` added |
| oasgen-provider | `braghettos/oasgen-provider` | already checked out as `braghettos-oasgen-provider` |
| rest-dynamic-controller | `braghettos/rest-dynamic-controller` | **repointed** (was `krateoplatformops`); `upstream` added. NB: fork `main` has **diverged** from upstream (not a clean ff) and carries the #40/#42 branches |
| plumbing (`jqutil`; candidate home for the shared `resourcesRefs`/`*DataTemplate` types + resolver, §3.2) | `braghettos/plumbing` | already core-provider's `replace` target |
| snowplow (frontend BFF — source of the `resourcesRefs`/`widgetDataTemplate` convention + resolver to share, §3.2) | `braghettos/snowplow` (fork to create) | not yet a fork; only needed if we lift its types/resolver into `plumbing` |

> **Plumbing alignment (audited 2026-06-18).** All forks now source plumbing from
> `braghettos/plumbing` and are pinned to the **same `v1.7.6`** (`replace` directives;
> core-provider, CDC, unstructured-runtime, braghettos-oasgen-provider — all build green;
> RDC has no plumbing dep). The fork's shared tags (`v1.7.0/v1.7.3/v1.8.1`) are
> **byte-identical to upstream**; `v1.7.6` is **fork-only**, carrying two unupstreamed
> fixes — the `jqutil` int/int32 gojq-panic fix (`28c9297`, directly relevant to §4.2) and
> a crdgen array-default fix (`9e1af5d`). However the fork is **diverged from upstream: 3
> ahead, 19 behind** (upstream is already at `v1.9.0`). **Action item:** merge `upstream`
> into `braghettos/plumbing` to catch up (preserving the 2 fork fixes; ideally upstream
> them), then re-verify the dependent forks build — the 19 upstream commits may move
> plumbing APIs.

go.mod consequences:

- **Shared types/resolver (§3.2)**: lifting snowplow's convention into `plumbing` lets
  snowplow, core-provider, CDC and RDC all depend on one implementation. Reconcile the
  version skew (snowplow `plumbing v0.6.2` vs the forks' `braghettos v1.7.6`).
- **`unstructured-runtime`**: `statusprojection` depends on `plumbing/jqutil` (jq,
  Proposal A) — so `unstructured-runtime` gains a `plumbing` dependency (pinned to the
  `braghettos` fork via `replace`). Under Proposal B it would instead pull `cel-go`. If
  taking the jq dependency into the generic runtime is undesirable, the engine can accept
  an injected evaluator interface and core-provider/CDC wires `jqutil` in — keeping
  `unstructured-runtime` dialect-agnostic. *(Open question, §11.)*
- CDC: routine bump + `replace github.com/krateoplatformops/unstructured-runtime =>
  github.com/braghettos/unstructured-runtime <new-tag>` (already on `v1.1.0`).
- core-provider: already `replace … => github.com/braghettos/plumbing`; `jqutil` rides it.

---

## 10. Alternatives considered

- **Keep it local to core-provider + CDC (no `unstructured-runtime` change).** Faster, no
  cross-repo work and no CDC version bump — but RDC/oasgen cannot reuse it, the two
  providers keep diverging, and the "unify `additionalStatusFields`" win is lost. Rejected
  given the explicit goal to converge on the shared runtime.
- **Transform language: jq vs CEL vs sprig.** Fully compared in §3.1 with examples.
  Recommendation is **jq** (already a Krateo dependency, platform convention, native type
  preservation that subsumes RDC #42). CEL is the documented runner-up (Kubernetes-native,
  compile-time typing, but a new dependency and not the Krateo convention); sprig/Go
  templates are rejected (stringly-typed).
- **Generate status purely from the chart (a `status.schema.json` shipped in the chart).**
  Pushes the contract into chart authorship and still needs a populate step; the mapping
  approach is more explicit about *where values come from* and supports transforms.

---

## 11. Open questions

- **Mapping delivery to the CDC**: ConfigMap (recommended) vs. CRD annotation (§6.1) —
  confirm against the multi-cluster decoupling constraints.
- **Transform dialect**: jq (Proposal A, recommended) vs CEL (Proposal B) — §3.1. Settle
  before Phase 0. (§3.2's snowplow precedent strongly favours jq.)
- **Shared-types home (§3.2)**: lift snowplow's `resourcesRefs`/`*DataTemplate`
  `Reference`/`ResourceRef`/`WidgetDataTemplate` types and the
  `widgetdatatemplate`/`resourcesRefs` resolver into **`plumbing`** (or a small shared
  module) so snowplow, the CDC, and RDC share one implementation — vs. duplicating the
  shape in `unstructured-runtime`. Decide the boundary; this is the biggest reuse lever.
  Note snowplow currently pins `plumbing v0.6.2` while core-provider is on the `braghettos`
  `v1.7.x` line — reconcile versions when lifting types.
- **Engine dependency boundary**: does `statusprojection` import `plumbing/jqutil`
  directly (simple, but adds a `plumbing` dep to the generic runtime), or take an injected
  evaluator interface so `unstructured-runtime` stays dialect-agnostic and the provider
  wires jq in (§9)? Lean injected-evaluator.
- **Source surface**: which named sources to expose and whether `Expression` may read the
  existing `status` (for accumulation). Lean minimal first (`spec`, `helm`/`response`),
  then `managed` in Phase 2.
- **Managed-resource addressing & freshness (§4.3)**: how to identify a specific managed
  object when chart-rendered names are release-prefixed/templated (select by `kind` +
  labels rather than hard-coded `name`?); and the watch strategy + resync interval to keep
  `managed`-derived values fresh without excessive reads. Shared with Phase 2 readiness.
- **Schema-helper home**: extract into `plumbing` (shared) vs. duplicate per provider
  initially.
- **RDC scope & sequencing**: branches #40 (populate `additionalStatusFields`) and #42
  (type-safe conversion) are already open on `braghettos/rest-dynamic-controller`. Do we
  (i) let them merge and then refactor RDC onto the shared engine, or (ii) hold them and
  land the shared engine first? Either way #42's type-conversion logic should end up *in*
  the shared engine, not duplicated.
- **Versioning interaction** (#14): how projected fields behave across the full/parallel/
  selective migration patterns — mappings are per-`CompositionDefinition`-version, so they
  ride the existing per-version status stamping, but worth an explicit test.
