// Package telemetry wires up OpenTelemetry tracing and metrics for the
// chart-inspector HTTP service. Everything is gated behind environment
// variables and defaults to OFF: when disabled, Setup registers nothing
// (no exporters, no providers, no global propagator) so the runtime
// behaviour is byte-identical to a build without telemetry.
//
// chart-inspector is a stateless HTTP service and is reconciled-by-nobody,
// so there is no reconcile-throughput signal. Its value in the observability
// graph is completing the cdc -> chart-inspector trace chain: by extracting
// the inbound W3C traceparent on its handlers (see Middleware), the cdc's
// client span becomes the parent of the chart-inspector server span.
package telemetry

import (
	"context"
	"fmt"

	"github.com/krateo-platformops/plumbing/env"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ServiceName is the value reported as the OTel resource service.name.
const ServiceName = "chart-inspector"

// Config captures the resolved telemetry configuration. It is derived from the
// environment by ConfigFromEnv and is safe to inspect even when telemetry is
// disabled.
type Config struct {
	// Enabled is the master switch. When false, Setup is a no-op.
	Enabled bool
	// TracingEnabled additionally gates span creation/export. It is implied by
	// Enabled but can be turned off independently via OTEL_TRACING_ENABLED=false.
	TracingEnabled bool
	// MetricsEnabled gates the metrics pipeline. Defaults to Enabled.
	MetricsEnabled bool
	// Endpoint is the OTLP/HTTP collector endpoint (host:port, no scheme).
	Endpoint string
	// Insecure controls whether the OTLP/HTTP exporter uses plaintext.
	Insecure bool
}

// ConfigFromEnv resolves the telemetry configuration from the environment.
//
// Env contract (all default to OFF):
//   - OTEL_ENABLED            master switch (default false)
//   - OTEL_TRACING_ENABLED    gate tracing only  (default: value of OTEL_ENABLED)
//   - OTEL_METRICS_ENABLED    gate metrics only  (default: value of OTEL_ENABLED)
//   - OTEL_EXPORTER_OTLP_ENDPOINT   collector endpoint host:port (default localhost:4318)
//   - OTEL_EXPORTER_OTLP_INSECURE   use plaintext OTLP/HTTP (default true)
func ConfigFromEnv() Config {
	enabled := env.Bool("OTEL_ENABLED", false)
	return Config{
		Enabled:        enabled,
		TracingEnabled: env.Bool("OTEL_TRACING_ENABLED", enabled),
		MetricsEnabled: env.Bool("OTEL_METRICS_ENABLED", enabled),
		Endpoint:       env.String("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4318"),
		Insecure:       env.Bool("OTEL_EXPORTER_OTLP_INSECURE", true),
	}
}

// ShutdownFunc flushes and tears down any telemetry providers registered by
// Setup. It is always non-nil and always safe to call (a no-op when telemetry
// is disabled).
type ShutdownFunc func(context.Context) error

// Provider is the live telemetry state returned by Setup. It exposes whether
// tracing/metrics are active so callers can gate instrumentation, and a single
// Shutdown that flushes every registered pipeline.
type Provider struct {
	cfg       Config
	shutdowns []ShutdownFunc
}

// TracingEnabled reports whether server spans should be created/exported.
func (p *Provider) TracingEnabled() bool { return p != nil && p.cfg.Enabled && p.cfg.TracingEnabled }

// MetricsEnabled reports whether request metrics should be recorded/exported.
func (p *Provider) MetricsEnabled() bool { return p != nil && p.cfg.Enabled && p.cfg.MetricsEnabled }

// Shutdown flushes and stops all registered telemetry pipelines.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var err error
	for _, fn := range p.shutdowns {
		if fn == nil {
			continue
		}
		if e := fn(ctx); e != nil {
			err = e
		}
	}
	return err
}

// Setup initialises OpenTelemetry according to the resolved Config. When
// telemetry is disabled it registers nothing and returns a Provider whose
// Shutdown is a no-op, preserving byte-identical default-off behaviour.
//
// When enabled it installs:
//   - the global W3C TraceContext + Baggage propagator (so inbound traceparent
//     from the cdc is honoured and outbound requests carry context),
//   - an OTLP/HTTP trace exporter + batching TracerProvider (if tracing on),
//   - an OTLP/HTTP metric exporter + periodic-reader MeterProvider (if metrics on),
//
// all bound to an OTel resource with service.name=chart-inspector.
func Setup(ctx context.Context, cfg Config) (*Provider, error) {
	p := &Provider{cfg: cfg}

	if !cfg.Enabled {
		// Default-off: register nothing, leave the global providers/propagator
		// at their no-op defaults.
		return p, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(ServiceName)),
	)
	if err != nil {
		return p, fmt.Errorf("building otel resource: %w", err)
	}

	// W3C propagation: required so the cdc's inbound traceparent parents our
	// server span, and so any outbound calls propagate the active context.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if cfg.TracingEnabled {
		traceOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
		}
		exp, err := otlptracehttp.New(ctx, traceOpts...)
		if err != nil {
			return p, fmt.Errorf("creating otlp trace exporter: %w", err)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		p.shutdowns = append(p.shutdowns, tp.Shutdown)
	}

	if cfg.MetricsEnabled {
		metricOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
		}
		mexp, err := otlpmetrichttp.New(ctx, metricOpts...)
		if err != nil {
			return p, fmt.Errorf("creating otlp metric exporter: %w", err)
		}
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp)),
		)
		otel.SetMeterProvider(mp)
		p.shutdowns = append(p.shutdowns, mp.Shutdown)
	}

	return p, nil
}
