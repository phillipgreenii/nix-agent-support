package obs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/obs"
)

// OTLP goes only to the loopback collector.
func TestLoopbackEndpointsOnly(t *testing.T) {
	for ep, want := range map[string]bool{
		"http://127.0.0.1:4318":          true,
		"http://localhost:4317":          true,
		"127.0.0.1:4317":                 true,
		"http://[::1]:4318":              true,
		"https://127.0.0.2:4318/v1/logs": true,
		"http://collector.example:4318":  false,
		"http://10.0.0.5:4317":           false,
		"collector:4317":                 false,
		"http://127.0.0.1.evil.example/": false,
		"":                               false,
	} {
		if got := obs.Loopback(ep); got != want {
			t.Errorf("Loopback(%q) = %v, want %v", ep, got, want)
		}
	}
}

func TestClientLabelIsAClosedSet(t *testing.T) {
	for in, want := range map[string]string{"cli": "cli", "web": "web", "connector": "connector", "swiftbar": "swiftbar", "": "unknown", "curl": "unknown", "CLI": "unknown"} {
		if got := obs.NormalizeClient(in); got != want {
			t.Errorf("NormalizeClient(%q) = %q, want %q", in, got, want)
		}
	}
}

// Only the allow-listed attributes reach a span; a free-text one is dropped.
func TestSpanAttributesAreAllowListed(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	_, span := tp.Tracer("t").Start(context.Background(), "s")
	obs.Attrs(span, attribute.String("http.route", "/x"), attribute.String("note", "FREE TEXT"), attribute.String("reason", "unknown_task"), attribute.String("title", "T"))
	span.End()
	got := map[string]bool{}
	for _, a := range exp.GetSpans()[0].Attributes {
		got[string(a.Key)] = true
	}
	if !got["http.route"] || !got["reason"] || got["note"] || got["title"] {
		t.Errorf("attributes = %v", got)
	}
}

// The log carries component and, with a span in the context, trace_id and span_id; OTLP keeps WARN and above.
func TestLoggerFieldsAndOTLPLevel(t *testing.T) {
	var file bytes.Buffer
	var otlp recordingHandler
	logger := obs.NewLogger(&file, &otlp)
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	ctx, span := tp.Tracer("t").Start(context.Background(), "s")
	defer span.End()
	logger.InfoContext(ctx, "an info line", "route", "/x")
	logger.WarnContext(ctx, "a warning")
	logger.Debug("a debug line")

	var lines []map[string]any
	for _, l := range bytes.Split(bytes.TrimSpace(file.Bytes()), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("not JSON: %s", l)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("the file carries Info and above: %d lines", len(lines))
	}
	if lines[0]["component"] != "pg-task-focus" || lines[0]["trace_id"] == nil || lines[0]["span_id"] == nil {
		t.Errorf("fields = %v", lines[0])
	}
	if len(otlp.msgs) != 1 || otlp.msgs[0] != "a warning" {
		t.Errorf("OTLP got %v, want only the warning", otlp.msgs)
	}
}

type recordingHandler struct{ msgs []string }

func (r *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (r *recordingHandler) Handle(_ context.Context, rec slog.Record) error {
	r.msgs = append(r.msgs, rec.Message)
	return nil
}
func (r *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *recordingHandler) WithGroup(string) slog.Handler      { return r }
