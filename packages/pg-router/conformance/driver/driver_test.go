package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// TestRun_ReproducesConformanceCases proves Run's static schema/golden case set
// exactly reproduces the pass/fail set package conformance's own tests used to
// assert directly before Task 3.13 moved that case-running logic here
// (TestGoldenFixturesValidate, TestNegative_Generic, TestNegative_Matrix): every
// golden/negative-generic/negative-matrix Result passes with a nil Target,
// since none of those cases ever needed a live participant. The four
// invoking/*-shaped results are Skipped instead — Target{} carries no
// mon.read, query, command-role, or store participant, so each invoking
// check reports Skipped via its own nil convention (Task 3.13 Binding
// decisions; invoking/command added by Task pg2-2j5ac.23.3; invoking/store's
// nil-Skipped convention added by Task 6.8 once INTF-STORE was realized —
// Task 6.7).
func TestRun_ReproducesConformanceCases(t *testing.T) {
	results := Run(context.Background(), Target{})
	if len(results) == 0 {
		t.Fatal("Run produced no results")
	}
	for _, r := range results {
		t.Run(r.Name, func(t *testing.T) {
			switch r.Name {
			case "invoking/mon.read", "invoking/query", "invoking/command", "invoking/store":
				if !r.Skipped {
					t.Fatalf("expected %s to be skipped against an empty Target, got err=%v", r.Name, r.Err)
				}
			default:
				if r.Skipped {
					t.Fatalf("unexpected skip: %s", r.SkipReason)
				}
				if r.Err != nil {
					t.Fatalf("%s: %v", r.Name, r.Err)
				}
			}
		})
	}
}

// TestRun_InvokingStore_SkippedWhenNil proves the store check reports Skipped
// with a store-specific reason when Target carries no Store participant —
// mirroring MonRead/Query's nil-Skipped convention (Target's own doc
// comment). This replaces the old TestRun_StoreAlwaysSkipped: INTF-STORE is
// realized now (Task 6.7), so the store check is no longer unconditionally
// skipped, and the old "enabled by Task 6.0" reason string is gone (Task 6.8
// Produces).
func TestRun_InvokingStore_SkippedWhenNil(t *testing.T) {
	r := findResult(t, Run(context.Background(), Target{}), "invoking/store")
	const want = "target carries no store participant"
	if !r.Skipped || r.Err != nil || r.SkipReason != want {
		t.Fatalf("invoking/store = %+v, want Skipped=true, no error, reason %q", r, want)
	}
}

// TestRun_InvokingStore_Pass proves the store check runs and passes a
// put/get/delete round trip against a live Target.Store (Task 6.8 Produces):
// put a value, get it back, delete it, then get once more and confirm the
// absent-key convention (value: null).
func TestRun_InvokingStore_Pass(t *testing.T) {
	target := Target{Store: &fakeStoreParticipant{}}
	r := findResult(t, Run(context.Background(), target), "invoking/store")
	if r.Skipped || r.Err != nil {
		t.Fatalf("invoking/store = %+v, want a clean pass", r)
	}
}

// TestRun_InvokingStore_GetAfterPutMismatch proves invokeStore fails when a
// participant's get does not echo back the value just put — the round-trip
// assertion this task's Produces requires, not merely that each verb
// individually returns a schema-valid reply.
func TestRun_InvokingStore_GetAfterPutMismatch(t *testing.T) {
	target := Target{Store: &fakeStoreParticipant{corruptGet: true}}
	r := findResult(t, Run(context.Background(), target), "invoking/store")
	if r.Err == nil {
		t.Fatal("expected invoking/store to fail when get-after-put does not echo the put value")
	}
}

// TestRun_ContextCanceled proves Run honors an already-canceled context rather
// than running the case set regardless.
func TestRun_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := Run(ctx, Target{})
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("Run(canceled ctx) = %+v, want a single error result", results)
	}
}

// fakeParticipant is a minimal conformance.Participant double used only to
// prove Run's invoking checks actually call through a live Target and
// validate the reply that comes back — this task does not wire a real
// core.Service (out of scope; Task 3.13 Files/Contract), just the driver
// mechanism a later caller's own live mon.read/query participant plugs into.
type fakeParticipant struct {
	reply []byte
	code  int
}

func (f fakeParticipant) Serve(subcommand string, stdin io.Reader, stdout io.Writer) int {
	_, _ = io.ReadAll(stdin) // the request is not inspected; this fake always answers the same way
	_, _ = stdout.Write(f.reply)
	return f.code
}

func validMonReadReply(t *testing.T) []byte {
	t.Helper()
	g, err := conformance.Golden("mon.read-reply")
	if err != nil {
		t.Fatalf("read mon.read-reply golden: %v", err)
	}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal mon.read-reply golden: %v", err)
	}
	return b
}

func validQueryReply(t *testing.T) []byte {
	t.Helper()
	g, err := conformance.Golden("source.query-reply")
	if err != nil {
		t.Fatalf("read source.query-reply golden: %v", err)
	}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal source.query-reply golden: %v", err)
	}
	return b
}

func TestRun_InvokingMonRead_Pass(t *testing.T) {
	target := Target{MonRead: fakeParticipant{reply: validMonReadReply(t), code: conformance.ExitOK}}
	r := findResult(t, Run(context.Background(), target), "invoking/mon.read")
	if r.Skipped || r.Err != nil {
		t.Fatalf("invoking/mon.read = %+v, want a clean pass", r)
	}
}

func TestRun_InvokingMonRead_BadExitCode(t *testing.T) {
	target := Target{MonRead: fakeParticipant{reply: validMonReadReply(t), code: conformance.ExitError}}
	r := findResult(t, Run(context.Background(), target), "invoking/mon.read")
	if r.Err == nil {
		t.Fatal("expected invoking/mon.read to fail on a non-OK exit code")
	}
}

func TestRun_InvokingMonRead_MalformedReply(t *testing.T) {
	target := Target{MonRead: fakeParticipant{reply: []byte("{ not json"), code: conformance.ExitOK}}
	r := findResult(t, Run(context.Background(), target), "invoking/mon.read")
	if r.Err == nil {
		t.Fatal("expected invoking/mon.read to fail on a malformed reply")
	}
}

func TestRun_InvokingMonRead_SchemaViolation(t *testing.T) {
	target := Target{MonRead: fakeParticipant{reply: []byte(`{"schemaVersion":"1","id":"m-1"}`), code: conformance.ExitOK}}
	r := findResult(t, Run(context.Background(), target), "invoking/mon.read")
	if r.Err == nil {
		t.Fatal("expected invoking/mon.read to fail a reply missing required values")
	}
}

func TestRun_InvokingQuery_Pass(t *testing.T) {
	target := Target{Query: fakeParticipant{reply: validQueryReply(t), code: conformance.ExitOK}}
	r := findResult(t, Run(context.Background(), target), "invoking/query")
	if r.Skipped || r.Err != nil {
		t.Fatalf("invoking/query = %+v, want a clean pass", r)
	}
}

func TestRun_InvokingQuery_BadExitCode(t *testing.T) {
	target := Target{Query: fakeParticipant{reply: validQueryReply(t), code: conformance.ExitError}}
	r := findResult(t, Run(context.Background(), target), "invoking/query")
	if r.Err == nil {
		t.Fatal("expected invoking/query to fail on a non-OK exit code")
	}
}

func TestRun_InvokingQuery_MalformedReply(t *testing.T) {
	target := Target{Query: fakeParticipant{reply: []byte("{ not json"), code: conformance.ExitOK}}
	r := findResult(t, Run(context.Background(), target), "invoking/query")
	if r.Err == nil {
		t.Fatal("expected invoking/query to fail on a malformed reply")
	}
}

func TestRun_InvokingQuery_SchemaViolation(t *testing.T) {
	target := Target{Query: fakeParticipant{reply: []byte(`{"schemaVersion":"1","id":"q-1"}`), code: conformance.ExitOK}}
	r := findResult(t, Run(context.Background(), target), "invoking/query")
	if r.Err == nil {
		t.Fatal("expected invoking/query to fail a reply naming neither branch")
	}
}

// fakeCommandParticipant is a minimal CommandParticipant double used to prove
// invokeCommand's exit-code classification without a real subprocess — this
// task's Freedom boundary explicitly leaves open whether the test double is a
// real compiled fixture binary or an in-process fake; this is the latter.
type fakeCommandParticipant struct {
	code int
	err  error
}

func (f fakeCommandParticipant) Run(ctx context.Context) (int, error) { return f.code, f.err }

func TestRun_InvokingCommand_Pass(t *testing.T) {
	target := Target{Command: fakeCommandParticipant{code: conformance.ExitOK}}
	r := findResult(t, Run(context.Background(), target), "invoking/command")
	if r.Skipped || r.Err != nil || r.Busy {
		t.Fatalf("invoking/command = %+v, want a clean pass", r)
	}
}

func TestRun_InvokingCommand_Busy(t *testing.T) {
	target := Target{Command: fakeCommandParticipant{code: conformance.ExitBusy}}
	r := findResult(t, Run(context.Background(), target), "invoking/command")
	if r.Err != nil || r.Skipped || !r.Busy {
		t.Fatalf("invoking/command = %+v, want Busy=true, no error, not skipped", r)
	}
}

func TestRun_InvokingCommand_PlainFailure(t *testing.T) {
	// A plain non-zero exit outside the reserved/collision set (0, 2, 3, 9) —
	// e.g. exit 1, ExitError — is a genuine conformance failure.
	target := Target{Command: fakeCommandParticipant{code: conformance.ExitError}}
	r := findResult(t, Run(context.Background(), target), "invoking/command")
	if r.Err == nil || r.Busy {
		t.Fatalf("invoking/command = %+v, want a plain Err, Busy=false", r)
	}
}

func TestRun_InvokingCommand_CoreReservedViolation(t *testing.T) {
	// Exit 3 (exitCoreReserved, DEC-WIRE-1) is core-reserved pre-flight — no
	// participant may legitimately emit it.
	target := Target{Command: fakeCommandParticipant{code: exitCoreReserved}}
	r := findResult(t, Run(context.Background(), target), "invoking/command")
	if r.Err == nil {
		t.Fatal("expected invoking/command to flag exit 3 (core-reserved) as a violation")
	}
}

func TestRun_InvokingCommand_UsageViolation(t *testing.T) {
	// Exit 2 (conformance.ExitUsage) is flagged here too, but for the
	// project-specific pg-connector-collision reason (Task pg2-2j5ac.23.3
	// Binding decisions #1) — not a blanket DEC-WIRE-1 prohibition.
	target := Target{Command: fakeCommandParticipant{code: conformance.ExitUsage}}
	r := findResult(t, Run(context.Background(), target), "invoking/command")
	if r.Err == nil {
		t.Fatal("expected invoking/command to flag exit 2 (usage) as a violation")
	}
}

func TestRun_InvokingCommand_SkippedWhenNil(t *testing.T) {
	r := findResult(t, Run(context.Background(), Target{}), "invoking/command")
	if !r.Skipped || r.Err != nil {
		t.Fatalf("invoking/command = %+v, want Skipped=true, no error, against a Target with no Command", r)
	}
}

// fakeStoreParticipant is a minimal in-memory conformance.Participant double
// exercising get/put/delete (Task 6.7's SubcommandGet/Put/Delete) the same
// way internal/core.Service's real handleGet/handlePut/handleDelete do,
// without pulling in internal/core — this driver package deliberately stays
// independent of it (Task 3.13 Binding decisions). corruptGet, when set,
// makes get always report a value distinct from whatever was actually put,
// to prove invokeStore's round-trip assertion — not just a per-verb schema
// check — is what fails.
type fakeStoreParticipant struct {
	data       map[string]string
	corruptGet bool
}

func (f *fakeStoreParticipant) Serve(subcommand string, stdin io.Reader, stdout io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return conformance.ExitError
	}
	var req struct {
		ID    string `json:"id"`
		Op    string `json:"op"`
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return conformance.ExitError
	}
	if f.data == nil {
		f.data = map[string]string{}
	}
	reply := map[string]any{"schemaVersion": "1", "id": req.ID}
	switch subcommand {
	case "put":
		f.data[req.Key] = req.Value
		reply["ok"] = true
	case "get":
		v, found := f.data[req.Key]
		if found && f.corruptGet {
			v = v + "-corrupted"
		}
		reply["ok"] = found
		if found {
			reply["value"] = v
		} else {
			reply["value"] = nil
		}
	case "delete":
		delete(f.data, req.Key)
		reply["ok"] = true
	default:
		return conformance.ExitError
	}
	b, err := json.Marshal(reply)
	if err != nil {
		return conformance.ExitError
	}
	_, _ = stdout.Write(b)
	return conformance.ExitOK
}

// findResult locates the one result named name, failing the test outright if
// Run produced none — every case below expects exactly one.
func findResult(t *testing.T, results []Result, name string) Result {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no result named %q", name)
	return Result{}
}

// TestNegativeMatrixCompleteness gates the schema-change mechanics rule
// (Global Constraints): adding/renaming a schemas/*.schema.json name requires
// >=1 negative row in the SAME commit. It checks every registered message
// type (schemas.Names(), via conformance.MessageTypes()) has at least one
// negativeMatrix row here, so a new schema landing with no negative case at
// all is caught rather than silently passing every other conformance case
// (Task 2.1 Step 2.1.3; negativeMatrix moved here from package conformance's
// own tests by the Task 3.13 extraction).
func TestNegativeMatrixCompleteness(t *testing.T) {
	for _, mt := range conformance.MessageTypes() {
		if len(negativeMatrix[mt]) == 0 {
			t.Errorf("%s has no negative-matrix row", mt)
		}
	}
}

// TestResult_ErrorIsNilOnPass documents Result's zero value as the passing
// shape (no Err, not Skipped) — the same convention Run's static cases rely on.
func TestResult_ErrorIsNilOnPass(t *testing.T) {
	var r Result
	if r.Err != nil || r.Skipped {
		t.Fatalf("zero Result = %+v, want a clean pass", r)
	}
	if !errors.Is(r.Err, nil) {
		t.Fatal("nil Err must satisfy errors.Is(nil, nil)")
	}
}
