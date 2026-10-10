package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/schemacheck"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/schemas"
)

// memLogs is an in-memory OTLP log exporter: it keeps every record, body and
// attributes, as text.
type memLogs struct {
	mu   sync.Mutex
	text strings.Builder
}

func (m *memLogs) Export(_ context.Context, recs []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range recs {
		fmt.Fprintf(&m.text, "%s ", recs[i].Body().String())
		recs[i].WalkAttributes(func(kv attribute.KeyValue) bool {
			fmt.Fprintf(&m.text, "%s=%s ", kv.Key, kv.Value.Emit())
			return true
		})
		m.text.WriteString("\n")
	}
	return nil
}
func (m *memLogs) Shutdown(context.Context) error   { return nil }
func (m *memLogs) ForceFlush(context.Context) error { return nil }
func (m *memLogs) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.text.String()
}

// TestPrivacyCanary pushes a unique sentinel through every field the event
// schema annotates x-free-text and asserts that it appears nowhere in the log
// file, an in-memory OTLP log exporter, the span attributes, /metrics or
// /healthz: free text MUST NOT reach telemetry. The test also fails when the
// schema gains a free-text field this test does not push, so a new one cannot
// slip past.
func TestPrivacyCanary(t *testing.T) {
	// The free-text fields of the schema, the same way the schema test lists them.
	s, err := schemacheck.Compile("event.schema.json", schemas.Event())
	if err != nil {
		t.Fatal(err)
	}
	var schemaFields []string
	for _, ty := range event.Types() {
		for _, p := range schemacheck.FreeTextFields(s, string(ty)) {
			schemaFields = append(schemaFields, string(ty)+"."+p)
		}
	}

	logs := &memLogs{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(logs)))
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	e := newEnv(t, options{otlp: otelslog.NewHandler(obs.ScopeName, otelslog.WithLoggerProvider(lp)), tracing: tp})

	sentinel := map[string]string{}
	mark := func(path string) string {
		v := "CANARY-" + strings.NewReplacer(".", "-", "[", "", "]", "").Replace(path) + "-ZX9Q"
		sentinel[path] = v
		return v
	}

	// period.changed.label, and task.skipped.reason through a shared rollover reason.
	e.ok("/api/v1/periods/change", map[string]any{
		"id": e.id(),
		"changes": []map[string]any{
			{"kind": "day", "start": "2026-10-07", "tz": "America/New_York", "label": mark("period.changed.label")},
		},
	})
	e.clock.Set(local(9, 0))
	e.ok("/api/v1/tasks/day:2026-10-07:plan-day/skip", map[string]any{"reason": mark("task.skipped.reason")})
	c := e.startCycle("review")
	e.clock.Set(local(9, 10))
	e.ok("/api/v1/cycles/annotate", map[string]any{
		"cycle_id": c, "note": mark("cycle.annotated.note"),
		"kv": []map[string]any{{"key": "pr", "value": mark("cycle.annotated.kv[].value")}},
	})

	events := func(typ string) []string {
		var out struct{ Events []struct{ ID string } }
		e.get("/api/v1/events?type="+typ).json(t, &out)
		var ids []string
		for _, ev := range out.Events {
			ids = append(ids, ev.ID)
		}
		return ids
	}
	skip := events("task.skipped")[0]
	annotated := events("cycle.annotated")[0]
	period := events("period.changed")[0]

	e.ok("/api/v1/events/"+skip+"/correct", map[string]any{
		"fields": map[string]any{"reason": mark("event.corrected.fields.reason")}, "reason": mark("event.corrected.reason"),
	})
	e.ok("/api/v1/events/"+annotated+"/correct", map[string]any{
		"fields": map[string]any{
			"note": mark("event.corrected.fields.note"),
			"kv":   []map[string]any{{"key": "pr", "value": mark("event.corrected.fields.kv[].value")}},
		},
	})
	// A period label is a correctable field of period.changed, which a retract of the lone event refuses; correct it.
	e.ok("/api/v1/events/"+period+"/correct", map[string]any{"fields": map[string]any{"label": mark("event.corrected.fields.label")}})
	e.clock.Set(local(9, 20))
	e.ok("/api/v1/events/"+annotated+"/retract", map[string]any{"reason": mark("event.retracted.reason")})

	// A refusal that carries text in its request must not log it either.
	e.refused("/api/v1/tasks/day:2026-10-07:nope/skip", map[string]any{"reason": "CANARY-REFUSED-ZX9Q"}, 404, "unknown_task")
	sentinel["refused"] = "CANARY-REFUSED-ZX9Q"

	// Every free-text path of the schema was pushed.
	var pushed []string
	for p := range sentinel {
		if p != "refused" {
			pushed = append(pushed, p)
		}
	}
	for _, f := range schemaFields {
		if sentinel[f] == "" {
			t.Errorf("the schema annotates %s as free text and this test does not push it", f)
		}
	}
	if len(pushed) != len(schemaFields) {
		t.Errorf("the test pushes %d fields, the schema has %d: %v vs %v", len(pushed), len(schemaFields), pushed, schemaFields)
	}

	// Where the sentinels MUST NOT appear.
	spanText := new(strings.Builder)
	for _, sp := range spans.GetSpans() {
		fmt.Fprintf(spanText, "%s ", sp.Name)
		for _, kv := range sp.Attributes {
			fmt.Fprintf(spanText, "%s=%s ", kv.Key, kv.Value.Emit())
		}
	}
	if spanText.Len() == 0 {
		t.Fatal("no span was recorded: the canary would be vacuous")
	}
	if !strings.Contains(e.log.String(), `"event_type":"task.skipped"`) {
		t.Fatalf("the mutation lines carry no event_type: the canary would be vacuous\n%s", e.log.String())
	}
	// Positive control: the sentinels did reach the log of record through the API, which returns text by design.
	all := string(e.get("/api/v1/events?view=original").Body)
	for path, v := range sentinel {
		if path != "refused" && !strings.Contains(all, v) {
			t.Errorf("the sentinel of %s never reached the log of record: the canary would be vacuous", path)
		}
	}
	sinks := map[string]string{
		"the log file":   e.log.String(),
		"the OTLP logs":  logs.String(),
		"the span attrs": spanText.String(),
		"/metrics":       string(e.raw("GET", "/metrics", nil, nil).Body),
		"/healthz":       string(e.raw("GET", "/healthz", nil, nil).Body),
		"/readyz":        string(e.raw("GET", "/readyz", nil, nil).Body),
	}
	for where, text := range sinks {
		for path, v := range sentinel {
			if strings.Contains(text, v) {
				t.Errorf("the sentinel of %s (%s) appears in %s", path, v, where)
			}
		}
	}
	_ = json.RawMessage(nil)
	_ = slog.LevelInfo
}
