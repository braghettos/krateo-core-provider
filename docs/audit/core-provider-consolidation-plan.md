# core-provider — audit vs. Matteo Gastaldello's roadmap, and a consolidation plan

> **Status:** draft for discussion (Diego). **Scope:** `braghettos/krateo-core-provider` + `krateo-composition-dynamic-controller` (CDC) + `unstructured-runtime` + `core-provider-chart`, audited against Matteo Gastaldello's `krateoplatformops/roadmap` discussions **#222, #230, #231, #232, #233, #234, #235, #242** (and #193 for scaling context). Every "fork reality" row is verified against code (`file:line`).
>
> **How to read this:** Part 1 is the audit (what Matteo found vs. what the fork actually does). Part 2 is a prioritized, phased consolidation plan. Items marked **⚖ DECISION** are strategic fork-vs-upstream choices for you to make, not mechanical work.

---

## Executive summary

Matteo's analysis is accurate and high-quality. Mapped onto **your fork**, his findings fall into four buckets:

- **The fork is AHEAD of upstream** on: served-version pruning/GC (his #234 "future work" — already done), `None` conversion (no webhook/cert churn), owner-scoped self-healing migration (F5), create-pending recovery (F8), and OTel tracing. Several problems he raises for *upstream* the fork already solved.
- **One confirmed correctness BUG the fork introduced**: #235 — removing the mutating webhook (F9) silently dropped **nested defaulting**. This is the single most urgent item.
- **One structural GAP that is the root of several symptoms**: `unstructured-runtime` (CDC) has **no graceful shutdown/drain** (#233, #231, #242). It strands `external-create-pending`, blocks CDC HA, and is *aggravated by F5* (version bumps roll the CDC).
- **Three strategic DIVERGENCES from upstream's direction**, which now stack and need a conscious call: migration philosophy (F5 eager vs. coexistence), create-pending (F8 recover vs. manual), and the `composition-version` label name (#102/#104).

Plus a set of **operational gaps** (chart HA defaults, leader-election posture, `kubectl` version display, per-controller config, state-model consolidation) that are real but lower-risk.

---

# Part 1 — Deep audit

## A. CRD version lifecycle (#234, #222)

| Matteo's claim / proposal | Fork reality (verified) | Verdict |
|---|---|---|
| Chart change → new CRD version → **new conversion webhook** | `None` conversion, no webhook (`crd.go` `setNoneConversion`) | ⚠️ stale for fork (true upstream) |
| Storage version = a real version needing re-stamp on removal | **`vacuum` is the permanent storage version** in BOTH fork and upstream (`generation.go:55-57`, `AppendVersion`); latest is `served:true,storage:false` | ❌ his "storage-safety" concern is **moot** in core-provider — pruning never touches `vacuum` |
| "The system only ever **appends** versions; nothing removes them" | Fork **prunes**: `prunableServedVersions` / `pruneStaleServedVersions` / `RemoveStaleVersions` (`compositiondefinitions.go:394+`) | ❌ false for fork — **the GC he proposes as future work is done** |
| Compatibility-first: coexist, remove only when nothing depends | Fork's ref-counted retirement (`versionReferencedByAnotherDefinition:375`) already matches this GC rule | ✅ fork matches |
| Recommend **no eager/implicit migration** (coexistence) | Fork does **eager owner-scoped self-healing migration** (F5, `UpdateCompositionsVersion`) | ⚖ **DIVERGENCE** (opposite direction) |
| Single authoritative `currentRef`; legacy fields become projections | Fork still has overlapping state (`status.apiVersion` + `status.managed.versionInfo` + label) | ✅ **valid gap** — not consolidated |
| `kubectl get composition` shows wrong version (`v1-2-3` names don't sort by k8s priority; no printer column) | Same `v<dots-as-dashes>` naming (`chart.go`); printer columns exist on **CompositionDefinition** (`types.go:304-311`) but **not on generated composition CRDs** | ✅ **valid gap** (cheap fix) |
| Per-composition upgrade policy (`Manual`/`Paused`), `upgrade-to-version` annotation, `ActiveOnly` strategy (#222) | Not implemented; fork upgrades all owned instances on bump | ✅ gap (design-level) |

**Correction to my prior note:** upstream does **not** make the latest version storage; both trees use `vacuum`. The only storage-adjacent difference is conversion (upstream passthrough-webhook vs. fork `None`).

**Pruning — live-verified on kind (k8s 1.36.1, `prune_e2e_test.go`, drives the real `prunableServedVersions`/`pruneStaleServedVersions`):**
- **S1 safe prune** — a stale served version with no instances/refs → prunable. ✅
- **S5 invariant** — `vacuum` (storage) and the current version are never prunable. ✅
- **S2 label guard** — an instance carrying `composition-version=v1-0-0` protects v1-0-0 (`kept=[v1-0-0:instances=1]`). ✅
- **S4 cross-reference** — another CompositionDefinition pinned to v1-0-0 protects it (`kept=[v1-0-0:referenced]`). ✅
- **S3 the danger probe** — an instance that exists at v1-0-0 but **lacks** the `composition-version` label: v1-0-0 **is pruned out from under it**. Verified outcome: the version is removed but the object is **NOT data-lost** — it remains readable through the current endpoint (None conversion serves the vacuum-stored object). Net effect = **orphaning, not deletion**, recoverable by re-stamping the label.

**So "ahead on pruning" holds — the GC is safe *given the label invariant*.** The one caveat S3 exposes is not a prune bug: the guard *cannot* drop the label filter (every version endpoint serves all stored objects under `None`, so an unlabeled list would over-retain forever). The real exposure is an instance that never got labeled — a composition **created on a target where the MutatingAdmissionPolicy is not installed** (F5's migration relabel covers policy-absent clusters, but a fresh CREATE there does not). This reinforces the **already-tracked policy-absence gap**, not a new item.

## B. Webhookless defaulting regression (#235) — **confirmed fork bug**

| Claim | Fork reality | Verdict |
|---|---|---|
| Fork `MutatingAdmissionPolicy` is **label-only** | `policy.go:60` CEL sets only `krateo.io/composition-version` | ✅ true |
| Fork has **no** replacement for the webhook's `PopulateDefaultsFromCRD` | `git grep PopulateDefaults\|applyDefaults` → 0 matches | ✅ true |
| **Nested defaults under an omitted parent** (`spec.image.tag` when `spec.image` absent) are silently dropped | apiserver doesn't synthesize absent parents; fork has nothing that does | ✅ **confirmed regression** |
| Array `minItems`+item-default also regresses | same cause; low likelihood (`crdgen` rarely emits item defaults) | ✅ latent |

**Impact:** any chart with `default:` under an optional object (`resources`, `image`, `persistence`, `securityContext` — very common) creates compositions with **missing defaults, no error**. Matteo reproduced it live; the code has no defaulting path. **His fix keeps you webhookless:** have `crdgen` emit a **field-level default** on the parent (`default: {}` / a pre-filled array from `minItems`) so the apiserver reaches the nested default.

## C. CDC runtime & HA (#233, #242, #193, #231)

| Finding | Fork reality (verified) | Verdict |
|---|---|---|
| **Graceful shutdown/drain absent in `unstructured-runtime`** (highest-priority; structural cause of stranded create-pending) | CDC `main.go:189` cancels root ctx on SIGTERM; workers not awaited → in-flight reconcile cancelled | ✅ **true — key structural gap** |
| CDC **cannot scale horizontally** (no leader election, fixed workers) | CDC `main.go` has **no** leader-election code | ✅ true — vertical-only |
| Management-policy enforcement **partial** in `unstructured-runtime` (create/update gating not applied) | upstream fix in-flight (`krateoplatformops/unstructured-runtime#58`, open); fork inherits the partial state | ✅ gap (watch the PR) |
| **Creation grace period** present in provider-runtime, absent in unstructured-runtime | not in fork's CDC runtime | ✅ gap (complements F8) |
| core-provider **leader election default `false`** → naive scale-up double-reconciles | `main.go:46` `flag.Bool("leader-election", …, false)` | ✅ true |
| Priority queue: fork's unstructured-runtime richer (3-level) than controller-runtime native (2-level) | fork keeps the 3-level queue | ✅ fork ahead; keep divergent (◆) |

## D. Deployment uniformity & chart HA (#230, #242)

| Finding | Fork reality (verified) | Verdict |
|---|---|---|
| CDCs deployed **uniformly**; no per-controller config; manual edits **reverted** by digest-driven re-apply | fork `deploy.go` re-applies + hashes every reconcile | ✅ true |
| No **opt-out** escape hatch (`krateo.io/unmanaged`) | `git grep unmanaged` in `deploy.go` → 0 | ✅ gap |
| Chart: `resources: {}` → CPU HPA can't compute | `chart/values.yaml:73` `resources: {}` | ✅ true |
| Chart: **no probes**, **no PDB**, **no topologySpread** | probes commented (`values.yaml:85-89`); no PDB template | ✅ true |
| **HPA only for core-provider**; chart-inspector `autoscaling.*` values are dead (Deployment drops `replicas` with no autoscaler → latent trap) | only `chart/templates/hpa.yaml`; none under `chart-inspector/` | ✅ true — real trap |
| Duplicate deploy logic core-provider vs oasgen-provider → extract shared component | (cross-repo) | ✅ valid |

## E. create-pending safety (#231) — F8 divergence

- Matteo recommends **keeping it manual** (decline auto-removal): removing the guard risks duplicating an already-created external resource.
- Fork's **F8 auto-recovers via Observe** (`worker.go:328-379`) — *not* blind removal (it observes first; if the resource exists → mark succeeded; if absent → clear pending + create).
- **Residual risk F8 carries that manual avoids:** an `Observe` **false-negative** under eventual consistency → clears pending → **duplicate**. And F5 (version-bump rollouts) + no-drain (C above) *increase* stranding, which F8 then auto-resolves.
- ⚖ **DIVERGENCE** — defensible, but the safer complement is **graceful drain** (kills stranding at the source), not the recovery path alone.

---

# Part 2 — Consolidation plan

Ordered by risk-reduction per unit effort. Tiers 0–2 are "make it correct and operable"; Tier 3 is CRD-lifecycle polish; Tier 4 is the strategic decisions to settle *with the maintainer* before upstream moves.

### Tier 0 — Correctness (do first)

**0.1 Fix the nested-defaulting regression (#235). — ✅ DONE (pending tag + bump).** *Repo: plumbing/crdgen (Option A — generator-level fix).*
- **Root cause verified at generator level:** crdgen preserves leaf `default:` but never synthesizes a parent `default: {}`, so a default under an omitted optional object is dropped (top-level/present-parent defaults were always fine). Blast radius is *nested-under-absent-parent only*.
- **Fix:** `crdgen/coders/types.go` `buildStruct` now emits a synthesized `+kubebuilder:default:={}` on an object property that carries a defaulted descendant but no default of its own. **Guarded** by `emptyObjectIsValid` so it never fires when the object has a required, non-defaultable child (which would turn a valid omission into a validation error). `$ref`-following is cycle-safe. Empty-object marker emitted as a literal (`{}`), independent of the release line's `DefaultValForKubebuilder` map handling.
- **Tests:** helper unit tests (`crdgen/coders/default_synth_test.go`) + Generate-level tests through the real controller-gen pipeline (`crdgen/nested_default_test.go`) + an apiserver-level e2e in core-provider (`internal/tools/crd/nested_default_e2e_test.go`) — creating a composition that **omits** `spec.image` yields `spec.image.tag=latest` materialized by the apiserver, no webhook; the guarded case correctly stays empty; top-level default applies. **Live-verified on kind (k8s 1.36.1): PASS.**
- **Adversarial review (6-lens workflow, 18 agents, every finding empirically re-probed):** found **1 real blocker** — `emptyObjectIsValid` wrongly approved a parent whose *required child object* had no own default (a defaulted sibling dragged the parent into an unsatisfiable `{}` → previously-valid omission rejected at admission). **Fixed** by the coherent "required means required" model: synthesize **only optional** fields, and treat a required property as satisfiable only if it carries its **own** default. Two further review findings fixed (required parents no longer auto-materialized; parents with an unrenderable own object-default still synthesize `{}`). Remaining gaps (array-item / inline-composition / additionalProperties / minProperties) confirmed **non-harmful** (valid CRDs) and documented in-code. Follow-up: backport object-default rendering to v1.7.x `DefaultValForKubebuilder` (separate pre-existing limitation).
- **Delivery:** branch `braghettos/plumbing` `fix/crdgen-nested-default-235-v1.7.16` (based on **v1.7.15**, the current fork tip — not v1.7.7, to avoid a semver-newer/content-older tag). **Remaining: cut tag `v1.7.16`, bump core-provider's `replace` v1.7.7 → v1.7.16.** core-provider builds clean against it (no API migration — Option A confirmed zero-cost).
- **Follow-up:** forward-port the same fix to plumbing `main` (removed `slogs/pretty` there; the map-render path differs) so it survives the eventual baseline modernization.
- **Why first:** silent data-correctness bug introduced by your own F9; every chart with optional-object defaults is affected.

### Tier 1 — Structural (unblocks HA + create-pending)

**1.1 Graceful shutdown / drain in `unstructured-runtime`. — ✅ IMPLEMENTED (pending review + tag).** *Repo: unstructured-runtime → CDC.*
- **Verified gap:** `Run` (controller.go:508) returned immediately on ctx cancel; workers (`wait.Until`) never awaited; the *same* SIGTERM ctx ran every in-flight `Observe/Create/Update/Delete` → writes severed. `controller.go`/`priorityqueue.go` were **byte-identical to upstream/main** (clean to upstream); the queue's `done` channel existed but was **never closed** (parked-worker wakeup = ~2-line backport).
- **Fix (decided: fork-first, in-flight-only, 30s default + paired chart):** workers run under a `context.Background()`-derived `reconcileCtx` (decoupled from SIGTERM), tracked in a `WaitGroup`; on shutdown: stop intake, `queue.ShutDown()` (now wakes parked workers via `close(done)`), bounded-wait in-flight reconciles up to `GracefulShutdownTimeout`, then cancel stragglers. New `Options.GracefulShutdownTimeout` (nil→30s, 0→abrupt, <0→forever) + `builder.WithGracefulShutdownTimeout`; **`Run` signature unchanged** (3 callers untouched). `SetGracefulShutdownTimeout` seam for the future HA lease-loss carve-out (#242).
- **Tests:** queue wakeup + idempotency; `Run` finishes an in-flight reconcile under a **live ctx** after SIGTERM; idle controller exits promptly; grace timeout cancels a stuck reconcile. Full suite green, **race-clean**. Branch `braghettos/unstructured-runtime` `feat/graceful-drain` (based on v1.3.1).
- **F8/#231:** the drain **strengthens** create-pending — the in-flight `Create` *and* its `SetExternalCreateSucceeded` annotation write now complete inside the grace window (delivers the 4.2 "drain-first" decision). #233 creation-grace-period is **not** bundled (lands in the `handleObserve` region PR #58 rewrites).
- **Adversarial review (5-lens workflow, 19 agents, `-race`):** caught **1 real blocker I introduced** — a `spin()` send-vs-`done` teardown race: a worker waking on `done` instead of receiving left `spin()` wedged on a blocking send while holding both queue mutexes, deadlocking every later `Len()`/`Done()` and the drain (reproducible even without `-race`). **Fixed** by making `spin()`'s hand-off selectable on `done`, plus a stress test + a 4-worker requeue-storm regression (race-clean, 3×). Also fixed a `timeout==0` compat issue (now returns without awaiting workers = true abrupt) and hardened the event-recorder test double for concurrency. Nits (waiters non-decrement) documented as benign.
- **Shipped:** `braghettos/unstructured-runtime` **tag `v1.3.2`** (superset of v1.3.1); CDC bumped (`feat/graceful-shutdown-flag`) with the `--graceful-shutdown-timeout` flag. **Remaining follow-up:** paired chart PR `terminationGracePeriodSeconds ≥ 40s`.
- **Payoff:** eliminates the structural cause of stranded `external-create-pending` (#231); prerequisite for any CDC HA; reduces the F5-rollout stranding.

**1.2 Adopt management-policy create/update gating.** Track/rebase `unstructured-runtime#58`; ensure the fork carries it (#233 item 2).

**1.3 Creation grace period** in unstructured-runtime (#233 item 7) — complements F8/create-safety.

### Tier 2 — Operability & HA (chart + posture)

**2.1 Chart HA baseline (`core-provider-chart`).**
- Add sane `resources.requests` for all three components (unblocks CPU HPAs).
- Ship **probes**, a **PodDisruptionBudget**, `topologySpreadConstraints`.
- **Fix the chart-inspector HPA trap:** either ship an HPA template for chart-inspector or make `autoscaling.enabled` not strip `replicas` without an autoscaler.
- core-provider HA-correct: `replicas ≥ 2` **with `leader-election: true`** (today default-off → double-reconcile if scaled). (#242)

**2.2 chart-inspector horizontal scaling.** It's stateless — give it the HPA it should have had (#242). Clean, low-risk win.

### Tier 3 — CRD-lifecycle polish

**3.1 `kubectl get composition` version display (#234B, #222).** Add an `additionalPrinterColumns` entry (version from the label/status) to the **generated** composition CRDs. Smallest, lowest-risk fix; independent of preferred-version resolution.

**3.2 Preferred-/served-version ordering.** Decide the display policy (**latest applied** recommended) and make preferred resolution deterministic despite `v1-2-3` names. Fold into 3.3.

**3.3 Consolidate the version state model (`currentRef`) (#234A).** Introduce one authoritative `status` runtime reference; make `status.apiVersion` / `versionInfo` / label **derived projections**; read served versions from the live CRD (already how pruning works). Shrinks the reconcile surface and removes drift-reconstruction.

### Tier 4 — ⚖ Strategic decisions — **DECIDED 2026-07 (Diego)**

These were the fork-vs-upstream forks in the road. Each compounds the next; all three are now settled. Directions below are the agreed outcome, to execute alongside Tiers 0–3.

**4.1 Migration philosophy: eager (F5) vs. coexistence (#234). → DECIDED: demote F5 to a strategy.**
Add `upgradePolicy: Automatic | Manual | Paused` (#222). Keep the current eager owner-scoped self-heal as the **`Automatic` default** (backward-compatible — existing CompositionDefinitions behave unchanged). `Manual` gates migration on an explicit `upgrade-to-version` trigger; `Paused` freezes. This keeps F5's capability while converging toward upstream's compatibility-first coexistence model — eager becomes *opt-in-by-default* rather than *mandatory*.
- Work: add `upgradePolicy` field to CompositionDefinition spec (default `Automatic`); branch `UpdateCompositionsVersion` on it; `Manual` reads an `upgrade-to-version` annotation; docs.

**4.2 create-pending: F8 recover vs. manual (#231). → DECIDED: keep F8, harden it, prioritize drain.**
Keep the auto-recovery, but (a) confirm the negative (`Observe` reports absent) with certainty before the "clear pending + create" branch fires — guard against eventual-consistency false-negatives that would duplicate an external resource; (b) land **graceful drain (1.1)** so stranding rarely happens in the first place, shrinking F8's blast radius to genuine edge cases.
- Work: harden F8's absent-branch (confirm-before-recreate) in unstructured-runtime; sequence after / together with 1.1.

**4.3 `composition-version` label name (#102/#104). → DECIDED: hold the name, consolidate the constant.**
Keep `krateo.io/composition-version` (upstream's #102 rename to `composition-parent-version` was reverted by #104 — no stable upstream target to track). Eliminate the still-open owner-label-duplication liability: move the cross-repo label constants (`CompositionDefinitionName/Namespace/…Label`, `CompositionVersionLabel`) into **one shared place** so core-provider and CDC stop declaring them independently (today they're byte-identical-by-contract across `meta.go` and `deploy.go`).
- Work: pick the home for the shared constants (candidate: a small `krateo.io/…/labels` package or the existing shared module both already import); replace the duplicated declarations + the CROSS-REPO CONTRACT comment with imports.

### Tier 5 — Larger / deferred

**5.1 Per-controller config + `krateo.io/unmanaged` opt-out (#230).** Escape hatch + declarative per-CDC overrides (rate limiter, workers, resync).
**5.2 CDC horizontal scaling / sharding (#193, #242).** Label-based sharding once drain (1.1) exists. Large.
**5.3 Shared deploy component** across core-provider/oasgen-provider (#230/#233). De-dup once the above stabilize.

---

## Scorecard

| Area | Fork status | Plan tier |
|---|---|---|
| Nested defaulting (#235) | 🟢 **fixed** in crdgen (guarded parent `default:{}`), live-verified on kind; pending tag v1.7.16 + core-provider bump | 0 |
| CDC graceful drain (#233/#231) | 🟢 **shipped** v1.3.2 (bounded in-flight drain, decoupled reconcile ctx); reviewed (1 blocker found+fixed), race-clean; CDC bumped | 1 |
| Mgmt-policy gating / grace period (#233) | 🟡 partial | 1 |
| Chart HA (resources/probes/PDB/HPA) (#242) | 🟡 gaps | 2 |
| `kubectl` version display (#234B) | 🟡 gap | 3 |
| Version state consolidation (#234A) | 🟡 gap | 3 |
| Version GC / pruning (#234) | 🟢 **done (ahead)** — live-verified safe on kind (S1–S5); caveat = unlabeled instances orphan (not delete), folds into policy-absence gap | — |
| `None` conversion / no certs (#235) | 🟢 **done (ahead)** | — |
| Migration philosophy (#234) | ⚖→✅ decided: `upgradePolicy`, F5=`Automatic` default | 4 |
| create-pending F8 (#231) | ⚖→✅ decided: keep+harden, drain-first | 4 |
| Label name (#102/#104) | ⚖→✅ decided: hold name, consolidate constant | 4 |
| Per-controller config / sharding (#230/#193) | ⚪ deferred | 5 |

**Recommended first three:** 0.1 (defaulting bug) → 1.1 (CDC drain) → 2.1/3.1 (chart HA + printer column). **Tier 4 is now decided** (see directions above) and folds into execution: 4.3's constant-consolidation rides with any CDC/label touch; 4.1/4.2 sequence after 1.1 (drain) lands.
