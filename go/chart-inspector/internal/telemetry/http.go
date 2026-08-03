package telemetry

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/krateo-platformops/chart-inspector"

// statusRecorder captures the response status code so the metrics middleware
// can label by outcome and count errors.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Instrument wraps an http.Handler with OTel server spans and request metrics,
// gated by the provider. When telemetry is disabled the handler is returned
// unchanged (byte-identical default-off path).
//
// route is the stable span/metric name for this endpoint (e.g. "/resources").
// Wrapping per-route lets us name spans without depending on Go 1.22 pattern
// metadata and keeps the heavy "/resources" inspection independently observable.
//
// otelhttp.NewHandler extracts the inbound W3C traceparent (set by the cdc's
// client span) via the global propagator, so the resulting server span is a
// child of the cdc span — completing the cdc -> chart-inspector trace chain.
func (p *Provider) Instrument(route string, h http.Handler) http.Handler {
	if p == nil || (!p.TracingEnabled() && !p.MetricsEnabled()) {
		return h
	}

	wrapped := h

	if p.MetricsEnabled() {
		wrapped = p.metricsMiddleware(route, wrapped)
	}

	if p.TracingEnabled() {
		wrapped = otelhttp.NewHandler(wrapped, route,
			otelhttp.WithFilter(healthFilter),
		)
	}

	return wrapped
}

// healthFilter excludes liveness/readiness probes from tracing so the trace
// backend isn't flooded with probe spans.
func healthFilter(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz", "/readyz":
		return false
	default:
		return true
	}
}

// metricsMiddleware records http.server request count, duration histogram and
// error count, labelled per endpoint and status class. Health probes are
// skipped to keep the signal focused on real inspection traffic.
func (p *Provider) metricsMiddleware(route string, next http.Handler) http.Handler {
	meter := otel.GetMeterProvider().Meter(meterName)

	requests, _ := meter.Int64Counter(
		"http.server.request.count",
		metric.WithDescription("Total number of HTTP requests handled."),
		metric.WithUnit("{request}"),
	)
	errors, _ := meter.Int64Counter(
		"http.server.request.errors",
		metric.WithDescription("Total number of HTTP requests that resulted in a 5xx response."),
		metric.WithUnit("{request}"),
	)
	duration, _ := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of HTTP request handling."),
		metric.WithUnit("s"),
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthFilter(r) {
			next.ServeHTTP(w, r)
			return
		}

		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start).Seconds()

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}

		attrs := metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("http.request.method", r.Method),
			attribute.Int("http.response.status_code", status),
		)

		ctx := r.Context()
		requests.Add(ctx, 1, attrs)
		duration.Record(ctx, elapsed, attrs)
		if status >= 500 {
			errors.Add(ctx, 1, attrs)
		}
	})
}
