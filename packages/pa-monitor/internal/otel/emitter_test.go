package otel

import (
	"context"
	"testing"
	"time"

	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
)

// capturingLogger is a minimal in-memory otellog.Logger that records every
// emitted Record, so tests can assert what LogEvent produces without a live
// OTLP collector. embedded.Logger satisfies the forward-compat sentinel the
// otellog.Logger interface embeds.
type capturingLogger struct {
	embedded.Logger
	records []otellog.Record
}

func (c *capturingLogger) Emit(_ context.Context, r otellog.Record) {
	c.records = append(c.records, r)
}

func (c *capturingLogger) Enabled(context.Context, otellog.EnabledParameters) bool { return true }

// TestEmitter_LogEvent_ProducesRecord proves the baseline-liveness emit path
// (daemon.started / daemon.heartbeat both route through LogEvent): a record is
// produced at info severity with body=name, an event_name attribute, every
// non-empty attr, and empty-valued attrs dropped.
func TestEmitter_LogEvent_ProducesRecord(t *testing.T) {
	cl := &capturingLogger{}
	e := &Emitter{logger: cl}

	e.LogEvent("daemon.heartbeat", map[string]string{
		"plan_tier":        "max_5x",
		"sessions_working": "2",
		"auto_resume":      "true",
		"five_hour_pct":    "", // empty -> must be dropped
	})

	if len(cl.records) != 1 {
		t.Fatalf("records = %d, want 1", len(cl.records))
	}
	rec := cl.records[0]
	if rec.Body().AsString() != "daemon.heartbeat" {
		t.Errorf("body = %q, want daemon.heartbeat", rec.Body().AsString())
	}
	if rec.Severity() != otellog.SeverityInfo {
		t.Errorf("severity = %v, want Info", rec.Severity())
	}
	got := map[string]string{}
	rec.WalkAttributes(func(kv otellog.KeyValue) bool {
		got[string(kv.Key)] = kv.Value.AsString()
		return true
	})
	if got["event_name"] != "daemon.heartbeat" {
		t.Errorf("event_name attr = %q, want daemon.heartbeat", got["event_name"])
	}
	for k, want := range map[string]string{"plan_tier": "max_5x", "sessions_working": "2", "auto_resume": "true"} {
		if got[k] != want {
			t.Errorf("attr %q = %q, want %q (all=%v)", k, got[k], want, got)
		}
	}
	if _, ok := got["five_hour_pct"]; ok {
		t.Errorf("empty-valued attr five_hour_pct should be dropped, got %q", got["five_hour_pct"])
	}
}

// TestEmitter_RecordNudgeSendFailed_EmitsWarnEndToEnd is the END-TO-END WARN
// assertion that ties together the two lower-level pieces already covered in
// isolation: TestSeverityForEvent (the name→severity map) and
// TestEmitter_LogEvent_ProducesRecord (LogEvent for an INFO event). It drives
// the full path from a producer's ingestion point through to the emitted
// record: RecordNudgeSendFailed → LogEvent → severityForEvent("nudge.send_failed")
// → a Record carrying SeverityWarn. Regressing ANY link (the severity map, the
// LogEvent severity wiring, or the producer's event name) fails this. The rich
// log attrs (session_id, error) must also reach the record.
func TestEmitter_RecordNudgeSendFailed_EmitsWarnEndToEnd(t *testing.T) {
	cl := &capturingLogger{}
	e := &Emitter{logger: cl} // no SDK counter; RecordNudgeSendFailed is nil-safe on it

	e.RecordNudgeSendFailed(
		map[string]string{"reason": "other"},
		map[string]string{"session_id": "sid-1", "error": "boom"},
	)

	if len(cl.records) != 1 {
		t.Fatalf("records = %d, want 1", len(cl.records))
	}
	rec := cl.records[0]
	if rec.Severity() != otellog.SeverityWarn {
		t.Errorf("severity = %v, want Warn (nudge.send_failed is an error-like event)", rec.Severity())
	}
	if rec.Body().AsString() != "nudge.send_failed" {
		t.Errorf("body = %q, want nudge.send_failed", rec.Body().AsString())
	}
	got := map[string]string{}
	rec.WalkAttributes(func(kv otellog.KeyValue) bool {
		got[string(kv.Key)] = kv.Value.AsString()
		return true
	})
	if got["event_name"] != "nudge.send_failed" {
		t.Errorf("event_name attr = %q, want nudge.send_failed", got["event_name"])
	}
	if got["session_id"] != "sid-1" {
		t.Errorf("logAttr session_id = %q, want sid-1 (rich log attrs must propagate to the record)", got["session_id"])
	}
}

func TestNew_NilWhenEndpointEmpty(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	e, err := New(context.Background(), Options{ServiceName: "test"})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if e != nil {
		t.Fatal("expected nil emitter when endpoint unset")
	}
}

// TestNew_WiresExportHealth constructs a real Emitter (endpoint set, no live
// collector) and asserts New wires the export-health decorators and the emitter
// starts healthy. Exporter construction does not dial, so this is offline-safe,
// mirroring the connection-emitter construction test.
func TestNew_WiresExportHealth(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")
	e, err := New(context.Background(), Options{ServiceName: "pa-monitor", ServiceVersion: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e == nil {
		t.Fatal("want non-nil emitter when endpoint set")
	}
	defer func() { _ = e.Shutdown(context.Background()) }()
	if e.health == nil {
		t.Fatal("New should wire an exportHealth tracker")
	}
	if !e.ExportHealthy() {
		t.Error("emitter should start healthy before any export attempt")
	}
}

// TestExportHealthy_NilSafe: a nil emitter (OTel disabled) reports healthy.
func TestExportHealthy_NilSafe(t *testing.T) {
	var e *Emitter
	if !e.ExportHealthy() {
		t.Error("nil emitter should report healthy")
	}
}

func TestEmitter_NilSafeMethods(t *testing.T) {
	var e *Emitter
	// Methods MUST not panic on nil receiver.
	e.RecordCaffeinateActive(false, "off", 0, nil)
	e.RecordBlockCost(3.14, map[string]string{"plan_tier": "max_5x"})
	e.RecordWeekCost(42.0, nil)
	e.RecordBlockLimitHit(map[string]string{"plan_tier": "max_5x"})
	e.RecordWeekLimitHit(nil)
	e.RecordCaffeinateRound(nil)
	e.RecordCaffeinateGraceExpired(nil)
	e.RecordContextLimitHit(nil)
	e.RecordNudgeSent(nil)
	e.RecordNudgeSendFailed(nil, nil)
	e.RecordNudgeSendFailed(map[string]string{"reason": "other"}, map[string]string{"session_id": "sid", "error": "boom"})
	e.RecordNudgeSuppressed(nil)
	e.RecordNudgeQueued(nil)
	e.RecordNudgeDroppedNoBridge(nil)
	e.RecordApiErrorObserved(nil)
	e.RecordSessionsErrored(nil)
	e.RecordNudgeDeferred(nil)
	e.RecordNudgeDeferred(map[string]int{"window_pending": 2})
	e.RecordSessionInfo(nil)
	e.RecordSessionInfo([]SessionInfo{{SessionID: "sid"}})
	e.LogEvent("test.event", nil)
	if err := e.Shutdown(context.Background()); err != nil {
		t.Errorf("nil Shutdown returned error: %v", err)
	}
}

func TestEmitter_RecordSessionsErrored_NilSafe(t *testing.T) {
	var e *Emitter
	// nil map should not panic
	e.RecordSessionsErrored(map[ErroredKey]int{{Kind: "rate_limit", IsTerminal: true}: 2})
}

func TestEmitter_RecordApiErrorObserved_NilSafe(t *testing.T) {
	var e *Emitter
	e.RecordApiErrorObserved(map[string]string{
		"session_id":  "sid-1",
		"kind":        "rate_limit",
		"is_terminal": "true",
	})
}

func TestEmitter_RecordNudgeSuppressed_NilSafe(t *testing.T) {
	var e *Emitter
	e.RecordNudgeSuppressed(map[string]string{
		"session_id": "sid-1",
		"sources":    "disrupted",
		"cause":      "session_active",
	})
}

func TestEmitter_RecordNudgeSent_WithLabels_NilSafe(t *testing.T) {
	var e *Emitter
	e.RecordNudgeSent(map[string]string{
		"session_id": "sid-1",
		"sources":    "disrupted,window_reset",
		"error_kind": "server_error",
		"escalated":  "false",
	})
}

// TestEmitter_RecordSessionInfo_RoundTrip exercises the buffer path: rows
// passed in MUST land in sessionInfoObs with the right tokens/cost values
// and a flattened attribute set carrying every documented column key.
//
// We construct a bare Emitter (no OTel SDK) to keep this a pure logic test;
// the meter callback only runs when an exporter is configured, which is
// covered by the integration paths.
func TestEmitter_RecordSessionInfo_RoundTrip(t *testing.T) {
	e := &Emitter{}
	rows := []SessionInfo{
		{
			SessionID:    "sid-aaa",
			SessionName:  "feature-x",
			Cwd:          "/home/me/repo",
			TerminalHost: "CMUX",
			Status:       "working",
			Model:        "claude-opus-4-7",
			ErrorKind:    "", // no terminal error
			Tokens:       12345,
			CostUSD:      0.42,
			Labels: map[string]string{
				"plan_tier":       "max_5x",
				"workspace.scope": "personal",
				"session_id":      "ignored-overwrite-attempt", // must NOT win
			},
		},
		{
			SessionID:    "sid-bbb",
			SessionName:  "",
			Cwd:          "/tmp/other",
			TerminalHost: "TMUX",
			Status:       "idle",
			Model:        "claude-sonnet-4-7",
			ErrorKind:    "rate_limit",
			Tokens:       0,
			CostUSD:      0,
		},
	}
	e.RecordSessionInfo(rows)
	if got := len(e.sessionInfoObs); got != 2 {
		t.Fatalf("sessionInfoObs len = %d, want 2", got)
	}
	// Row 0 — values
	r0 := e.sessionInfoObs[0]
	if r0.sessionID != "sid-aaa" || r0.tokens != 12345 || r0.costUSD != 0.42 {
		t.Errorf("row0 = {sid:%s tokens:%d cost:%v}, want {sid-aaa 12345 0.42}",
			r0.sessionID, r0.tokens, r0.costUSD)
	}
	got0 := map[string]string{}
	for _, kv := range r0.attrs {
		got0[string(kv.Key)] = kv.Value.AsString()
	}
	wantKeys := []string{"session_id", "session_name", "cwd", "terminal_host", "status", "model", "plan_tier", "workspace.scope"}
	for _, k := range wantKeys {
		if got0[k] == "" {
			t.Errorf("row0 attr %q missing, got attrs=%v", k, got0)
		}
	}
	// error_kind empty MUST be dropped (attrsToKV skips empty values).
	if _, ok := got0["error_kind"]; ok {
		t.Errorf("row0 error_kind should be omitted when empty, got %q", got0["error_kind"])
	}
	if got0["session_id"] != "sid-aaa" {
		t.Errorf("caller label overrode column key: session_id=%q", got0["session_id"])
	}
	// Row 1 — error_kind populated, no caller labels.
	r1 := e.sessionInfoObs[1]
	got1 := map[string]string{}
	for _, kv := range r1.attrs {
		got1[string(kv.Key)] = kv.Value.AsString()
	}
	if got1["error_kind"] != "rate_limit" {
		t.Errorf("row1 error_kind = %q, want rate_limit", got1["error_kind"])
	}
	if got1["status"] != "idle" {
		t.Errorf("row1 status = %q, want idle", got1["status"])
	}
}

// TestEmitter_RecordCaffeinateActive_StateAttr verifies the process `state`
// attribute and grace-remaining value are buffered. The gauge VALUE encodes
// the MODE (active), while `state` carries the PROCESS — so "armed but not
// holding" (active=true, state=off) is distinguishable from "holding".
func TestEmitter_RecordCaffeinateActive_StateAttr(t *testing.T) {
	e := &Emitter{}
	e.RecordCaffeinateActive(true, "grace", 42, map[string]string{"plan_tier": "max_5x"})
	if e.caffeinateActiveVal != 1 {
		t.Errorf("caffeinateActiveVal = %d, want 1 (mode active)", e.caffeinateActiveVal)
	}
	if e.caffeinateGraceVal != 42 {
		t.Errorf("caffeinateGraceVal = %d, want 42", e.caffeinateGraceVal)
	}
	got := map[string]string{}
	for _, kv := range e.caffeinateAttrs {
		got[string(kv.Key)] = kv.Value.AsString()
	}
	if got["state"] != "grace" {
		t.Errorf("state attr = %q, want grace; attrs=%v", got["state"], got)
	}
	if got["plan_tier"] != "max_5x" {
		t.Errorf("plan_tier attr = %q, want max_5x", got["plan_tier"])
	}
	// The incident case: MODE active, PROCESS off.
	e.RecordCaffeinateActive(true, "off", 0, nil)
	got2 := map[string]string{}
	for _, kv := range e.caffeinateAttrs {
		got2[string(kv.Key)] = kv.Value.AsString()
	}
	if e.caffeinateActiveVal != 1 || got2["state"] != "off" {
		t.Errorf("incident case: want active=1 + state=off, got active=%d state=%q",
			e.caffeinateActiveVal, got2["state"])
	}
}

// TestEmitter_RecordSessionInfo_Replaces ensures successive calls REPLACE
// rather than append — required for dormant sessions to age out.
func TestEmitter_RecordSessionInfo_Replaces(t *testing.T) {
	e := &Emitter{}
	e.RecordSessionInfo([]SessionInfo{{SessionID: "a"}, {SessionID: "b"}})
	e.RecordSessionInfo([]SessionInfo{{SessionID: "a"}})
	if got := len(e.sessionInfoObs); got != 1 {
		t.Fatalf("after replace, len = %d, want 1", got)
	}
	if e.sessionInfoObs[0].sessionID != "a" {
		t.Errorf("remaining row = %q, want a", e.sessionInfoObs[0].sessionID)
	}
}

func TestRecordersAreNilSafe(t *testing.T) {
	var e *Emitter // nil
	// must not panic
	e.RecordTickDuration(time.Second)
	e.RecordPhase("discover", time.Millisecond)
	e.RecordScan("full", time.Millisecond, 1024)
	e.RecordSubprocess("git_branch", time.Millisecond)
	if e.MeterProvider() != nil {
		t.Fatal("nil emitter MeterProvider must be nil")
	}
}

func TestNewRegistersInstrumentsAndMeterProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	e, err := New(context.Background(), Options{ServiceName: "pa-monitor"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = e.Shutdown(context.Background()) }()
	if e.MeterProvider() == nil {
		t.Fatal("MeterProvider must be non-nil when enabled")
	}
	// recorders must not panic against a live emitter
	e.RecordPhase("discover", 2*time.Millisecond)
	e.RecordScan("incremental", time.Millisecond, 512)
	e.RecordSubprocess("terminal_host", 3*time.Millisecond)
	e.RecordTickDuration(10 * time.Millisecond)
}
