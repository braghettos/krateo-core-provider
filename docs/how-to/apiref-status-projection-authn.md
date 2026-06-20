# The `apiRef` status source (authn allowlist)

When a `CompositionDefinition` declares `spec.apiRef`, the composition-dynamic-controller
(CDC) resolves that RESTAction via snowplow **under its own identity** each reconcile, and
projects the result under `.api` for `statusDataTemplate` mappings to read.

To authenticate, the CDC presents a **projected ServiceAccount token** (audience `authn`) to
authn's `POST /serviceaccount/login`, which exchanges it for a short-lived service JWT. authn
only performs that exchange for ServiceAccounts that are on its **allowlist** — i.e. for which
a `serviceaccount.authn.krateo.io/ServiceAccount` mapping exists **in the authn operator
namespace** (e.g. `krateo-system`).

Both pieces are **provisioned automatically by core-provider** when `apiRef` is set:

1. the projected token volume on the CDC Deployment
   (`/var/run/secrets/krateo.io/serviceaccount/token`); and
2. the authn allowlist mapping (this document).

The only step left to the platform operator is binding RBAC to the issued group (see below) —
authn never authors RBAC.

## The per-composition ServiceAccount

core-provider creates one ServiceAccount **per composition**, named `<resource>-<apiVersion>`
in the **composition's namespace**. For a composition resource
`fireworksapps.composition.krateo.io/v1-0-0` deployed in namespace `apps`, the CDC SA is:

```
namespace: apps
name:      fireworksapps-v1-0-0
```

## The allowlist mapping (auto-created)

When `apiRef` is set, core-provider creates this in the authn operator namespace
(`COMPOSITION_AUTHN_NAMESPACE`, default `krateo-system`) and deletes it on undeploy:

```yaml
apiVersion: serviceaccount.authn.krateo.io/v1alpha1
kind: ServiceAccount
metadata:
  name: cdc-apps-fireworksapps-v1-0-0       # cdc-<compositionNamespace>-<resource>-<apiVersion>
  namespace: krateo-system                  # the authn operator namespace
spec:
  serviceAccountRef:
    namespace: apps                          # the composition's namespace
    name: fireworksapps-v1-0-0               # <resource>-<apiVersion>
  groups:
    - krateo:cdc:fireworksapps-v1-0-0        # krateo:cdc:<resource>-<apiVersion>
  displayName: "CDC (apps/fireworksapps-v1-0-0)"
```

This requires core-provider to have manage rights on
`serviceaccounts.serviceaccount.authn.krateo.io` (granted by the core-provider chart
ClusterRole) and to know the authn namespace (`COMPOSITION_AUTHN_NAMESPACE`).

## Binding RBAC to the issued identity (operator step)

`spec.groups` become the issued clientconfig certificate's `O=` (organization), so **standard
Kubernetes RBAC bound to that group** scopes what the resolved RESTAction may read. The group
is **per composition** — `krateo:cdc:<resource>-<apiVersion>` — so each composition type can be
granted exactly the reads its RESTAction performs:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: krateo-cdc-fireworksapps-restaction-read
subjects:
  - kind: Group
    name: krateo:cdc:fireworksapps-v1-0-0
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: krateo-cdc-fireworksapps-restaction-read   # grants the reads the RESTAction performs
  apiGroup: rbac.authorization.k8s.io
```

## Configuration summary

| Setting | Where | Default |
| --- | --- | --- |
| authn operator namespace | `COMPOSITION_AUTHN_NAMESPACE` (core-provider) | `krateo-system` |
| token audience | CDC Deployment projected volume | `authn` |
| token path | CDC env `COMPOSITION_CONTROLLER_SERVICEACCOUNT_TOKEN_PATH` | `/var/run/secrets/krateo.io/serviceaccount/token` |
| snowplow / authn URLs | CDC env `URL_SNOWPLOW` / `URL_AUTHN` | in-cluster service DNS |
| issued group | derived | `krateo:cdc:<resource>-<apiVersion>` |
