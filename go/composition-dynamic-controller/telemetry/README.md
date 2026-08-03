# Telemetry Assets

This folder contains ready-to-use telemetry assets for `composition-dynamic-controller`.

- `dashboards/composition-dynamic-controller.dashboard.json`: Grafana dashboard with metric panels
- `collector/otel-collector-config.yaml`: minimal OpenTelemetry Collector config (OTLP HTTP -> Prometheus endpoint)
- `otelcol-values.yaml`: Helm values for OpenTelemetry Collector deployment
- `metrics-reference.md`: metric catalog with example queries

## Prerequisites

1. Run `composition-dynamic-controller` with OpenTelemetry enabled:

```yaml
OTEL_ENABLED: "true"
OTEL_EXPORT_INTERVAL: "30s"
OTEL_EXPORTER_OTLP_ENDPOINT: "http://otel-collector.monitoring.svc.cluster.local:4318"
```

2. Make the OpenTelemetry Collector reachable from `composition-dynamic-controller`.
3. Configure Prometheus to scrape the Collector Prometheus exporter on `0.0.0.0:9464`.
4. Connect Grafana to Prometheus as a data source.

## Import The Dashboard

1. Open Grafana.
2. Go to `Dashboards` -> `New` -> `Import`.
3. Upload `dashboards/composition-dynamic-controller.dashboard.json`.
4. Select your Prometheus data source.
5. Save.

## Example Panels Included

- **Queue Status**: Queue depth and in-flight reconciliations
- **Success Metrics**: Startup success/failure counters, reconciliation success rate (15m)
- **Queue Performance**: Queue wait latency (p95), queue work duration (p95), oldest item age (p95)
- **Reconciliation Latency**: Overall reconcile duration (p95)
- **Operation Latencies**: External operations (observe, connect, create, update, delete), finalizer operations, status updates
- **Error Tracking**: Operation failure counts grouped by operation type
- **Requeue Tracking**: Requeue reasons and failure requeue counts

## Metric Naming Notes

Prometheus normalizes metric names with underscores:
- `unstructured_runtime.queue.depth` → `unstructured_runtime_reconcile_queue_depth`
- Counters get `_total` suffix: `unstructured_runtime_reconcile_success_total`
- Histograms expose `_bucket`, `_sum`, `_count` series

Use `_sum / _count` for average latency, not `_bucket` quantiles.

## OpenTelemetry Collector Setup

The collector receives metrics on HTTP port 4318 (OTLP) and exports to Prometheus on port 9464.

```bash
helm install otel-collector open-telemetry/opentelemetry-collector \
  -n monitoring \
  -f otelcol-values.yaml
```

## Troubleshooting

- Check collector logs: `kubectl logs -n monitoring deployment/otel-collector`
- Verify Prometheus can scrape: `http://<collector-service>:9464/metrics`
- Confirm OTEL endpoint is reachable from the controller pod
- Check controller logs for `OpenTelemetry metrics initialized` message

- counters may appear as `<metric>_total`
- histograms usually appear as `<metric>_bucket`, `<metric>_sum`, `<metric>_count`

The dashboard uses Prometheus-style queries for the normalized metric names.
If your environment differs, edit the panel queries accordingly.

The webhook latency panels use the histogram `_sum` and `_count` series to show average duration, while the traffic panels use `core_provider_webhook_request_total` filtered by `webhook="mutating"` and `webhook="conversion"`.

Webhook panels stay empty until all of the following are true:

- `OTEL_ENABLED` is set to `true`.
- `OTEL_EXPORTER_OTLP_ENDPOINT` points to a reachable Collector.
- The webhook server receives real mutating or conversion admission requests.

If you only restart the controller or reconcile resources, those panels will still remain blank because webhook metrics are only emitted during admission traffic.

## Collector Example

Use [otelcol-values.yaml](otelcol-values.yaml) as the Helm values file.

The file already includes the Collector config, so you usually only need to adjust the image tag or the target environment.

If you need the raw Collector pipeline on its own, start from [collector/otel-collector-config.yaml](collector/otel-collector-config.yaml) and adapt it to your deployment.

Current pipeline in the example:

- Receiver: OTLP HTTP on `4318`
- Processor: `batch`
- Exporter: Prometheus endpoint on `9464`

## Deploying OpenTelemetry Collector (Helm)

You can deploy a shared Collector in-cluster with the official Helm chart.

1. Add Helm repo:

```bash
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo update
```

2. Use [otelcol-values.yaml](otelcol-values.yaml) as the Helm values file.

The values file exposes these ports:

```yaml
ports:
  otlp-http:
    enabled: true
    containerPort: 4318
    servicePort: 4318
    protocol: TCP
  prom-metrics:
    enabled: true
    containerPort: 9464
    servicePort: 9464
    protocol: TCP
```

3. Install Collector:

```bash
helm upgrade --install otel-collector open-telemetry/opentelemetry-collector \
	-n monitoring --create-namespace \
	-f otelcol-values.yaml
```

4. The chart creates the ServiceMonitor automatically, so Prometheus can scrape the Collector metrics endpoint.

5. Point `core-provider` to the Collector service:

```yaml
OTEL_ENABLED: "true"
OTEL_EXPORT_INTERVAL: "30s"
OTEL_EXPORTER_OTLP_ENDPOINT: "http://otel-collector-opentelemetry-collector.monitoring.svc.cluster.local:4318"
```

6. Quick checks:

```bash
kubectl -n monitoring get pods
kubectl -n monitoring get svc
```

Then ensure Prometheus scrapes the Collector's `:9464` endpoint.
