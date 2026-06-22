package loghandler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestNewJSONHandlerOTelFields(t *testing.T) {
	var buf bytes.Buffer
	slog.New(NewJSONHandler(slog.LevelInfo, &buf)).Warn("hi")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %v / %q", err, buf.String())
	}
	if n, _ := m["SeverityNumber"].(float64); int(n) != 13 { // WARN
		t.Fatalf("SeverityNumber for WARN want 13 got %v", m["SeverityNumber"])
	}
	if m["service.name"] != ServiceName {
		t.Fatalf("missing OTel service.name: %v", m)
	}
	if m["service"] != ServiceName {
		t.Fatalf("ingester-compat `service` missing: %v", m)
	}
	if _, ok := m["trace_id"]; ok {
		t.Fatalf("trace_id must be absent without a span")
	}
}

func TestNewJSONHandlerTraceBridge(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewJSONHandler(slog.LevelInfo, &buf))

	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("0102030405060708")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled})
	log.InfoContext(trace.ContextWithSpanContext(context.Background(), sc), "with span")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if m["trace_id"] != tid.String() {
		t.Fatalf("trace_id want %s got %v", tid, m["trace_id"])
	}
	if m["span_id"] != sid.String() {
		t.Fatalf("span_id want %s got %v", sid, m["span_id"])
	}
}
