package conformance

import (
	"bytes"
	"embed"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/schemas"
)

//go:embed testdata/compat/*.json
var compatGolden embed.FS

// loadGolden wraps the shared Golden loader (conformance.go) with a
// t.Fatalf-on-error signature for the tests below — the golden-loading
// implementation itself lives once in conformance.go so package conformance's
// own tests and the extracted conformance/driver package (Task 3.13) never
// carry two copies of it.
func loadGolden(t *testing.T, name string) map[string]any {
	t.Helper()
	v, err := Golden(name)
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return v
}

// TestBackwardCompat_LegacyGoldens proves the WIDENED cli.status-reply schema
// (Task 3.8) still accepts the OLD legacy-shaped reply — the pre-Task-3.8
// 4-field {schemaVersion, deliveries, queues, config} shape the core used to
// send. Go's own encoding/json ignoring unrecognized fields on unmarshal into
// a fixed struct is a SEPARATE, unrelated guarantee about an old CLIENT
// reading a NEW reply (Task 3.8 Binding decisions); this instead proves the
// schema itself has not tightened in a way that would reject the OLD reply
// shape the widening is supposed to preserve as a subset.
func TestBackwardCompat_LegacyGoldens(t *testing.T) {
	b, err := compatGolden.ReadFile("testdata/compat/cli.status-reply.json")
	if err != nil {
		t.Fatalf("read compat golden: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode compat golden: %v", err)
	}
	if err := Check("cli.status-reply", v); err != nil {
		t.Fatalf("widened cli.status-reply schema rejected the legacy golden: %v", err)
	}
}

// schemaVersion mismatch is reported (not guessed) at the envelope layer
// (INV-INTF-1).
func TestSchemaVersionMismatch(t *testing.T) {
	g := loadGolden(t, "handler.dispatch")
	g["schemaVersion"] = "99"
	err := schemas.CheckSchemaVersion(g)
	if err == nil {
		t.Fatal("unknown schemaVersion not reported")
	}
}

// A malformed / non-object payload is rejected, not crashed.
func TestMalformedPayload(t *testing.T) {
	if err := CheckBytes("event", []byte(`{ this is not json`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	if err := CheckBytes("event", []byte(`42`)); err == nil {
		t.Fatal("non-object payload accepted")
	}
}

// Cross-field rules a structural schema cannot express (both directions).
func TestCrossFieldRules(t *testing.T) {
	// store.request: value iff put.
	getWithValue := map[string]any{"schemaVersion": "1", "id": "s", "op": "get", "key": "k", "value": "v"}
	if err := Check("store.request", getWithValue); err == nil {
		t.Fatal("value present on op=get was accepted")
	}
	putNoValue := map[string]any{"schemaVersion": "1", "id": "s", "op": "put", "key": "k"}
	if err := Check("store.request", putNoValue); err == nil {
		t.Fatal("op=put without value was accepted")
	}
}

// Acceptance-ack shapes validate (the #2 acceptance handshake): the deferred ack
// and the ingest-event ack.
func TestAcceptanceAckShapes(t *testing.T) {
	acks := map[string]map[string]any{
		"handler.dispatch-reply": {"schemaVersion": "1", "id": "h", "deferred": true},
		"cli.ingest-event-reply": {"schemaVersion": "1", "id": "t", "accepted": float64(1), "rejected": []any{}},
	}
	for mt, ack := range acks {
		if err := Check(mt, ack); err != nil {
			t.Errorf("acceptance ack %s rejected: %v", mt, err)
		}
	}
}

// --- CLI transport round-trip (boundary proven live) ----------------------

func TestCLIRoundTrip_SyncOutcome(t *testing.T) {
	h := &ReferenceHandler{State: Started}
	dispatch := loadGolden(t, "handler.dispatch")
	reply, code, err := RoundTrip(h, "dispatch", dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	var rv any
	if err := json.Unmarshal(reply, &rv); err != nil {
		t.Fatalf("reply not JSON: %v", err)
	}
	// golden dispatch is the "deferred" branch; a sync handler replies with an
	// outcome — either way the reply must satisfy the reply schema.
	if err := Check("handler.dispatch-reply", rv); err != nil {
		t.Fatalf("reply failed handler.dispatch-reply schema: %v", err)
	}
}

func TestCLIRoundTrip_DeferredAck(t *testing.T) {
	h := &ReferenceHandler{State: Started, Deferred: true}
	reply, code, _ := RoundTrip(h, "dispatch", loadGolden(t, "handler.dispatch"))
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	var rv any
	_ = json.Unmarshal(reply, &rv)
	if err := Check("handler.dispatch-reply", rv); err != nil {
		t.Fatalf("deferred ack failed schema: %v", err)
	}
	if obj, _ := rv.(map[string]any); obj["deferred"] != true {
		t.Fatalf("expected deferred:true, got %v", rv)
	}
}

// The coarse exit codes are a WIRE contract, so their NUMBERS are part of the
// declared interface (INV-INTF-1) and not an implementation detail: a caller
// outside this module compares against integers, not against these identifiers.
// Every other assertion here goes through the names, which is exactly why a
// silent renumbering would pass them all — this is the one check that would catch
// BUSY moving back onto the usage code and inverting both readings
// (ADR 0042's Consequences).
func TestCoarseExitCodeValues(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"ok", ExitOK, 0},
		{"unexpected error", ExitError, 1},
		{"usage error", ExitUsage, 2},
		{"busy (pre-accept decline)", ExitBusy, 9},
	} {
		if c.got != c.want {
			t.Errorf("%s exit code = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestCLIRoundTrip_Busy(t *testing.T) {
	h := &ReferenceHandler{State: Started, Busy: true}
	_, code, _ := RoundTrip(h, "dispatch", loadGolden(t, "handler.dispatch"))
	if code != ExitBusy {
		t.Fatalf("busy handler exit=%d, want %d", code, ExitBusy)
	}
}

func TestCLIRoundTrip_Malformed(t *testing.T) {
	h := &ReferenceHandler{State: Started}
	// Send a dispatch missing its required event -> handler rejects with exit 1.
	_, code, _ := RoundTrip(h, "dispatch", map[string]any{"schemaVersion": "1", "id": "h"})
	if code != ExitError {
		t.Fatalf("malformed dispatch exit=%d, want %d", code, ExitError)
	}
}

// Out-of-lifecycle: a participant accepts messages ONLY when Started
// (interfaces.md lifecycle; INV-INTF-1).
func TestOutOfLifecycle(t *testing.T) {
	for _, st := range []Lifecycle{Starting, Stopping, Stopped} {
		h := &ReferenceHandler{State: st}
		_, code, _ := RoundTrip(h, "dispatch", loadGolden(t, "handler.dispatch"))
		if code == ExitOK {
			t.Fatalf("handler in lifecycle %d accepted a message (should not)", st)
		}
	}
}

// Idempotency: a handler tolerates the same event delivered twice (INV-EVT-2).
func TestHandlerIdempotentDuplicate(t *testing.T) {
	h := &ReferenceHandler{State: Started}
	d := loadGolden(t, "handler.dispatch")
	for range 2 {
		if _, code, _ := RoundTrip(h, "dispatch", d); code != ExitOK {
			t.Fatalf("duplicate dispatch not accepted (code=%d)", code)
		}
	}
	if !h.Seen("evt-abc123") {
		t.Fatal("handler did not record the event id")
	}
}

// CheckBytes: valid bytes pass (incl. the cross-field pass for store.request),
// and a cross-field violation delivered as raw bytes is rejected.
func TestCheckBytes(t *testing.T) {
	good, _ := json.Marshal(loadGolden(t, "event"))
	if err := CheckBytes("event", good); err != nil {
		t.Fatalf("valid event bytes rejected: %v", err)
	}
	sr, _ := json.Marshal(loadGolden(t, "store.request"))
	if err := CheckBytes("store.request", sr); err != nil {
		t.Fatalf("valid store.request bytes rejected: %v", err)
	}
	// A schema-valid but cross-field-invalid store.request is rejected via bytes.
	bad := []byte(`{"schemaVersion":"1","id":"s","op":"get","key":"k","value":"v"}`)
	if err := CheckBytes("store.request", bad); err == nil {
		t.Fatal("cross-field violation via CheckBytes was accepted")
	}
}

// Defensive: the cross-field rules reject a non-object value directly.
func TestCrossFieldRules_NonObject(t *testing.T) {
	if storeRequestRule(42) == nil {
		t.Fatal("storeRequestRule accepted a non-object")
	}
}

// RoundTrip surfaces a marshal error for an unmarshalable request.
func TestRoundTripMarshalError(t *testing.T) {
	h := &ReferenceHandler{State: Started}
	if _, _, err := RoundTrip(h, "dispatch", make(chan int)); err == nil {
		t.Fatal("expected marshal error for unmarshalable request")
	}
}

// Serve rejects malformed JSON on stdin (the transport's parse branch).
func TestServeMalformedStdin(t *testing.T) {
	h := &ReferenceHandler{State: Started}
	var out bytes.Buffer
	if code := h.Serve("dispatch", strings.NewReader("{ not json"), &out); code != ExitError {
		t.Fatalf("malformed stdin exit=%d, want %d", code, ExitError)
	}
}

func TestIsSchemaError(t *testing.T) {
	if !IsSchemaError(Check("event", map[string]any{})) {
		t.Fatal("validation failure should be a schema error")
	}
	// An unknown-schemaVersion error is also a schema error.
	if !IsSchemaError(schemas.CheckSchemaVersion(map[string]any{"schemaVersion": "99"})) {
		t.Fatal("unknown-version failure should be a schema error")
	}
	if IsSchemaError(nil) {
		t.Fatal("nil is not a schema error")
	}
}

// MessageTypes tracks the schema registry.
func TestMessageTypesNonEmpty(t *testing.T) {
	if len(MessageTypes()) == 0 || !strings.Contains(strings.Join(MessageTypes(), ","), "event") {
		t.Fatalf("MessageTypes missing entries: %v", MessageTypes())
	}
}

// Lifecycle.String reports the DOC-declared state names (interfaces.md
// "Lifecycle"), so a diagnostic or a registry view never prints an opaque integer.
func TestLifecycleNames(t *testing.T) {
	cases := map[Lifecycle]string{
		Starting: "starting",
		Started:  "started",
		Stopping: "stopping",
		Stopped:  "stopped",
		Crashing: "crashing",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Fatalf("Lifecycle(%d).String() = %q, want %q", int(state), got, want)
		}
	}
	if got := Lifecycle(99).String(); !strings.Contains(got, "99") {
		t.Fatalf("String of an out-of-range Lifecycle = %q, want it to name the value", got)
	}
}
