package conformance

import (
	"bytes"
	"embed"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/schemas"
)

//go:embed testdata/golden/*.json
var golden embed.FS

func loadGolden(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := golden.ReadFile("testdata/golden/" + name + ".json")
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode golden %s: %v", name, err)
	}
	return v
}

// GOAL-7: every message type has a golden example that validates against its
// schema — turns interfaces.md's illustrative samples into golden fixtures.
func TestGoldenFixturesValidate(t *testing.T) {
	for _, mt := range MessageTypes() {
		t.Run(mt, func(t *testing.T) {
			g := loadGolden(t, mt)
			if err := Check(mt, g); err != nil {
				t.Fatalf("golden %s failed its own schema: %v", mt, err)
			}
		})
	}
}

// Generic negatives applied to EVERY message type via its golden: an extra field
// (additionalProperties) is rejected, and — for a message carrying a const
// schemaVersion — an unknown version is rejected at the schema layer.
func TestNegative_Generic(t *testing.T) {
	for _, mt := range MessageTypes() {
		t.Run(mt+"/additionalProperties", func(t *testing.T) {
			g := loadGolden(t, mt)
			// oneOf/top-level-object: an undeclared field must be rejected.
			g["totallyUnexpectedField"] = "x"
			if err := Check(mt, g); err == nil {
				t.Fatalf("%s accepted an additional property", mt)
			}
		})
	}
}

// Per-message-type negative matrix: each independently-violable constraint has a
// rejecting case (required-field-missing, wrong-type, enum-out-of-range).
func TestNegative_Matrix(t *testing.T) {
	type nc struct {
		desc string
		json string
	}
	matrix := map[string][]nc{
		"event": {
			{"missing id", `{"schemaVersion":"1","type":"t"}`},
			{"missing type", `{"schemaVersion":"1","id":"e"}`},
			{"wrong-type payload", `{"id":"e","type":"t","payload":"notobj"}`},
			{"wrong-type expiresAt", `{"id":"e","type":"t","expiresAt":900}`},
			// The duration-valued field is GONE from the event (DEC-EVENT-1), and
			// additionalProperties:false is what makes that a REJECTION rather than a
			// silently-ignored leftover — the one check that would have caught the
			// doc-side deletion never reaching the code (bead pg2-85dv2).
			{"legacy duration field", `{"id":"e","type":"t","ttl":"5m"}`},
		},
		"source.query": {
			{"missing callback", `{"schemaVersion":"1","id":"q"}`},
			{"schemaVersion const mismatch", `{"schemaVersion":"9","id":"q","callback":"c"}`},
			{"wrong-type id", `{"schemaVersion":"1","id":5,"callback":"c"}`},
		},
		"source.query-reply": {
			{"neither branch", `{"schemaVersion":"1","id":"q"}`},
			{"deferred wrong const", `{"schemaVersion":"1","id":"q","deferred":false}`},
		},
		"handler.dispatch": {
			{"missing event", `{"schemaVersion":"1","id":"h","callback":"c"}`},
			{"event missing type", `{"schemaVersion":"1","id":"h","event":{"id":"e"},"callback":"c"}`},
			{"event carries the legacy duration field", `{"schemaVersion":"1","id":"h","event":{"id":"e","type":"t","ttl":"5m"},"callback":"c"}`},
		},
		"handler.dispatch-reply": {
			{"neither branch", `{"schemaVersion":"1","id":"h"}`},
		},
		"session-status": {
			{"missing state", `{"schemaVersion":"1","id":"h"}`},
			{"enum out of range", `{"schemaVersion":"1","id":"h","state":"weird"}`},
			{"progress above max", `{"schemaVersion":"1","id":"h","state":"running","progress":2}`},
			{"failure bad class", `{"schemaVersion":"1","id":"h","state":"failed","failure":{"class":"nope","message":"m"}}`},
		},
		"session-status-reply": {
			{"missing accepted", `{"schemaVersion":"1","id":"h"}`},
			{"wrong-type accepted", `{"schemaVersion":"1","id":"h","accepted":"yes"}`},
		},
		"mon.read": {
			{"missing metrics", `{"schemaVersion":"1","id":"m"}`},
			{"metrics wrong item type", `{"schemaVersion":"1","id":"m","metrics":[1,2]}`},
		},
		"mon.read-reply": {
			{"value wrong type", `{"schemaVersion":"1","id":"m","values":[{"name":"x","value":"y"}]}`},
		},
		"mon.update": {
			{"missing value", `{"schemaVersion":"1","id":"m","name":"x"}`},
		},
		"mon.update-reply": {
			{"missing accepted", `{"schemaVersion":"1","id":"m"}`},
		},
		"store.request": {
			{"enum op out of range", `{"schemaVersion":"1","id":"s","op":"purge","key":"k"}`},
			{"missing key", `{"schemaVersion":"1","id":"s","op":"get"}`},
		},
		"store.reply": {
			{"wrong-type ok", `{"schemaVersion":"1","id":"s","ok":"yes"}`},
		},
		"cli.ingest-event": {
			{"missing events", `{"schemaVersion":"1","id":"t"}`},
			{"empty events (minItems)", `{"schemaVersion":"1","id":"t","events":[]}`},
			{"event missing id", `{"schemaVersion":"1","id":"t","events":[{"type":"t"}]}`},
		},
		"cli.ingest-event-reply": {
			{"accepted wrong type", `{"schemaVersion":"1","id":"t","accepted":"1","rejected":[]}`},
			{"rejected item missing reason", `{"schemaVersion":"1","id":"t","accepted":0,"rejected":[{"id":"e"}]}`},
		},
		"cli.push-inject": {
			{"missing type (event ref)", `{"schemaVersion":"1","id":"e"}`},
			{"legacy duration field (event ref)", `{"schemaVersion":"1","id":"e","type":"t","ttl":"5m"}`},
		},
		"cli.status-reply": {
			{"session bad state", `{"schemaVersion":"1","sessions":[{"id":"h","handler":"r","event":"e","state":"weird"}],"queues":[],"config":{"sources":0,"handlers":0}}`},
			{"queue depth wrong type", `{"schemaVersion":"1","sessions":[],"queues":[{"type":"t","depth":"3"}],"config":{"sources":0,"handlers":0}}`},
		},
	}

	for mt, cases := range matrix {
		for _, c := range cases {
			t.Run(mt+"/"+c.desc, func(t *testing.T) {
				var v any
				if err := json.Unmarshal([]byte(c.json), &v); err != nil {
					t.Fatalf("bad test json: %v", err)
				}
				if err := Check(mt, v); err == nil {
					t.Fatalf("%s: %s was ACCEPTED but should be rejected", mt, c.desc)
				}
			})
		}
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
	// session-status: failure iff failed.
	failedNoFailure := map[string]any{"schemaVersion": "1", "id": "h", "state": "failed"}
	if err := Check("session-status", failedNoFailure); err == nil {
		t.Fatal("state=failed without failure was accepted")
	}
	runningWithFailure := map[string]any{
		"schemaVersion": "1", "id": "h", "state": "running",
		"failure": map[string]any{"class": "critical", "message": "m"},
	}
	if err := Check("session-status", runningWithFailure); err == nil {
		t.Fatal("failure present on non-failed state was accepted")
	}
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
// and both callback acks.
func TestAcceptanceAckShapes(t *testing.T) {
	acks := map[string]map[string]any{
		"handler.dispatch-reply": {"schemaVersion": "1", "id": "h", "deferred": true},
		"session-status-reply":   {"schemaVersion": "1", "id": "h", "accepted": true},
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
	_, code, _ := RoundTrip(h, "dispatch", map[string]any{"schemaVersion": "1", "id": "h", "callback": "c"})
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

// CheckBytes: valid bytes pass (incl. the cross-field pass for session-status),
// and a cross-field violation delivered as raw bytes is rejected.
func TestCheckBytes(t *testing.T) {
	good, _ := json.Marshal(loadGolden(t, "event"))
	if err := CheckBytes("event", good); err != nil {
		t.Fatalf("valid event bytes rejected: %v", err)
	}
	ss, _ := json.Marshal(loadGolden(t, "session-status"))
	if err := CheckBytes("session-status", ss); err != nil {
		t.Fatalf("valid session-status bytes rejected: %v", err)
	}
	// A schema-valid but cross-field-invalid session-status is rejected via bytes.
	bad := []byte(`{"schemaVersion":"1","id":"h","state":"running","failure":{"class":"critical","message":"m"}}`)
	if err := CheckBytes("session-status", bad); err == nil {
		t.Fatal("cross-field violation via CheckBytes was accepted")
	}
}

// Defensive: the cross-field rules reject a non-object value directly.
func TestCrossFieldRules_NonObject(t *testing.T) {
	if sessionStatusRule("not-an-object") == nil {
		t.Fatal("sessionStatusRule accepted a non-object")
	}
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
