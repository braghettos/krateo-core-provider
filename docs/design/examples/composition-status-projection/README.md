# Composition status projection — example manifests

Worked manifests for [`../../composition-status-projection.md`](../../composition-status-projection.md).
**Design draft — these reference proposed `spec` fields (`statusDataTemplate`, `apiRef`,
`extras`) that do not exist yet.** They illustrate the design; they are not applyable today.

> **`extras` shape is NOT final — blocked on snowplow.** snowplow is adding `extras`
> *within* `apiRef`; core-provider will copy that shape once released. The `spec.extras`
> sibling used in `02-compositiondefinition.yaml` is a placeholder and will likely move
> nested under `apiRef`.

| File | What |
|---|---|
| `01-phase1-minimal.yaml` | Phase 1 — built-ins only (`.self`/`.helm`); no I/O, no RBAC |
| `02-compositiondefinition.yaml` | Full definition — built-ins + `apiRef` + author-declared `extras` |
| `03-restaction.yaml` | The referenced `RESTAction` + endpoint Secrets (in-cluster kube API + external) |
| `04-composition-instance.yaml` | A `Composition` instance + the resulting `.status` (in comments) |

## Data flow (full example)

1. Author declares `spec.extras` on the CompositionDefinition — `${ jq }` over the
   Composition instance, e.g. `compositionId: ${ .metadata.uid }`.
2. The CDC evaluates `extras` against the instance and asks **snowplow** to resolve the
   `apiRef` RESTAction, **under snowplow's own ServiceAccount**, passing the map as `Extras`.
3. `Extras` seeds the RESTAction's jq root; each `api[]` call's `path`/`payload` reads it
   (label-scoped kube reads + external calls). Responses merge back keyed by call name.
4. The CDC projects the combined root (`.self`, `.helm`, `.api.*`) via `statusDataTemplate`
   `${ jq }` and writes the **Composition's** `.status`. Only the Composition status is
   persisted; the RESTAction result is an ephemeral runtime query.

## Notes

- **No `resourcesRefs`**: the kube API is just an HTTP endpoint, so in-cluster reads are
  RESTAction calls.
- **Credentials live only on snowplow's SA** (the endpoint Secret tokens), never in the CDC.
- **Phase 1** (file `01`) needs none of the apiRef machinery and is independently shippable.
