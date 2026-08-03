# Chart Inspector

Chart Inspector is a service that provides endpoints for inspecting Helm charts. It allows users to retrieve resources from Helm charts and perform various operations on them.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [API](#api)

## Overview

Chart Inspector is a Krateo tool that enables the `composition-dynamic-controller` to generate its own RBAC policy. It returns a list of resources involved in a chart installation, considering the current cluster state using `helm template --server`, which evaluates lookups dynamically.

The `/resources` endpoint wraps the `http.RoundTripper` of the Helm client with a tracer that intercepts requests made to the Kubernetes API server. It then returns a list of resources involved in the chart installation.

## Architecture

![Chart Inspector Architecture](_diagrams/architecture.png "Chart Inspector Architecture")

## API

### API Endpoints

#### Retrieve Helm Chart Resources

- **Endpoint:** `/resources`
- **Method:** `GET`
- **Query Parameters (required):**
  - `compositionName` (string): The name of the Composition resource.
  - `compositionNamespace` (string): The namespace of the Composition resource.
  - `compositionDefinitionName` (string): The name of the CompositionDefinition resource.
  - `compositionDefinitionNamespace` (string): The namespace of the CompositionDefinition resource.
  - `compositionVersion` (string): The API version of the Composition (e.g. `v1alpha1`).
  - `compositionResource` (string): The plural resource name for Compositions (e.g. `compositions`).

- **Query Parameters (optional):**
  - `compositionGroup` (string): Composition group (default: `composition.krateo.io`).
  - `compositionDefinitionGroup` (string): CompositionDefinition group (default: `core.krateo.io`).
  - `compositionDefinitionVersion` (string): CompositionDefinition version (default: `v1alpha1`).
  - `compositionDefinitionResource` (string): CompositionDefinition resource name (default: `compositiondefinitions`).

- **Response:** JSON array of resources touched by the Helm chart template.

##### Example Request

```sh
curl "http://localhost:8081/resources?compositionName=my-composition&compositionNamespace=default&compositionDefinitionName=my-cd&compositionDefinitionNamespace=default&compositionVersion=v1alpha1&compositionResource=compositions"
```

### Swagger Documentation

Chart Inspector provides Swagger documentation for its API. You can access it at:

```
http://localhost:8081/swagger/
```

# Environment variables
Some environment variables affect the behavior of Chart Inspector and the components used in tests.

- `DEBUG`: If set (e.g. DEBUG=true) enables debug output used in tests and local runs. Default is false.
- `HELM_CHART_CACHE_DIR`:Directory where downloaded charts are temporarily stored. If not set, /tmp/helmchart-cache is used. The cache is used by getter.Get (getter.go) to avoid repeated downloads.