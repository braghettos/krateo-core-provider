// Package loghandler provides the slog handler used across the core-provider.
//
// It emits logs as one JSON object per line on the given writer in the
// OpenTelemetry log model: the time field rendered as `timestamp` in RFC3339Nano
// UTC, a SeverityNumber alongside the level, persistent `service`/`service.name`
// attributes, and trace_id/span_id whenever a span is in the record's context.
// This format is required by logs-ingester, which discards any line that is not a
// valid JSON object and parses `timestamp` with time.Parse(time.RFC3339Nano, ...).
// See docs/log-ingester-compatibility.md.
//
// core-provider keeps this handler self-contained (it does not share
// unstructured-runtime's pkg/logging) because its logging plumbing comes from the
// upstream, un-forked provider-runtime; the OTel additions here mirror
// unstructured-runtime's NewOTelJSONHandler.
package loghandler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// ServiceName is emitted as the `service`/`service.name` attribute on every log
// line and is mapped to the service_name column by logs-ingester.
const ServiceName = "core-provider"

// otelSeverityNumber maps an slog.Level to the OpenTelemetry SeverityNumber.
// (TRACE=1, DEBUG=5, INFO=9, WARN=13, ERROR=17.)
func otelSeverityNumber(l slog.Level) int {
	switch {
	case l < slog.LevelDebug:
		return 1
	case l < slog.LevelInfo:
		return 5
	case l < slog.LevelWarn:
		return 9
	case l < slog.LevelError:
		return 13
	default:
		return 17
	}
}

// NewJSONHandler returns a slog.Handler that writes one JSON object per line to w
// (defaulting to os.Stderr when nil) in the OTel log model: `timestamp`
// RFC3339Nano UTC, the level (SeverityText) plus a sibling SeverityNumber, the
// `service`/`service.name` resource attributes, and trace_id/span_id whenever a
// span is present in the record's context.
func NewJSONHandler(level slog.Leveler, w io.Writer) slog.Handler {
	if w == nil {
		w = os.Stderr
	}

	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:     level,
		AddSource: false,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Only rewrite the top-level time field, not nested attributes that
			// happen to share the key.
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.String("timestamp", a.Value.Time().UTC().Format(time.RFC3339Nano))
			}
			return a
		},
	})

	base := h.WithAttrs([]slog.Attr{
		slog.String("service.name", ServiceName),
		// `service` kept alongside the OTel `service.name` during the logs-ingester transition.
		slog.String("service", ServiceName),
	})
	return &otelHandler{Handler: base}
}

// otelHandler enriches each record with SeverityNumber and, when a span is in
// context, trace_id/span_id — without polluting the Body.
type otelHandler struct {
	slog.Handler
}

func (o *otelHandler) Handle(ctx context.Context, rec slog.Record) error {
	rec.AddAttrs(slog.Int("SeverityNumber", otelSeverityNumber(rec.Level)))
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		rec.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return o.Handler.Handle(ctx, rec)
}

func (o *otelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &otelHandler{Handler: o.Handler.WithAttrs(attrs)}
}

func (o *otelHandler) WithGroup(name string) slog.Handler {
	return &otelHandler{Handler: o.Handler.WithGroup(name)}
}
