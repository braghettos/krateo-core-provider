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
2. Add `spec.additionalStatusFields` to `CompositionDefinition`, a list of
   **field mappings** `{ from, to, transform? }`. core-provider injects matching
   properties into the generated CRD's status schema (so they survive subresource
   pruning); the CDC fills them each reconcile.
3. Put the **generic projection engine in `unstructured-runtime`** (a new
   `pkg/tools/statusprojection` package). It takes a CR, one or more **named sources**
   (the CR's own `spec`, and provider-supplied maps such as the Helm release metadata
   or — in RDC — the API response), a list of mappings, and writes `status`. Both the
   CDC and RDC consume it; `oasgen-provider`'s `additionalStatusFields` becomes the
   "source = response" case of the same model.
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
stop: a shared engine should **subsume #40 + #42** — keeping #42's type preservation while
adding nested paths, transforms, and multiple sources — so RDC adopts it instead of
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
- **G2.** Those fields are populated each reconcile by **projecting/transforming the
  instance's inputs** (its `spec`, i.e. the chart values) — and, by extension, other
  named sources (Helm release metadata; in RDC, the API response).
- **G3.** The runtime projection/transform engine is **shared** (lives in
  `unstructured-runtime`), so the CDC and RDC use one implementation and `oasgen`'s
  `additionalStatusFields` is re-expressed as one case of it.
- **G4.** Populate `status.observedGeneration` (cheap, asked for by #14).
- **G5.** Backward compatible: no declarations ⇒ status identical to today.

**Non-goals (this phase).**

- **N1.** kstatus-based readiness rollup of `managed[]` into `Ready` (Phase 2, §7).
- **N2.** Helm release revision/state/deploy-time surfacing (Phase 2, §7).
- **N3.** Changing the conversion/`vacuum` storage-version machinery.
- **N4.** Arbitrary cross-resource lookups (reading other clusters' objects) as a
  projection source.

---

## 3. The unifying model

A status projection is a pure function:

```
project(sources: map[string]any, mappings: []Mapping) -> patch applied to CR .status
```

where a **source** is a named root document the engine can read JSONPath-style paths
from, and a **Mapping** is:

```
Mapping {
  From      string   // path into a source, e.g. "spec.service.host" or "response.id"
  To        string   // dotted path under status, e.g. "endpoint" or "network.host"
  Source    string   // which named source From reads from; default "spec"
  Transform string   // OPTIONAL CEL expression; receives `value` (+ `self`, `sources`)
}
```

The provider decides **which sources exist** and **where the mappings come from**:

| Provider | Sources it supplies | Mapping origin |
|---|---|---|
| core-provider / CDC | `spec` (the CR's own spec = chart values), `helm` (release metadata: revision, name, version, status) | `CompositionDefinition.spec.additionalStatusFields` |
| oasgen / RDC | `response` (API GET/FINDBY body), `spec` | `RestDefinition.spec.resource.additionalStatusFields` (+ `identifiers`) |

The engine is identical; only the source maps and the mapping list differ. This is the
core of G3: **`additionalStatusFields` (response→status) and our new spec→status
projection are the same operation over different sources.**

### 3.1 Why CEL for `Transform`

- Already the expression language of the in-apiserver `MutatingAdmissionPolicy` this repo
  adopted, and of CRD validation rules — consistent mental model, no new runtime dep
  beyond `k8s.io/apiserver/pkg/cel` / `github.com/google/cel-go` (already transitively
  present).
- Sandboxed, no I/O, deterministic — safe to evaluate on user-authored definitions.
- Plain copy is just the degenerate transform `value`; so "path copy + optional
  transform" collapses to one code path.

`sprig`/Go templates are the alternative (familiar to chart authors) but stringly-typed
and weaker for numeric/bool results; CEL preserves types, which matters for the schema
(see §5). **Recommendation: CEL, with copy-by-path as the default when `Transform` is
empty.**

---

## 4. API: `CompositionDefinition.spec.additionalStatusFields`

```go
// apis/compositiondefinitions/v1alpha1/types.go
type CompositionDefinitionSpec struct {
    Chart  *ChartInfo        `json:"chart,omitempty"`
    Deploy *DeploymentTarget `json:"deploy,omitempty"`

    // AdditionalStatusFields declares extra fields projected onto the generated
    // Composition CRD's status and populated by the controller each reconcile.
    // +optional
    // +listType=map
    // +listMapKey=to
    AdditionalStatusFields []StatusFieldMapping `json:"additionalStatusFields,omitempty"`
}

type StatusFieldMapping struct {
    // To is the dotted path under .status to write, e.g. "endpoint" or "network.host".
    // +required
    To string `json:"to"`

    // From is the dotted path read from Source, e.g. "service.host".
    // Required unless Transform is a self-contained expression.
    // +optional
    From string `json:"from,omitempty"`

    // Source names the document From reads from. Default "spec" (the chart values).
    // Allowed (CDC): "spec", "helm". (RDC additionally: "response".)
    // +optional
    // +kubebuilder:default=spec
    Source string `json:"source,omitempty"`

    // Transform is an optional CEL expression producing the value to write.
    // Receives `value` (the resolved From), `self` (the whole CR), and `sources`.
    // When empty, the value at From is copied verbatim.
    // +optional
    Transform string `json:"transform,omitempty"`

    // Type optionally pins the JSON-schema type of the generated status property
    // ("string"|"integer"|"number"|"boolean"|"object"|"array"). When omitted the
    // type is inferred from the chart values schema at From (string fallback).
    // +optional
    Type string `json:"type,omitempty"`
}
```

Example `CompositionDefinition`:

```yaml
spec:
  chart: { url: oci://…/fireworksapp, version: 1.2.0 }
  additionalStatusFields:
    - to: endpoint                       # status.endpoint
      from: service.host                 # spec.service.host (a chart value)
    - to: url                            # status.url (derived)
      source: spec
      transform: '"https://" + spec.service.host + ":" + string(spec.service.port)'
    - to: release.revision               # status.release.revision (nested)
      source: helm
      from: revision
```

---

## 5. Schema generation (provider-local, core-provider)

The status subresource **prunes** unknown fields, so every declared `to` path must exist
in the generated CRD's status schema or the controller's writes are silently dropped.
core-provider therefore extends its status schema from the declarations:

- Start from the static `status.schema.json` (unchanged baseline).
- For each `StatusFieldMapping`, add a property at `to` (building intermediate objects for
  dotted paths) with type:
  - `Type` if pinned; else
  - inferred by resolving `From` against the **chart's `values.schema.json`** (the same
    JSON-schema we already parse to build the spec) when `source: spec` — mirroring how
    `oas2jsonschema.composeStatusSchema` resolves against the response schema; else
  - `string` fallback.
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
duplicate `to`s, unknown `source`, and CEL that fails to compile — surfaced as a condition
on the `CompositionDefinition`, not deferred to the CDC.

---

## 6. Runtime engine (shared, `unstructured-runtime`)

New package `pkg/tools/statusprojection` in **`braghettos/unstructured-runtime`**:

```go
package statusprojection

// Mapping mirrors the declarative field mapping (decoupled from any provider CRD).
type Mapping struct {
    To, From, Source, Transform string
}

// Project evaluates mappings against sources and writes results into cr's .status.
// `sources` maps a source name ("spec","helm","response",…) to a document; the CR's
// own spec is injected as source "spec" automatically. CEL programs are compiled once
// and may be cached by the caller via Compile.
func Project(cr *unstructured.Unstructured, sources map[string]any, mappings []Mapping) error

// SetObservedGeneration writes status.observedGeneration = metadata.generation.
func SetObservedGeneration(cr *unstructured.Unstructured)
```

Properties (note how each beats the RDC `populateStatusFields` limits from §1.4):

- **Pure & side-effect free** (no client calls); the caller persists via the existing
  `tools.UpdateStatus`.
- **Nested `from` and `to` paths** — resolve dotted paths in any source and write nested
  status paths (building intermediate objects). Beats #40's top-level-only matching.
- **Type-preserving writes** through `unstructured.SetNestedField` so values match the
  generated schema's types. Adopt #42's int/float/bool conversion (incl. its float
  handling that avoids CRD warnings) as the no-transform path — i.e. lift #42's logic
  into the shared engine rather than leaving it in RDC.
- **Optional CEL transform**, compiled once and cached keyed by expression. Beats
  copy-only. With no transform, behaviour == #42's type-preserving copy.
- Errors are per-mapping and aggregated; a bad mapping degrades that field (and raises a
  `Synced=False/ReconcileError` reason) rather than failing the whole reconcile.

Living here means RDC can **replace** its `populateStatusFields` loop (the #40/#42 work)
with a `Project` call where `sources["response"] = body` — keeping the type-safety #42
just added, gaining nested paths + transforms, and ending the per-provider divergence
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

## 7. Deferred: readiness rollup & Helm metadata (Phase 2 sketch)

Not built now; recorded so the API above stays forward-compatible.

- **Readiness rollup.** Iterate `status.managed[]`, fetch each via the dynamic client,
  compute per-object status with **kstatus** (`sigs.k8s.io/cli-utils/pkg/kstatus`), and
  roll up into `Ready` (`Current`⇒True; any `Failed`⇒False/reason; else
  `InProgress`). This needs the CDC to **watch** managed objects (cost noted in #14's open
  questions) and a generic helper — a natural second `unstructured-runtime` addition
  (`pkg/tools/readiness`) reusable by RDC. An extensibility hook handles kinds kstatus
  can't assess.
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
   `CompositionDefinition`; merge declared properties into the generated status schema;
   validate declarations on reconcile; pass mappings to the CDC (ConfigMap route).
3. **Phase 1b — CDC populate.** Bump CDC's `unstructured-runtime` to the tag carrying
   `statusprojection` (routine, already on `v1.1.0`); call `Project` +
   `SetObservedGeneration`; e2e on a chart that declares a couple of fields + a transform.
4. **Phase 1c — RDC convergence.** Replace RDC's `populateStatusFields` with `Project`.
   **Coordinate with the in-flight `braghettos/rest-dynamic-controller` branches #40 and
   #42** (§1.4): rather than merging a third bespoke variant, land the shared engine and
   port #42's type-conversion into it, then have RDC call `Project`. If #40/#42 merge
   first, Phase 1c becomes a refactor that deletes `populateStatusFields` in favour of the
   shared call. Decide ordering with whoever owns #40/#42.
5. **Phase 2 — readiness rollup + Helm metadata** (§7), separately.

Each phase is independently shippable; Phase 0+1 deliver the headline capability.

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
| rest-dynamic-controller | `braghettos/rest-dynamic-controller` (fork exists) | repoint when Phase 1c starts |
| plumbing (schema helper, if extracted) | `braghettos/plumbing` | already core-provider's `replace` target |

go.mod consequences:

- CDC: bump + `replace github.com/krateoplatformops/unstructured-runtime =>
  github.com/braghettos/unstructured-runtime <new-tag>`.
- core-provider: if the schema helper lands in `plumbing`, it rides the existing
  `replace … => github.com/braghettos/plumbing` already present.

---

## 10. Alternatives considered

- **Keep it local to core-provider + CDC (no `unstructured-runtime` change).** Faster, no
  cross-repo work and no CDC version bump — but RDC/oasgen cannot reuse it, the two
  providers keep diverging, and the "unify `additionalStatusFields`" win is lost. Rejected
  given the explicit goal to converge on the shared runtime.
- **Go/sprig templates instead of CEL.** Familiar to chart authors but stringly-typed and
  weaker for typed status values; CEL matches the repo's existing
  `MutatingAdmissionPolicy`/CRD-validation expression language. Rejected as the default;
  could be offered later as an alternate `transform` dialect if chart authors ask.
- **Generate status purely from the chart (a `status.schema.json` shipped in the chart).**
  Pushes the contract into chart authorship and still needs a populate step; the mapping
  approach is more explicit about *where values come from* and supports transforms.

---

## 11. Open questions

- **Mapping delivery to the CDC**: ConfigMap (recommended) vs. CRD annotation (§6.1) —
  confirm against the multi-cluster decoupling constraints.
- **CEL surface**: which variables to expose (`value`, `self`, `sources`) and whether to
  allow reading the existing `status` (for accumulation). Lean minimal first.
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
