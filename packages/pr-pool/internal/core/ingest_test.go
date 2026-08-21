package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// oneEventRequest is a minimal, schema-valid ingest-event request.
const oneEventRequest = `{"schemaVersion":"1","id":"trk-9f2c","events":[` + oneEvent + `]}`

// oneEvent is a minimal, schema-valid event. Its `expiresAt` sits FAR in the
// future on purpose: the de-duplication window IS the retention window
// (INV-EVT-3), so a test that needs a duplicate to be ABSORBED has to ask for a
// window — under the born-expired default there is barely one.
const oneEvent = `{"schemaVersion":"1","id":"evt-abc123","type":"review-requested","expiresAt":"2099-01-01T00:00:00Z","payload":{}}`

// serveIngest runs the ingest-event subcommand IN PROCESS through the participant
// boundary (the same entry point the socket transport funnels into) and returns
// the decoded reply plus the exit code.
func serveIngest(t *testing.T, svc *Service, request string) (map[string]any, int) {
	t.Helper()
	var out strings.Builder
	code := svc.Serve(SubcommandIngestEvent, strings.NewReader(request), &out)
	var reply map[string]any
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", out.String(), err)
	}
	return reply, code
}

// inactiveThisRunType is a type a configured binding DECLARES while no binding
// active this run serves it — INV-DISP-3's second case. No listener is ever
// registered for it, which is what "inactive this run" looks like to the queue.
const inactiveThisRunType = "declared-but-inactive-this-run"

// testBindings is the CONFIGURED binding set every test core in this package is
// built with: the types this package's fixtures emit. Any OTHER type is unknown to
// the configuration and must be rejected (INV-DISP-3), which is what the
// unknown-type tests rely on.
func testBindings() Bindings {
	return NewBindings("review-requested", "t", "t1", "t2", inactiveThisRunType)
}

// recordingObserver captures the ingest conditions the core reports to metrics, so
// a test can assert the condition was recorded and not merely logged.
type recordingObserver struct{ unknownTypes []string }

func (o *recordingObserver) OnUnknownTypeRejected(eventType string) {
	o.unknownTypes = append(o.unknownTypes, eventType)
}

// startedService returns a service already in `started` WITHOUT a socket, so the
// participant boundary can be exercised on its own, carrying this package's
// configured binding set.
func startedService(t *testing.T) *Service {
	t.Helper()
	return startedServiceWith(t, testBindings(), nil)
}

// startedServiceWith is startedService with the CONFIGURED binding set and the
// metric observer named explicitly — the two inputs INV-DISP-3's cases turn on.
func startedServiceWith(t *testing.T, bindings Bindings, obs IngestObserver) *Service {
	t.Helper()
	return &Service{
		state:    conformance.Started,
		q:        newQueue(t),
		bindings: bindings,
		obs:      obs,
		reg:      NewRegistry(nil),
		command:  "pr-pool",
	}
}

// The happy path: a valid batch is accepted and lands in the durable queue, and
// the reply conforms to cli.ingest-event-reply.
func TestIngestEvent_AcceptsAndEnqueues(t *testing.T) {
	svc := startedService(t)
	reply, code := serveIngest(t, svc, oneEventRequest)

	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; reply=%v", code, conformance.ExitOK, reply)
	}
	if err := conformance.Check(IngestReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema (INV-INTF-2): %v", err)
	}
	if reply["id"] != "trk-9f2c" {
		t.Fatalf("id = %v, want the tracking id echoed back", reply["id"])
	}
	if reply["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1", reply["accepted"])
	}
	if got := reply["rejected"].([]any); len(got) != 0 {
		t.Fatalf("rejected = %v, want empty", got)
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("queue depth for review-requested = %d, want 1 (the event must be durable)", depth)
	}
}

// A whole batch is enqueued, in order, from one call.
func TestIngestEvent_AcceptsABatch(t *testing.T) {
	svc := startedService(t)
	req := `{"schemaVersion":"1","id":"trk-1","events":[
		{"id":"e1","type":"t1"},
		{"id":"e2","type":"t1"},
		{"id":"e3","type":"t2"}]}`
	reply, code := serveIngest(t, svc, req)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if reply["accepted"] != float64(3) {
		t.Fatalf("accepted = %v, want 3", reply["accepted"])
	}
	depth := svc.Queue().DepthByType()
	if depth["t1"] != 2 || depth["t2"] != 1 {
		t.Fatalf("depth = %v, want t1=2 t2=1", depth)
	}
}

// INV-DISP-3's FIRST case: no configured binding declares this type, so the core
// REJECTS the event to the caller — it is not enqueued, the reply names it as
// rejected, and the condition reaches logs and metrics. The reason string is the
// one DEC-WIRE-1's rejected example shows.
func TestIngestEvent_RejectsATypeNoConfiguredBindingDeclares(t *testing.T) {
	obs := &recordingObserver{}
	svc := startedServiceWith(t, testBindings(), obs)
	reply, code := serveIngest(t, svc, `{"schemaVersion":"1","id":"trk-1","events":[{"id":"e1","type":"nobody-declares-this"}]}`)

	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1 for a type no configured binding declares (INV-DISP-3); reply=%v", code, reply)
	}
	if err := conformance.Check(IngestReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}
	if reply["accepted"] != float64(0) {
		t.Fatalf("accepted = %v, want 0", reply["accepted"])
	}
	rejected := reply["rejected"].([]any)
	if len(rejected) != 1 {
		t.Fatalf("rejected = %v, want 1 entry", rejected)
	}
	entry := rejected[0].(map[string]any)
	if entry["id"] != "e1" {
		t.Fatalf("rejection id = %v, want the event id", entry["id"])
	}
	// The wording is the contract's own (DEC-WIRE-1's `rejected` example), and the
	// `unknown type: ` prefix is what keeps this cause distinguishable from
	// `malformed: ` and `enqueue: `.
	if got, want := entry["reason"], `unknown type: no configured binding declares "nobody-declares-this"`; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
	if len(svc.Queue().DepthByType()) != 0 {
		t.Fatalf("depth = %v, want nothing enqueued — a rejected event MUST NOT enter the queue", svc.Queue().DepthByType())
	}
	if len(obs.unknownTypes) != 1 || obs.unknownTypes[0] != "nobody-declares-this" {
		t.Fatalf("observed unknown types = %v, want the condition recorded once for nobody-declares-this", obs.unknownTypes)
	}
}

// DEC-WIRE-1's `rejected` example shape, end to end: one malformed event and one
// event whose type is unknown to the configuration, in ONE reply — `accepted` 0,
// exit 1, and the two causes distinguishable by their reason prefixes.
func TestIngestEvent_RejectedListCarriesBothMalformedAndUnknownType(t *testing.T) {
	svc := startedService(t)
	req := `{"schemaVersion":"1","id":"trk-9f2c","events":[
		{"id":"evt-abc123"},
		{"id":"evt-def456","type":"review-abandoned"}]}`
	reply, code := serveIngest(t, svc, req)

	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1; reply=%v", code, reply)
	}
	if err := conformance.Check(IngestReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}
	if reply["accepted"] != float64(0) {
		t.Fatalf("accepted = %v, want 0", reply["accepted"])
	}
	byID := map[string]string{}
	for _, r := range reply["rejected"].([]any) {
		e := r.(map[string]any)
		byID[e["id"].(string)] = e["reason"].(string)
	}
	if got := byID["evt-abc123"]; !strings.HasPrefix(got, "malformed: ") {
		t.Fatalf("evt-abc123 reason = %q, want a malformed: prefix", got)
	}
	if got, want := byID["evt-def456"], `unknown type: no configured binding declares "review-abandoned"`; got != want {
		t.Fatalf("evt-def456 reason = %q, want %q", got, want)
	}
	if len(svc.Queue().DepthByType()) != 0 {
		t.Fatalf("depth = %v, want nothing enqueued", svc.Queue().DepthByType())
	}
}

// INV-DISP-3's SECOND case, and the boundary the whole invariant turns on: a type
// a configured binding DECLARES but that no binding active this run serves is
// ACCEPTED and enqueued — it waits, is offered to nobody, and is dropped
// unconsumed-expired (INV-EVT-1, INV-EVT-4). The identical event, with the
// identical (empty) set of active listeners, is REJECTED once the CONFIGURATION no
// longer declares its type — which is what "validity is judged against the
// configuration, never against the run's active subset" means in code.
func TestIngestEvent_AcceptsADeclaredTypeNoActiveBindingServes(t *testing.T) {
	req := `{"schemaVersion":"1","id":"trk-1","events":[{"id":"e1","type":"` + inactiveThisRunType + `"}]}`

	// Declared by the configuration; inactive this run (no listener registered).
	svc := startedServiceWith(t, NewBindings(inactiveThisRunType), nil)
	reply, code := serveIngest(t, svc, req)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0 — a declared type merely inactive this run is accepted (INV-DISP-3); reply=%v", code, reply)
	}
	if reply["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1", reply["accepted"])
	}
	if got := reply["rejected"].([]any); len(got) != 0 {
		t.Fatalf("rejected = %v, want empty — this case is neither an error nor a warning", got)
	}
	if depth := svc.Queue().DepthByType()[inactiveThisRunType]; depth != 1 {
		t.Fatalf("depth = %d, want 1 — the event must be enqueued and wait", depth)
	}
	// Offered to nobody, then dropped unconsumed-expired: the event carries no
	// `expiresAt`, so its expiry is the core's own now at ingest (INV-EVT-1).
	if accepted := svc.Queue().Dispatch(); accepted != 0 {
		t.Fatalf("Dispatch accepted %d, want 0 — no binding active this run serves the type", accepted)
	}
	if dropped := svc.Queue().Expire(); dropped != 1 {
		t.Fatalf("Expire dropped %d, want 1 (unconsumed-expired, INV-EVT-4)", dropped)
	}

	// Same event, same absence of active listeners — but no configured binding
	// declares the type, so THIS one is rejected and never enqueued.
	undeclared := startedServiceWith(t, NewBindings(), nil)
	reply, code = serveIngest(t, undeclared, req)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1 once the configuration declares no binding for the type; reply=%v", code, reply)
	}
	if len(undeclared.Queue().DepthByType()) != 0 {
		t.Fatalf("depth = %v, want nothing enqueued", undeclared.Queue().DepthByType())
	}
}

// A duplicate id still within its retention is ABSORBED and counted as accepted
// (INV-EVT-3) — a source must never have to track what it already emitted.
func TestIngestEvent_DuplicateIsAbsorbedNotRejected(t *testing.T) {
	svc := startedService(t)
	if _, code := serveIngest(t, svc, oneEventRequest); code != conformance.ExitOK {
		t.Fatalf("first ingest exit = %d, want 0", code)
	}
	reply, code := serveIngest(t, svc, oneEventRequest)
	if code != conformance.ExitOK {
		t.Fatalf("re-ingest exit = %d, want 0 (dedup is not a rejection); reply=%v", code, reply)
	}
	if reply["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1 (the duplicate is absorbed)", reply["accepted"])
	}
	if len(reply["rejected"].([]any)) != 0 {
		t.Fatalf("rejected = %v, want empty for a duplicate", reply["rejected"])
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("depth = %d, want 1 (deduped, not doubled)", depth)
	}
}

// A batch with one bad event still delivers the good ones, and names the bad one
// in `rejected` with exit 1 (DEC-WIRE-1's rejected example, docs/decisions/wire.md).
func TestIngestEvent_PartialBatchRejectsOnlyTheMalformed(t *testing.T) {
	svc := startedService(t)
	// All three faults are caught by the schema layer (INV-INTF-2): `noType`
	// violates it structurally, and `badAt` / `badExpiry` fail the `at` /
	// `expiresAt` RFC3339 `pattern` — including a DURATION-shaped expiresAt, the
	// shape a caller written against the old duration-valued contract would
	// send. (A value that matched the pattern's syntax but failed calendar
	// semantics, e.g. a month of 13, would instead reach DecodeEvent's
	// time.Parse and be rejected there — not exercised by this case.)
	req := `{"schemaVersion":"1","id":"trk-1","events":[
		{"id":"good","type":"t"},
		{"id":"noType"},
		{"id":"badAt","type":"t","at":"yesterday"},
		{"id":"badExpiry","type":"t","expiresAt":"15m"}]}`
	reply, code := serveIngest(t, svc, req)

	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1 when anything was rejected", code)
	}
	if err := conformance.Check(IngestReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}
	if reply["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1 (the good event must still be delivered)", reply["accepted"])
	}
	rejected := reply["rejected"].([]any)
	if len(rejected) != 3 {
		t.Fatalf("rejected = %v, want 3 entries", rejected)
	}
	byID := map[string]string{}
	for _, r := range rejected {
		e := r.(map[string]any)
		byID[e["id"].(string)] = e["reason"].(string)
	}
	for _, id := range []string{"noType", "badAt", "badExpiry"} {
		reason, ok := byID[id]
		if !ok {
			t.Fatalf("event %s missing from rejected: %v", id, byID)
		}
		if !strings.HasPrefix(reason, "malformed: ") {
			t.Fatalf("event %s reason = %q, want a malformed: prefix", id, reason)
		}
	}
	if depth := svc.Queue().DepthByType()["t"]; depth != 1 {
		t.Fatalf("depth = %d, want only the good event queued", depth)
	}
}

// An event too broken to yield an id is still reported (with an empty id), never
// dropped silently.
func TestIngestEvent_RejectsANonObjectEvent(t *testing.T) {
	svc := startedService(t)
	reply, code := serveIngest(t, svc, `{"schemaVersion":"1","id":"trk-1","events":["not-an-object"]}`)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1", code)
	}
	rejected := reply["rejected"].([]any)
	if len(rejected) != 1 {
		t.Fatalf("rejected = %v, want 1 entry", rejected)
	}
	if got := rejected[0].(map[string]any)["id"]; got != "" {
		t.Fatalf("rejection id = %v, want an empty id for an unidentifiable event", got)
	}
}

// Envelope faults produce the protocol error envelope + exit 1: no event could be
// read, so there is no accepted/rejected tally to report.
func TestIngestEvent_EnvelopeFaults(t *testing.T) {
	cases := []struct {
		desc, req, want string
	}{
		{"malformed JSON", `{"schemaVersion":`, "malformed JSON"},
		{"not an object", `["a"]`, "not an object"},
		{"missing schemaVersion", `{"id":"t","events":[` + oneEvent + `]}`, "missing schemaVersion"},
		{"unknown schemaVersion", `{"schemaVersion":"9","id":"t","events":[` + oneEvent + `]}`, "unknown schemaVersion"},
		{"missing id", `{"schemaVersion":"1","events":[` + oneEvent + `]}`, `missing required field "id"`},
		{"non-string id", `{"schemaVersion":"1","id":5,"events":[` + oneEvent + `]}`, "id must be a string"},
		{"missing events", `{"schemaVersion":"1","id":"t"}`, `missing required field "events"`},
		{"events not an array", `{"schemaVersion":"1","id":"t","events":{}}`, "events must be an array"},
		{"empty events", `{"schemaVersion":"1","id":"t","events":[]}`, "at least one event"},
		{"additional property", `{"schemaVersion":"1","id":"t","events":[` + oneEvent + `],"extra":1}`, "additional properties not allowed: extra"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			svc := startedService(t)
			reply, code := serveIngest(t, svc, tc.req)
			if code != conformance.ExitError {
				t.Fatalf("exit = %d, want 1", code)
			}
			msg, _ := reply["error"].(string)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error = %q, want it to contain %q", msg, tc.want)
			}
			if len(svc.Queue().DepthByType()) != 0 {
				t.Fatal("an envelope fault must enqueue nothing")
			}
		})
	}
}

// decodeIngest's envelope gate must agree EXACTLY with cli.ingest-event.schema.json
// at the envelope level. It deliberately skips the schema's `items` clause (so one
// bad event cannot fail a whole batch), and this pins that to be the ONLY
// difference.
func TestDecodeIngest_EnvelopeMatchesSchema(t *testing.T) {
	// Every case keeps its events VALID, so the schema's items clause never fires
	// and any disagreement is an envelope-level disagreement.
	cases := []string{
		`{"schemaVersion":"1","id":"t","events":[` + oneEvent + `]}`,
		`{"schemaVersion":"1","id":"","events":[` + oneEvent + `]}`,
		`{"schemaVersion":"1","events":[` + oneEvent + `]}`,
		`{"schemaVersion":"1","id":"t"}`,
		`{"id":"t","events":[` + oneEvent + `]}`,
		`{"schemaVersion":"9","id":"t","events":[` + oneEvent + `]}`,
		`{"schemaVersion":1,"id":"t","events":[` + oneEvent + `]}`,
		`{"schemaVersion":"1","id":5,"events":[` + oneEvent + `]}`,
		`{"schemaVersion":"1","id":"t","events":[]}`,
		`{"schemaVersion":"1","id":"t","events":{}}`,
		`{"schemaVersion":"1","id":"t","events":[` + oneEvent + `],"extra":1}`,
		`["not-an-object"]`,
	}
	for _, req := range cases {
		t.Run(req, func(t *testing.T) {
			schemaOK := conformance.CheckBytes(IngestRequestSchema, []byte(req)) == nil
			_, err := decodeIngest([]byte(req))
			gateOK := err == nil
			if schemaOK != gateOK {
				t.Fatalf("drift: schema accepts=%v but decodeIngest accepts=%v (err=%v)", schemaOK, gateOK, err)
			}
		})
	}
}

// A durable-write failure is surfaced per event with an `enqueue:` reason — a
// different cause from `malformed:`, and NOT silently counted as accepted.
func TestIngestEvent_EnqueueFailureIsReported(t *testing.T) {
	q, err := eventqueue.New(failingStore{})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	svc := &Service{state: conformance.Started, q: q, bindings: testBindings(), reg: NewRegistry(nil), command: "pr-pool"}

	reply, code := serveIngest(t, svc, oneEventRequest)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1 when the durable write failed", code)
	}
	if err := conformance.Check(IngestReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}
	if reply["accepted"] != float64(0) {
		t.Fatalf("accepted = %v, want 0", reply["accepted"])
	}
	rejected := reply["rejected"].([]any)
	if len(rejected) != 1 {
		t.Fatalf("rejected = %v, want 1 entry", rejected)
	}
	entry := rejected[0].(map[string]any)
	if entry["id"] != "evt-abc123" {
		t.Fatalf("rejection id = %v, want the event id", entry["id"])
	}
	if reason := entry["reason"].(string); !strings.HasPrefix(reason, "enqueue: ") {
		t.Fatalf("reason = %q, want an enqueue: prefix (distinguishable from malformed:)", reason)
	}
}

// failingStore is a Store whose durable append always fails.
type failingStore struct{}

var errStoreDown = errors.New("store is down")

func (failingStore) Append(eventqueue.Record) error       { return errStoreDown }
func (failingStore) Replay() ([]eventqueue.Record, error) { return nil, nil }
func (failingStore) Close() error                         { return nil }

// The reply's schemaVersion is the one the core declares, not a literal.
func TestIngestEvent_ReplyCarriesTheCoreSchemaVersion(t *testing.T) {
	svc := startedService(t)
	reply, _ := serveIngest(t, svc, oneEventRequest)
	if reply["schemaVersion"] != schemas.SchemaVersion {
		t.Fatalf("schemaVersion = %v, want %q", reply["schemaVersion"], schemas.SchemaVersion)
	}
}

func TestRawEventID(t *testing.T) {
	if got := rawEventID([]byte(`{"id":"e1"}`)); got != "e1" {
		t.Fatalf("rawEventID = %q, want e1", got)
	}
	if got := rawEventID([]byte(`"scalar"`)); got != "" {
		t.Fatalf("rawEventID of a non-object = %q, want empty", got)
	}
	if got := rawEventID([]byte(`{"id":5}`)); got != "" {
		t.Fatalf("rawEventID of a non-string id = %q, want empty", got)
	}
}
