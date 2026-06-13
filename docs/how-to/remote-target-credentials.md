# How-to: credentials for remote deployment targets (with External Secrets Operator)

When a `CompositionDefinition` sets `spec.deploy.mode: Remote`, core-provider needs a
kubeconfig for the target cluster. core-provider is a **pure consumer of a native
Kubernetes Secret**: it reads the kubeconfig on every reconcile and re-reconciles
promptly when the Secret changes (a Secret watch is wired in the controller). It does
**not** mint or rotate credentials itself — that is delegated to your secret manager via
**External Secrets Operator (ESO)**.

## The contract

```yaml
apiVersion: core.krateo.io/v1alpha1
kind: CompositionDefinition
metadata:
  name: fireworksapp-remote
  namespace: demo-system
spec:
  chart:
    url: https://example.com/fireworks-app-0.1.0.tgz
  deploy:
    mode: Remote
    kubeconfigRef:
      name: prod-eu-kubeconfig   # a native Secret in the management cluster
      namespace: demo-system
      key: kubeconfig            # key holding a complete kubeconfig
```

The Secret value under `key` must be a complete kubeconfig that authenticates to the
target cluster. The bound identity needs permission in the target to create/update/delete
**CustomResourceDefinitions**, **RBAC** (ServiceAccounts/Roles/ClusterRoles + bindings),
and **Deployments** (the composition-dynamic-controller) — plus whatever the chart's
resources require.

## Rotation model

- **ESO owns rotation.** It syncs the kubeconfig (or a token rendered into a kubeconfig)
  from your backing store (Vault, AWS/GCP/Azure secret managers, …) into the Secret above,
  refreshing on `spec.refreshInterval`.
- **core-provider reacts.** It re-reads the Secret each reconcile and the controller's
  Secret watch enqueues the `CompositionDefinition` as soon as the Secret changes, so a
  rotation is picked up promptly rather than at the next poll.
- **No bespoke renewal loop** lives in core-provider (design decision): the management
  cluster never holds a standing token-minting process; that responsibility is ESO's.

## Recipe A — sync an existing kubeconfig from a secret store

Store a ready kubeconfig in your backing store (e.g. Vault key
`secret/clusters/prod-eu` field `kubeconfig`) and sync it:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: prod-eu-kubeconfig
  namespace: demo-system
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: SecretStore
  target:
    name: prod-eu-kubeconfig          # matches kubeconfigRef.name
    creationPolicy: Owner
  data:
  - secretKey: kubeconfig             # matches kubeconfigRef.key
    remoteRef:
      key: secret/clusters/prod-eu
      property: kubeconfig
```

## Recipe B — render a ServiceAccount token into a kubeconfig (rotating token)

When the backing store holds the target API endpoint, CA, and a (rotating) ServiceAccount
token separately, use ESO templating to assemble the kubeconfig. ESO re-renders on each
refresh, so token rotation flows straight through to core-provider:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: prod-eu-kubeconfig
  namespace: demo-system
spec:
  refreshInterval: 30m
  secretStoreRef:
    name: vault-backend
    kind: SecretStore
  target:
    name: prod-eu-kubeconfig
    creationPolicy: Owner
    template:
      engineVersion: v2
      data:
        kubeconfig: |
          apiVersion: v1
          kind: Config
          clusters:
          - name: prod-eu
            cluster:
              server: {{ .server }}
              certificate-authority-data: {{ .ca }}
          contexts:
          - name: prod-eu
            context: { cluster: prod-eu, user: core-provider }
          current-context: prod-eu
          users:
          - name: core-provider
            user:
              token: {{ .token }}
  data:
  - { secretKey: server, remoteRef: { key: secret/clusters/prod-eu, property: server } }
  - { secretKey: ca,     remoteRef: { key: secret/clusters/prod-eu, property: ca } }
  - { secretKey: token,  remoteRef: { key: secret/clusters/prod-eu, property: token } }
```

> Tip: provision the target ServiceAccount + RBAC and mint its (short-lived, rotating)
> token in the target cluster out-of-band (CI, a cluster-bootstrap job, or the cloud
> provider's IRSA/Workload-Identity flow), publishing the token to your secret store.
> core-provider only consumes the resulting kubeconfig.

## Conversion webhook note (multi-version CRDs)

For multi-version Compositions deployed to a remote target, also set
`CORE_PROVIDER_WEBHOOK_URL` to core-provider's externally reachable `/convert` endpoint
(its TLS cert must match the served webhook cert). Without it, remote CRDs are installed
with `NoneConverter` (no cross-version conversion). See
[`../design/multicluster-compositions.md`](../design/multicluster-compositions.md).
