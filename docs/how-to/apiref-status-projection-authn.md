# Enabling the `apiRef` status source (authn allowlist)

When a `CompositionDefinition` declares `spec.apiRef`, the composition-dynamic-controller
(CDC) resolves that RESTAction via snowplow **under its own identity** each reconcile, and
projects the result under `.api` for `statusDataTemplate` mappings to read.

To authenticate, the CDC presents a **projected ServiceAccount token** (audience `authn`) to
authn's `POST /serviceaccount/login`, which exchanges it for a short-lived service JWT. authn
only performs that exchange for ServiceAccounts that are on its **allowlist** — i.e. for which
a `serviceaccount.authn.krateo.io/ServiceAccount` mapping exists **in the authn operator
namespace** (e.g. `krateo-system`).

The token volume is mounted automatically by core-provider when `apiRef` is set
(`/var/run/secrets/krateo.io/serviceaccount/token`). What is **not** automatic (pending a
decision, see below) is the allowlist mapping.

## The per-composition ServiceAccount

core-provider creates one ServiceAccount **per composition**, named `<resource>-<apiVersion>`
in the **composition's namespace**. For a composition resource
`fireworksapps.composition.krateo.io/v1-0-0` deployed in namespace `apps`, the CDC SA is:

```
namespace: apps
name:      fireworksapps-v1-0-0
```

## The allowlist mapping (sample)

Create this in the **authn operator namespace** (where authn lists mappings), with
`serviceAccountRef` pointing at the composition's CDC SA:

```yaml
apiVersion: serviceaccount.authn.krateo.io/v1alpha1
kind: ServiceAccount
metadata:
  name: cdc-fireworksapps-v1-0-0          # becomes the issued identity's username
  namespace: krateo-system                # MUST be the authn operator namespace
spec:
  serviceAccountRef:
    namespace: apps                        # the composition's namespace
    name: fireworksapps-v1-0-0             # <resource>-<apiVersion>
  groups:
    - krateo:composition-dynamic-controller   # -> issued cert O= -> standard k8s RBAC
  displayName: "CDC (fireworksapps v1-0-0)"
```

`spec.groups` become the issued clientconfig certificate's `O=` (organization), so **standard
Kubernetes RBAC bound to those groups** scopes what the resolved RESTAction may read. Bind a
`ClusterRole` to the group with a normal `ClusterRoleBinding` (authn never authors RBAC):

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: krateo-cdc-restaction-read
subjects:
  - kind: Group
    name: krateo:composition-dynamic-controller
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: krateo-cdc-restaction-read         # grants the reads the RESTAction performs
  apiGroup: rbac.authorization.k8s.io
```

## Open decision: who creates the mapping?

Because the CDC SA is **dynamic** (one per composition), a static sample does not scale. Two
options:

1. **Manual / platform-managed** — the operator (or a higher-level controller) creates the
   mapping. core-provider stays out of authn's namespace. Simple, explicit, no new cross-
   namespace RBAC for core-provider.
2. **Auto-provisioned by core-provider** — when `apiRef` is set, core-provider also creates
   the mapping in the authn operator namespace (and deletes it on undeploy). Requires:
   core-provider config for the authn namespace, RBAC to write
   `serviceaccounts.serviceaccount.authn.krateo.io` there, and a **fixed group convention**
   (the authn design lists group conventions as still open).

This document uses `krateo:composition-dynamic-controller` as a placeholder group; the actual
convention is a platform decision.
