package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/kvstore"
	"github.com/phillipgreenii/pg-router/schemas"
)

// startedServiceWithStore returns a started, socket-less service (the same
// literal-construction pattern startedServiceWith/startedServiceWithMonitoring
// already use) carrying store as its Service.store — bypassing Listen's own
// kvstore.NewInMemory() default so a test can hand in whichever kvstore.Store
// it wants to exercise get/put/delete against.
func startedServiceWithStore(t *testing.T, store kvstore.Store) *Service {
	t.Helper()
	return &Service{
		state:    conformance.Started,
		q:        newQueue(t),
		bindings: testBindings(),
		reg:      NewRegistry(nil),
		command:  "pg-router",
		store:    store,
	}
}

// serveStore runs subcommand IN PROCESS through the participant boundary
// (the same entry point the socket transport funnels into) and returns the
// decoded reply plus exit code — the same shape serveIngest/serveMonRead
// already use.
func serveStore(t *testing.T, svc *Service, subcommand, request string) (map[string]any, int) {
	t.Helper()
	var out strings.Builder
	code := svc.Serve(subcommand, strings.NewReader(request), &out)
	var reply map[string]any
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", out.String(), err)
	}
	return reply, code
}

// TestStorePutGetDelete_InMemoryDefault is Task 6.7's round-trip proof
// against Service.store's DEFAULT (kvstore.NewInMemory(), never swapped —
// no KindStorage participant registers in this test): put a value, read it
// back, delete it, then read again — the absent-key case MUST report
// {ok:false, value:null} end-to-end, not an omitted value field (Task 6.7's
// own widened store.reply.schema.json is what makes an explicit null legal).
func TestStorePutGetDelete_InMemoryDefault(t *testing.T) {
	svc := startedServiceWithStore(t, kvstore.NewInMemory())

	putReply, code := serveStore(t, svc, SubcommandPut, `{"schemaVersion":"1","id":"s-1","op":"put","key":"k1","value":"v1"}`)
	if code != conformance.ExitOK {
		t.Fatalf("put exit = %d, want %d; reply=%v", code, conformance.ExitOK, putReply)
	}
	if err := conformance.Check(StoreReplySchema, putReply); err != nil {
		t.Fatalf("put reply failed its own schema (INV-INTF-2): %v", err)
	}
	if putReply["ok"] != true {
		t.Fatalf("put reply ok = %v, want true", putReply["ok"])
	}

	getReply, code := serveStore(t, svc, SubcommandGet, `{"schemaVersion":"1","id":"s-1","op":"get","key":"k1"}`)
	if code != conformance.ExitOK {
		t.Fatalf("get exit = %d, want %d; reply=%v", code, conformance.ExitOK, getReply)
	}
	if err := conformance.Check(StoreReplySchema, getReply); err != nil {
		t.Fatalf("get reply failed its own schema: %v", err)
	}
	if getReply["ok"] != true || getReply["value"] != "v1" {
		t.Fatalf("get reply = %v, want ok=true value=v1", getReply)
	}

	delReply, code := serveStore(t, svc, SubcommandDelete, `{"schemaVersion":"1","id":"s-1","op":"delete","key":"k1"}`)
	if code != conformance.ExitOK {
		t.Fatalf("delete exit = %d, want %d; reply=%v", code, conformance.ExitOK, delReply)
	}
	if delReply["ok"] != true {
		t.Fatalf("delete reply ok = %v, want true", delReply["ok"])
	}

	afterReply, code := serveStore(t, svc, SubcommandGet, `{"schemaVersion":"1","id":"s-1","op":"get","key":"k1"}`)
	if code != conformance.ExitOK {
		t.Fatalf("get-after-delete exit = %d, want %d; reply=%v", code, conformance.ExitOK, afterReply)
	}
	if err := conformance.Check(StoreReplySchema, afterReply); err != nil {
		t.Fatalf("absent-key reply failed its own schema (must accept value: null): %v", err)
	}
	value, hasValue := afterReply["value"]
	if !hasValue || value != nil {
		t.Fatalf("absent-key get reply value = %v (present=%v), want an explicit null", value, hasValue)
	}
	if afterReply["ok"] != false {
		t.Fatalf("absent-key get reply ok = %v, want false", afterReply["ok"])
	}
}

// Deleting a key that was never present is a no-op, not an error (kvstore.
// Store's own documented contract) — the socket verb must report ok=true,
// never surface that as a failure.
func TestStoreDelete_AbsentKeyIsNoOp(t *testing.T) {
	svc := startedServiceWithStore(t, kvstore.NewInMemory())
	reply, code := serveStore(t, svc, SubcommandDelete, `{"schemaVersion":"1","id":"s-1","op":"delete","key":"never-put"}`)
	if code != conformance.ExitOK || reply["ok"] != true {
		t.Fatalf("delete-absent reply=%v code=%d, want ok=true exit=%d", reply, code, conformance.ExitOK)
	}
}

// A request whose own `op` field disagrees with the dispatched subcommand is
// rejected as a request-shaped error, never silently misrouted to the wrong
// store method (Binding decision 3: get/put/delete are three DISTINCT
// verbs, not one "store" verb branching on op).
func TestStoreRequest_OpMismatchRejected(t *testing.T) {
	svc := startedServiceWithStore(t, kvstore.NewInMemory())
	_, code := serveStore(t, svc, SubcommandGet, `{"schemaVersion":"1","id":"s-1","op":"put","key":"k1","value":"v1"}`)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d for a request whose op does not match the dispatched subcommand", code, conformance.ExitError)
	}
}

// fakeStorageParticipant is the test-local stub callback Task 6.7's own
// packet body names as the authorized way to exercise wireStore's
// correctness — never a live registered KindStorage participant, which no
// production command-resolution seam can reach today (the BLOCKING GAP
// recorded in storeclient.go's package doc). It implements
// wireclient.Runner directly, over its own map[string]string, branching on
// the VERB argv's own last element carries (mirroring what a real
// participant binary would receive as its subcommand argument) rather than
// the request body's own `op` field, so the test genuinely exercises the
// argv-invocation mechanism wireStore.call reuses from wireclient.Runner.
type fakeStorageParticipant struct {
	mu   sync.Mutex
	data map[string]string
}

func (f *fakeStorageParticipant) Run(_ context.Context, argv []string, stdin []byte) ([]byte, int, error) {
	if len(argv) == 0 {
		return nil, 0, errors.New("fakeStorageParticipant: empty argv")
	}
	var req storeRequestWire
	if err := json.Unmarshal(stdin, &req); err != nil {
		return nil, 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := storeReplyWire{SchemaVersion: schemas.SchemaVersion, ID: req.ID}
	ok := true
	switch argv[len(argv)-1] {
	case SubcommandGet:
		if v, found := f.data[req.Key]; found {
			reply.Value = &v
		}
		// absent key: Value stays nil -> the wire's {value: null} shape.
	case SubcommandPut:
		f.data[req.Key] = *req.Value
	case SubcommandDelete:
		delete(f.data, req.Key)
	default:
		ok = false
	}
	reply.OK = &ok
	body, err := json.Marshal(reply)
	return body, 0, err
}

// TestStore_WireForwardingToFakeParticipant proves Service.Register's
// KindStorage branch actually performs the documented swap (Binding
// decision 4): registering a KindStorage participant makes subsequent get/
// put/delete calls forward over wireStore/wireclient.Runner to the fake
// participant above, round-tripping a real value AND the absent-key
// value:null case, end-to-end.
func TestStore_WireForwardingToFakeParticipant(t *testing.T) {
	svc := startedServiceWithStore(t, kvstore.NewInMemory())
	fake := &fakeStorageParticipant{data: map[string]string{}}
	svc.storeCommand = func(id string) ([]string, error) { return []string{"fake-storage-cmd", "--id", id}, nil }
	svc.storeRunner = fake

	if _, err := svc.Register("storage-1", KindStorage); err != nil {
		t.Fatalf("Register: %v", err)
	}

	putReply, code := serveStore(t, svc, SubcommandPut, `{"schemaVersion":"1","id":"s-1","op":"put","key":"k1","value":"v1"}`)
	if code != conformance.ExitOK || putReply["ok"] != true {
		t.Fatalf("put reply=%v code=%d, want ok=true exit=%d", putReply, code, conformance.ExitOK)
	}

	getReply, code := serveStore(t, svc, SubcommandGet, `{"schemaVersion":"1","id":"s-1","op":"get","key":"k1"}`)
	if code != conformance.ExitOK || getReply["ok"] != true || getReply["value"] != "v1" {
		t.Fatalf("get reply=%v code=%d, want ok=true value=v1 exit=%d", getReply, code, conformance.ExitOK)
	}

	delReply, code := serveStore(t, svc, SubcommandDelete, `{"schemaVersion":"1","id":"s-1","op":"delete","key":"k1"}`)
	if code != conformance.ExitOK || delReply["ok"] != true {
		t.Fatalf("delete reply=%v code=%d, want ok=true exit=%d", delReply, code, conformance.ExitOK)
	}

	afterReply, code := serveStore(t, svc, SubcommandGet, `{"schemaVersion":"1","id":"s-1","op":"get","key":"k1"}`)
	if code != conformance.ExitOK {
		t.Fatalf("get-after-delete exit = %d, want %d", code, conformance.ExitOK)
	}
	value, hasValue := afterReply["value"]
	if !hasValue || value != nil {
		t.Fatalf("absent-key value via wire-forwarding = %v (present=%v), want an explicit null", value, hasValue)
	}
	if afterReply["ok"] != false {
		t.Fatalf("absent-key ok via wire-forwarding = %v, want false", afterReply["ok"])
	}
}

// TestStore_WireForwardingWithNoCommandResolverErrors proves the BLOCKING
// GAP is realized HONESTLY: with storeCommand/storeRunner both left nil —
// exactly the shape of every real deployment today, since no production
// wiring sets Options.StoreCommand yet — Register still performs the
// documented swap (Binding decision 4), but calling into the resulting
// store reports the missing command-resolution plainly rather than
// pretending a registered participant is reachable over the wire.
func TestStore_WireForwardingWithNoCommandResolverErrors(t *testing.T) {
	svc := startedServiceWithStore(t, kvstore.NewInMemory())
	if _, err := svc.Register("storage-2", KindStorage); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, code := serveStore(t, svc, SubcommandGet, `{"schemaVersion":"1","id":"s-1","op":"get","key":"k1"}`)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d (no command-resolution configured for the registered storage participant)", code, conformance.ExitError)
	}
}
