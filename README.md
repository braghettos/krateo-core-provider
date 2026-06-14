# Krateo Core Provider

The **Krateo Core Provider** is the foundation of Krateo Composable Operations (KCO). It enables the management of Helm charts as Kubernetes-native resources by automating versioned CRD generation, strict JSON schema validation, and fine-grained RBAC isolation.

## Key Features

- **Dynamic CRD Generation**: Automatically creates and manages versioned CRDs from Helm chart schemas.
- **Schema-Driven Validation**: Enforces strict input validation at the API level.
- **Orchestration**: Manages the lifecycle of Composition Dynamic Controllers (CDCs).
- **Local or remote deployment** (since 2.0.0): A `CompositionDefinition` deploys its Composition into the local management cluster by default, or into a remote cluster selected with `spec.deploy.targetRef` (a cluster-scoped `KubernetesTarget` pointing at a kubeconfig `Secret`). See [Remote deployment targets](docs/how-to/remote-target-credentials.md) and the [design doc](docs/design/multicluster-compositions.md).

## Requirements

- **Kubernetes ≥ 1.36** on the management cluster and on every remote target. Since 2.0.0 core-provider hosts no admission webhooks: generated CRDs use `None` conversion and the `krateo.io/composition-version` label is stamped in-apiserver by a `MutatingAdmissionPolicy` (GA `admissionregistration.k8s.io/v1`), which requires 1.36. The policy is shipped declaratively by the Helm chart, not installed by core-provider.

## Security by Design

- **Least-Privilege Access**: Supports the generation of fine-grained RBAC policies for managed compositions.
- **Validated Deployments**: Integrates with the Krateo Chart Inspector to perform dry-runs and validation before deployment.

## Quick Start

```sh
helm repo add krateo https://charts.krateo.io
helm repo update
helm install krateo-core-provider krateo/core-provider --namespace krateo-system --create-namespace
```

## Documentation

For detailed guides, architecture diagrams, and full reference, visit the official documentation:

👉 **[https://docs.krateo.io](https://docs.krateo.io/key-concepts/kco/core-provider/overview)**