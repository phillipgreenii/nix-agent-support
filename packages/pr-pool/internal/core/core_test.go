package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/activity"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// Serve dispatches the `register` subcommand (Task 2.1): a participant
// registering over the boundary gets back a cli.register-reply-shaped reply —
// accepted, an empty callback for a handler kind (ingestCallbackFor has no
// target for it), and a non-empty selfStatusCallback (every kind gets one,
// interfaces.md "Self-status").
func TestServe_RegisterSubcommand(t *testing.T) {
	svc := startedService(t)
	var out strings.Builder
	req := `{"schemaVersion":"1","id":"role-triage","kind":"handler"}`
	code := svc.Serve(SubcommandRegister, strings.NewReader(req), &out)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; body=%s", code, conformance.ExitOK, out.String())
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", out.String(), err)
	}
	if err := conformance.Check(RegisterReplySchema, reply); err != nil {
		t.Fatalf("reply failed cli.register-reply schema: %v", err)
	}
	if reply["accepted"] != true {
		t.Fatalf("accepted = %v, want true", reply["accepted"])
	}
	if reply["callback"] != "" {
		t.Fatalf("callback = %v, want empty for a handler kind", reply["callback"])
	}
	if cb, _ := reply["selfStatusCallback"].(string); cb == "" {
		t.Fatal("selfStatusCallback is empty, want a minted command every kind gets")
	}
}

// Close must propagate stopping/stopped to every registered participant
// (Task 2.1 Step 2.1.6) — the same orderly-shutdown signal interfaces.md's
// lifecycle diagram declares for the core's own state, but Close previously
// never touched s.reg at all, so a registered participant's Registration
// stayed `started` forever after the core it belonged to had gone away.
func TestClose_PropagatesStoppingStoppedToRegistry(t *testing.T) {
	svc, _ := startService(t, shortDir(t))
	reg, err := svc.Register("p1", KindHandler)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := svc.Registry().SetLifecycle(reg.ID, conformance.Started); err != nil {
		t.Fatalf("SetLifecycle(started): %v", err)
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, ok := svc.Registry().Get("p1")
	if !ok {
		t.Fatal("participant p1 missing from registry after Close")
	}
	if got.State != conformance.Stopped {
		t.Fatalf("State after Close = %v, want %v", got.State, conformance.Stopped)
	}
}

// End to end over the REAL socket: a caller with the core's Ref delivers events
// and they land in the durable queue.
func TestSocketRoundTrip_IngestEvent(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)

	client, err := Dial(ref)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	reply, code, err := client.Call(SubcommandIngestEvent, []byte(oneEventRequest))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%s", code, reply)
	}
	var decoded map[string]any
	if err := json.Unmarshal(reply, &decoded); err != nil {
		t.Fatalf("reply %s is not JSON: %v", reply, err)
	}
	if err := conformance.Check(IngestReplySchema, decoded); err != nil {
		t.Fatalf("socket reply failed the ingest-event reply schema: %v", err)
	}
	if decoded["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1", decoded["accepted"])
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("queue depth = %d, want 1 after a socket ingest", depth)
	}
}

// The SOCKET transport and the IN-PROCESS participant boundary must produce
// byte-identical replies and the same exit code: the socket is a carrier for
// conformance.Participant, not a second implementation with its own semantics.
func TestSocketAndInProcessTransportsAgree(t *testing.T) {
	requests := []string{
		oneEventRequest,
		`{"schemaVersion":"1","id":"trk-2","events":[{"id":"bad","type":"t"}]}`,
		`{"schemaVersion":"9","id":"trk-3","events":[` + oneEvent + `]}`,
	}
	for _, req := range requests {
		t.Run(req, func(t *testing.T) {
			// In-process, through the participant boundary the conformance suite uses.
			direct := startedService(t)
			wantReply, wantCode, err := conformance.RoundTrip(direct, SubcommandIngestEvent, json.RawMessage(req))
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}

			// Over the socket, against a fresh core with the same empty queue.
			_, ref := startService(t, shortDir(t))
			client, err := Dial(ref)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer func() { _ = client.Close() }()
			gotReply, gotCode, err := client.Call(SubcommandIngestEvent, []byte(req))
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if gotCode != wantCode {
				t.Fatalf("exit over socket = %d, in process = %d", gotCode, wantCode)
			}
			if string(gotReply) != string(wantReply) {
				t.Fatalf("reply over socket = %s, in process = %s", gotReply, wantReply)
			}
		})
	}
}

// Service must satisfy the EXISTING conformance.Participant transport so the
// declared suite can drive it directly (INV-INTF-2) — no bespoke harness.
func TestService_IsAConformanceParticipant(t *testing.T) {
	var p conformance.Participant = startedService(t)
	reply, code, err := conformance.RoundTrip(p, SubcommandIngestEvent, json.RawMessage(oneEventRequest))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%s", code, reply)
	}
	var decoded map[string]any
	if err := json.Unmarshal(reply, &decoded); err != nil {
		t.Fatalf("reply is not JSON: %v", err)
	}
	if err := conformance.Check(IngestReplySchema, decoded); err != nil {
		t.Fatalf("reply failed its schema: %v", err)
	}
}

// Concurrent callers must be served without a race and without losing events.
func TestSocket_ConcurrentCallers(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client, err := Dial(ref)
			if err != nil {
				t.Errorf("Dial: %v", err)
				return
			}
			defer func() { _ = client.Close() }()
			req := `{"schemaVersion":"1","id":"trk-` + string(rune('a'+i)) + `","events":[{"id":"e` + string(rune('a'+i)) + `","type":"t"}]}`
			_, code, err := client.Call(SubcommandIngestEvent, []byte(req))
			if err != nil {
				t.Errorf("Call: %v", err)
				return
			}
			if code != conformance.ExitOK {
				t.Errorf("exit = %d, want 0", code)
			}
		}(i)
	}
	wg.Wait()
	if depth := svc.Queue().DepthByType()["t"]; depth != n {
		t.Fatalf("queue depth = %d, want %d (no event lost across concurrent callers)", depth, n)
	}
}

// A body-less reply — the legal busy shape (exit 9, no body) — must cross the
// transport as an explicit null, since an empty json.RawMessage is not valid JSON
// and would corrupt the frame.
func TestRespond_BodylessReplyIsNull(t *testing.T) {
	svc := startedService(t)
	var out strings.Builder
	svc.respond(&out, conformance.ExitBusy, nil)

	var resp wireResponse
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
		t.Fatalf("frame %q is not JSON: %v", out.String(), err)
	}
	if resp.ExitCode != conformance.ExitBusy {
		t.Fatalf("exit = %d, want %d", resp.ExitCode, conformance.ExitBusy)
	}
	if string(resp.Reply) != "null" {
		t.Fatalf("reply = %s, want null for a body-less response", resp.Reply)
	}
}

// --- Task 3.8: the `status` verb -------------------------------------------

// statusRequest is a minimal, schema-valid status request: since omitted.
const statusRequest = `{"schemaVersion":"1"}`

// startedServiceForStatus returns a started service carrying the extra Task
// 3.8 seams a status reply composes from (activityRing, configPath,
// startedAt) — the same startedServiceWith/startedServiceWithMonitoring
// literal-construction pattern, since these fields have no Options-level
// test constructor of their own.
func startedServiceForStatus(t *testing.T, ring *activity.Ring) *Service {
	t.Helper()
	return &Service{
		state:        conformance.Started,
		q:            newQueue(t),
		bindings:     testBindings(),
		reg:          NewRegistry(nil),
		command:      "pr-pool",
		activityRing: ring,
		configPath:   "/repo/.pr-pool/config.toml",
		startedAt:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
}

// serveStatus runs the `status` subcommand IN PROCESS through the
// participant boundary, matching serveIngest/serveMonRead's shape. It must
// only be called from the test's own goroutine (it may call t.Fatalf) — see
// TestServeStatus_InterleavedReadOnlyInvariance and
// TestServeStatus_ThreeWayConcurrency for the plain svc.Serve calls their
// worker goroutines use instead.
func serveStatus(t *testing.T, svc *Service, request string) (map[string]any, int) {
	t.Helper()
	var out strings.Builder
	code := svc.Serve(SubcommandStatus, strings.NewReader(request), &out)
	var reply map[string]any
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", out.String(), err)
	}
	return reply, code
}

// TestServeStatus_LifecycleTable proves `status` obeys the SAME INV-INTF-1
// lifecycle gate (Serve's own State() check) every other subcommand does:
// messages cross only in `started`; every other lifecycle state is refused
// with the protocol error envelope (Task 3.8 Acceptance).
func TestServeStatus_LifecycleTable(t *testing.T) {
	cases := []struct {
		state    conformance.Lifecycle
		wantCode int
	}{
		{conformance.Starting, conformance.ExitError},
		{conformance.Started, conformance.ExitOK},
		{conformance.Stopping, conformance.ExitError},
		{conformance.Stopped, conformance.ExitError},
	}
	for _, tc := range cases {
		t.Run(tc.state.String(), func(t *testing.T) {
			svc := startedServiceForStatus(t, nil)
			svc.state = tc.state
			reply, code := serveStatus(t, svc, statusRequest)
			if code != tc.wantCode {
				t.Fatalf("state=%s: exit = %d, want %d; reply=%v", tc.state, code, tc.wantCode, reply)
			}
			if tc.wantCode == conformance.ExitOK {
				if err := conformance.Check(StatusReplySchema, reply); err != nil {
					t.Fatalf("reply failed its own schema: %v", err)
				}
				return
			}
			if err := conformance.Check(ErrorReplySchema, reply); err != nil {
				t.Fatalf("out-of-lifecycle reply failed the error-envelope schema: %v", err)
			}
		})
	}
}

// TestServeStatus_NilTickBootWindow proves the boot window (no PublishTick
// call yet — CurrentTick() == nil) composes a schema-valid reply with every
// tick-derived field simply OMITTED rather than guessed at, and never
// panics (Task 3.8 Acceptance: "nil-tick boot window handled without
// panic").
func TestServeStatus_NilTickBootWindow(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	reply, code := serveStatus(t, svc, statusRequest)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}
	for _, absent := range []string{"mode", "resolvedConfig", "lastTickAt", "snapshotAt", "tickIntervalMs"} {
		if v, present := reply[absent]; present {
			t.Fatalf("reply[%q] = %v, want omitted before the first PublishTick", absent, v)
		}
	}
	if got, ok := reply["sources"].([]any); !ok || len(got) != 0 {
		t.Fatalf("sources = %v, want an empty array before the first tick", reply["sources"])
	}
	cfg, ok := reply["config"].(map[string]any)
	if !ok || cfg["sources"] != float64(0) || cfg["handlers"] != float64(0) {
		t.Fatalf("legacy config = %v, want zero counts pre-tick", reply["config"])
	}
	core, ok := reply["core"].(map[string]any)
	if !ok || core["state"] != "started" {
		t.Fatalf("core = %v, want state=started even pre-tick (Listen-time state, not tick-derived)", reply["core"])
	}
}

// TestServeStatus_ComposesLiveState proves the reply actually reflects live
// state from every seam Task 3.8's Contract names: queue depths (Task 3.2),
// the activity ring (Task 3.4), the gate cell (Task 3.5, via the SAME
// ObserveGateFromSocketVerb method Task 3.9's own verb will call), the
// registry (existing Registry.List()), and a published tick.
func TestServeStatus_ComposesLiveState(t *testing.T) {
	ring := activity.New(4)
	ring.Append(activity.Entry{Type: "review-requested", Outcome: "delivered"})
	svc := startedServiceForStatus(t, ring)

	if _, err := svc.q.Enqueue(eventqueue.Event{ID: "e1", Type: "review-requested", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := svc.Register("review", KindHandler); err != nil {
		t.Fatalf("Register: %v", err)
	}
	svc.ObserveGateFromSocketVerb(time.Now(), "quota_paused", GateInfo{Set: true, Owner: "ops"})
	now := time.Now()
	svc.PublishTick(TickSnapshot{
		RunMode:    RunModeLongRunning,
		Version:    "test-version",
		Config:     ResolvedConfig{RepoRoot: "/repo", ActiveRoles: 1, ActiveQueries: 1},
		Sources:    []SourceReport{{Name: "feedback-ready"}},
		LastTickAt: now,
		SnapshotAt: now,
	})

	reply, code := serveStatus(t, svc, statusRequest)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}

	queues, _ := reply["queues"].([]any)
	if len(queues) != 1 || queues[0].(map[string]any)["type"] != "review-requested" {
		t.Fatalf("queues = %v, want the one enqueued type (Task 3.2 DepthByType)", queues)
	}
	gates, _ := reply["gates"].([]any)
	foundGate := false
	for _, g := range gates {
		gm := g.(map[string]any)
		if gm["name"] == "quota_paused" && gm["set"] == true && gm["owner"] == "ops" {
			foundGate = true
		}
	}
	if !foundGate {
		t.Fatalf("gates = %v, want quota_paused set by ObserveGateFromSocketVerb", gates)
	}
	listeners, _ := reply["listeners"].([]any)
	if len(listeners) != 1 || listeners[0].(map[string]any)["id"] != "review" {
		t.Fatalf("listeners = %v, want the registered handler (Registry.List())", listeners)
	}
	activityEntries, _ := reply["activity"].([]any)
	if len(activityEntries) != 1 || activityEntries[0].(map[string]any)["outcome"] != "delivered" {
		t.Fatalf("activity = %v, want the ring's one entry (Task 3.4 Ring.Read)", activityEntries)
	}
	if reply["mode"] != RunModeLongRunning {
		t.Fatalf("mode = %v, want %s", reply["mode"], RunModeLongRunning)
	}
	sources, _ := reply["sources"].([]any)
	if len(sources) != 1 || sources[0].(map[string]any)["name"] != "feedback-ready" {
		t.Fatalf("sources = %v, want the published tick's one source", sources)
	}
}

// TestServeStatus_ActivityDroppedFalseWhenNothingEvicted proves the reply's
// activityDropped field is false whenever the ring has not yet been asked
// to skip past anything it discarded — the since==0 (omitted-cursor) case
// Ring.Read documents as "always false", and the ordinary case where a
// non-zero since still names an entry the ring still retains.
func TestServeStatus_ActivityDroppedFalseWhenNothingEvicted(t *testing.T) {
	ring := activity.New(8)
	ring.Append(activity.Entry{Type: "review-requested", Outcome: "delivered"}) // seq=1
	svc := startedServiceForStatus(t, ring)

	// since omitted (statusRequest): the zero-value/no-cursor case.
	reply, code := serveStatus(t, svc, statusRequest)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}
	if reply["activityDropped"] != false {
		t.Fatalf("activityDropped = %v, want false (since omitted, nothing to have dropped)", reply["activityDropped"])
	}

	// since=1 names the one retained entry itself — nothing between since and
	// what's retained was ever evicted.
	reply, code = serveStatus(t, svc, `{"schemaVersion":"1","since":1}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if reply["activityDropped"] != false {
		t.Fatalf("activityDropped = %v, want false (since names a still-retained entry)", reply["activityDropped"])
	}
}

// TestServeStatus_ActivityDroppedFalseWithNoRing proves activityDropped
// defaults to false (present, not merely absent) when the service carries no
// activity ring at all — mirroring how the `activity` array itself defaults
// to empty rather than being omitted.
func TestServeStatus_ActivityDroppedFalseWithNoRing(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	reply, code := serveStatus(t, svc, statusRequest)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}
	if reply["activityDropped"] != false {
		t.Fatalf("activityDropped = %v, want false with no ring configured", reply["activityDropped"])
	}
	if activityEntries, _ := reply["activity"].([]any); len(activityEntries) != 0 {
		t.Fatalf("activity = %v, want empty with no ring configured", reply["activity"])
	}
}

// TestServeStatus_ActivityDroppedTrueAfterEviction proves the eviction
// signal actually crosses the wire: a ring whose capacity is exceeded, read
// with a since cursor older than the oldest entry it still retains, reports
// activityDropped=true (Ring.Read's "since < oldest-retained" case).
func TestServeStatus_ActivityDroppedTrueAfterEviction(t *testing.T) {
	ring := activity.New(2) // tiny capacity: easy to overrun
	for i := 0; i < 5; i++ {
		ring.Append(activity.Entry{Type: "review-requested", Outcome: "delivered"})
	}
	// Capacity 2, 5 appends (seq 1..5): only seq 4 and 5 are still retained.
	svc := startedServiceForStatus(t, ring)

	reply, code := serveStatus(t, svc, `{"schemaVersion":"1","since":1}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}
	if reply["activityDropped"] != true {
		t.Fatalf("activityDropped = %v, want true (since=1 predates the oldest retained seq=4)", reply["activityDropped"])
	}
	activityEntries, _ := reply["activity"].([]any)
	if len(activityEntries) != 2 {
		t.Fatalf("activity = %v, want exactly the 2 still-retained entries", reply["activity"])
	}
}

// alwaysAcceptListener accepts every event of the given type — the minimal
// eventqueue.Listener stub TestServeStatus_ThreeWayConcurrency needs to
// drive real Dispatch() acceptances; core_test.go otherwise never needs a
// Listener implementation of its own.
type alwaysAcceptListener struct{ id, typ string }

func (l *alwaysAcceptListener) ID() string                        { return l.id }
func (l *alwaysAcceptListener) Matches(evt eventqueue.Event) bool { return evt.Type == l.typ }
func (l *alwaysAcceptListener) Offer(eventqueue.Event) bool       { return true }

// TestServeStatus_InterleavedReadOnlyInvariance proves `status` is read-only:
// a burst of concurrent status reads interleaved around a queue mutation
// must never itself change the queue depth or the registry size — the
// invariant the whole composition rests on (composeStatusReply's doc:
// "no q.mu is ever taken here", Task 3.8 Binding decisions Step 8).
func TestServeStatus_InterleavedReadOnlyInvariance(t *testing.T) {
	svc := startedServiceForStatus(t, activity.New(8))
	if _, err := svc.q.Enqueue(eventqueue.Event{ID: "e1", Type: "review-requested", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := svc.Register("m-1", KindMonitor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	beforeDepth := svc.q.DepthByType()["review-requested"]
	beforeLen := svc.reg.Len()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out strings.Builder
			svc.Serve(SubcommandStatus, strings.NewReader(statusRequest), &out)
		}()
	}
	wg.Wait()

	afterDepth := svc.q.DepthByType()["review-requested"]
	afterLen := svc.reg.Len()
	if afterDepth != beforeDepth {
		t.Fatalf("queue depth changed from %d to %d across interleaved status reads — status must be read-only", beforeDepth, afterDepth)
	}
	if afterLen != beforeLen {
		t.Fatalf("registry length changed from %d to %d across interleaved status reads", beforeLen, afterLen)
	}
}

// TestServeStatus_ThreeWayConcurrency runs three independent actors
// concurrently — status reads, live Enqueue/Dispatch/Expire churn, and the
// gate-cell write a future socket pause/resume verb (Task 3.9, out of scope
// here per this task's own Out-of-scope note) will call — and proves neither
// a data race (run with -race) nor a deadlock results, and that a status
// call made after every actor settles still composes a schema-valid reply.
func TestServeStatus_ThreeWayConcurrency(t *testing.T) {
	svc := startedServiceForStatus(t, activity.New(16))
	svc.q.Register(&alwaysAcceptListener{id: "h1", typ: "t"})

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			var out strings.Builder
			svc.Serve(SubcommandStatus, strings.NewReader(statusRequest), &out)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			id := fmt.Sprintf("e%d", i)
			_, _ = svc.q.Enqueue(eventqueue.Event{ID: id, Type: "t", ExpiresAt: time.Now().Add(time.Hour)})
			svc.q.Dispatch()
			svc.q.Expire()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			svc.ObserveGateFromSocketVerb(time.Now(), "quota_paused", GateInfo{Set: i%2 == 0})
		}
	}()

	wg.Wait()

	reply, code := serveStatus(t, svc, statusRequest)
	if code != conformance.ExitOK {
		t.Fatalf("final status exit = %d, want 0; reply=%v", code, reply)
	}
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("final status reply failed its own schema: %v", err)
	}
}
