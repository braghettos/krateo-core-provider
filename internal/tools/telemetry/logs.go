package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

// SetupOTLPLogs installs a global OTel LoggerProvider backed by an OTLP/HTTP log exporter, so log
// records emitted through provider-runtime's logging.NewOTelHandler are exported over OTLP alongside
// traces and metrics. Endpoint/headers come from the standard OTEL_EXPORTER_OTLP_* environment (the
// same as the trace exporter). It MUST be called before the log handler is built (the otelslog
// bridge captures the installed provider at construction). When enabled is false it is a no-op
// returning a nil-op shutdown, so callers can wire it unconditionally and gate on their OTEL flag.
func SetupOTLPLogs(ctx context.Context, enabled bool, serviceName string) (func(context.Context) error, error) {
	if !enabled {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", serviceName),
	))
	if err != nil {
		res = resource.Default()
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(res),
	)
	logglobal.SetLoggerProvider(lp)
	return lp.Shutdown, nil
}
