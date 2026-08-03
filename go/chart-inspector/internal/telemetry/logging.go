package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// traceHandler is a slog.Handler decorator that enriches every record with the
// active span's trace_id and span_id (when a span is present in the context),
// so structured OTel-JSON logs correlate with traces in the backend. It wraps
// the existing plumbing logger handler, preserving its JSON shape; on the
// default-off path the base handler is used directly with zero overhead.
type traceHandler struct {
	slog.Handler
}

// WithTraceCorrelation wraps a slog.Handler so emitted records carry trace_id /
// span_id from the active span. It is only applied when telemetry is enabled;
// otherwise the original handler is returned unchanged.
func (p *Provider) WithTraceCorrelation(h slog.Handler) slog.Handler {
	if !p.TracingEnabled() {
		return h
	}
	return &traceHandler{Handler: h}
}

func (t *traceHandler) Handle(ctx context.Context, rec slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		rec.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return t.Handler.Handle(ctx, rec)
}

func (t *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{Handler: t.Handler.WithAttrs(attrs)}
}

func (t *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{Handler: t.Handler.WithGroup(name)}
}
