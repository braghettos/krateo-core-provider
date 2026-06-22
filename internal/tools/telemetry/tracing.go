// Package telemetry provides core-provider's OpenTelemetry trace pipeline.
//
// core-provider's metrics + logging come from the upstream (un-forked) provider-runtime,
// which has no tracing; this is a small, self-contained trace setup that mirrors
// unstructured-runtime's SetupTracing rather than forking provider-runtime. The resource is
// resource.Default(), so service.name (OTEL_SERVICE_NAME) and service.version
// (OTEL_RESOURCE_ATTRIBUTES) are shared with the metrics pipeline via the standard OTel env.
// core-provider is the engine/operator, so it carries no composition-id of its own.
package telemetry

import (
	"context"

	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/krateoplatformops/core-provider"

// SetupTracing configures an OTLP trace pipeline and installs the W3C propagator. It is a
// no-op (returning a working no-op shutdown) when tracingEnabled is false, so callers wire it
// unconditionally and otel.Tracer(...).Start stays safe. Uses WithBatcher (NEVER WithSyncer):
// a slow or dead collector must never block a reconcile.
func SetupTracing(ctx context.Context, log logging.Logger, tracingEnabled bool) (func(context.Context) error, error) {
	// Install the propagator even when not exporting, so an inbound traceparent is honored.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	if !tracingEnabled {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.Default()),
	)
	otel.SetTracerProvider(tp)
	if log != nil {
		log.Info("OpenTelemetry tracing initialized")
	}
	return tp.Shutdown, nil
}

// Tracer returns core-provider's tracer (a no-op tracer until SetupTracing enables one).
func Tracer() trace.Tracer { return otel.Tracer(tracerName) }

// RecordError marks the span failed when err != nil; no-op otherwise.
func RecordError(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}
