# Composition Dynamic Controller (CDC)

The **Composition Dynamic Controller (CDC)** is the execution engine of Krateo. It is a specialized operator that manages the full lifecycle of Helm-based services by watching and reconciling `Composition` resources.

## Key Features

- **Lifecycle Orchestration**: Manages the end-to-end deployment, updates, and deletion of services based on Helm charts.
- **Dynamic Reconciliation**: Automatically reconciles resource states, ensuring the live cluster matches the desired state defined in the `Composition`.
- **Chart Inspector Integration**: Leverages the Krateo Chart Inspector for secure dry-runs, ensuring chart validity and resource safety before application.
- **Declarative Status Projection**: Projects author-defined fields onto each `Composition`'s `.status` on every reconcile, and stamps `status.observedGeneration`.

## Status Projection

On each reconcile the CDC evaluates a set of declarative `${ jq }` mappings (the `statusDataTemplate`, shipped from core-provider via `COMPOSITION_CONTROLLER_STATUS_DATA_TEMPLATE` as a JSON array of `{forPath, expression}` items) and writes their results onto the `Composition`'s `.status`. Each expression runs over a combined source root:

- `self` / `spec` / `status` — the composition object itself (with `spec`/`status` as sugar for `self.spec`/`self.status`);
- `helm` — the managed Helm release's `url`, `version`, `status`, `revision`, and `name`;
- `api` — the resolved `apiRef` RESTAction (see below), present only when an `apiRef` is configured and resolution succeeds.

The CDC also stamps `status.observedGeneration` from `metadata.generation` every reconcile.

Projection is **degrade-only**: a failing or invalid mapping affects only its own field and never the baseline status, and an invalid `statusDataTemplate` simply disables projection. It is **skipped while a composition is gracefully paused**.

### `apiRef` `.api` source

When an `apiRef` is declared, the CDC resolves a single RESTAction through **snowplow**, under its **own** identity, and feeds the result in as the projection's `api` source. The resolution chain is:

1. **authn** (`internal/authn`) — the CDC reads its projected (audience `authn`) ServiceAccount token from disk and exchanges it at authn's `POST /serviceaccount/login` (TokenReview-validated) for a service JWT, caching the JWT until shortly before its expiry.
2. **snowplow** (`internal/snowplow`) — the CDC GETs snowplow's `/call?resource=restactions&...&extras=<json>` with `Authorization: Bearer <JWT>`, receiving the resolved RESTAction's `.status` (the keyed `.api.<callName>` map).
3. **resolver** (`internal/composition/apiresolver.go`) — per-instance composition context (`compositionName`, `compositionNamespace`, `compositionId`) is layered over the author-declared static `apiRef.extras` (request-wins) before resolution.

`.api` resolution is also degrade-only: if it fails, the `.api` source is simply absent (api-dependent mappings skip) while built-in and `helm`-sourced fields still project.

### Configuration

All status-projection and `apiRef` settings are env-driven and optional; an empty `apiRef` name disables `.api` resolution entirely:

| Env var | Description |
| --- | --- |
| `COMPOSITION_CONTROLLER_STATUS_DATA_TEMPLATE` | JSON-encoded `statusDataTemplate` (`[{forPath, expression}]`). |
| `COMPOSITION_CONTROLLER_API_REF_NAME` | Name of the RESTAction resolved for the `.api` source; empty disables `apiRef` resolution. |
| `COMPOSITION_CONTROLLER_API_REF_NAMESPACE` | Namespace of the `apiRef` RESTAction. |
| `COMPOSITION_CONTROLLER_API_REF_EXTRAS` | JSON object of static extras merged into the `apiRef` resolution. |
| `URL_SNOWPLOW` | Snowplow base URL for resolving RESTActions. |
| `URL_AUTHN` | Authn base URL for exchanging the SA token for a service JWT. |
| `COMPOSITION_CONTROLLER_SERVICEACCOUNT_TOKEN_PATH` | Path to the projected (authn-audience) SA token (default `/var/run/secrets/krateo.io/serviceaccount/token`). |

A real end-to-end test of the `apiRef` chain (real authn + real snowplow on kind) lives under [`hack/apiref-e2e/`](hack/apiref-e2e/README.md).

## Security & Operational Design

- **RBAC Enforcement**: Provisions specific, fine-grained RBAC policies for each managed composition, enforcing least-privilege principles at the instance level.
- **Graceful Lifecycle Management**: Supports advanced management features like service pausing/resuming and controlled Helm release versioning.
- **Observability**: Built-in support for OpenTelemetry to monitor reconciliation health and performance metrics.

## Documentation

For detailed guides, architecture diagrams, and full reference, visit the official documentation:
👉 **[https://docs.krateo.io](https://docs.krateo.io/key-concepts/kco/cdc/overview)**
