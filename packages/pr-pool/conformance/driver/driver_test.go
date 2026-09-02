package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/phillipgreenii/pr-pool/conformance"
)

// TestRun_ReproducesConformanceCases proves Run's static schema/golden case set
// exactly reproduces the pass/fail set package conformance's own tests used to
// assert directly before Task 3.13 moved that case-running logic here
// (TestGoldenFixturesValidate, TestNegative_Generic, TestNegative_Matrix): every
// golden/negative-generic/negative-matrix Result passes with a nil Target,
// since none of those cases ever needed a live participant. The three
// invoking/store-shaped results are Skipped instead — Target{} carries no
// mon.read or query participant, and the store check is unconditionally
// pre-skipped (Task 3.13 Binding decisions).
func TestRun_ReproducesConformanceCases(t *testing.T) {
	results := Run(context.Background(), Target{})
	if len(results) == 0 {
		t.Fatal("Run produced no results")
	}
	for _, r := range results {
		t.Run(r.Name, func(t *testing.T) {
			switch r.Name {
			case "invoking/mon.read", "invoking/query", "invoking/store":
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

// TestRun_StoreAlwaysSkipped proves the store check ships pre-skipped with the
// exact reason string regardless of Target (INTF-STORE is not realized until
// Phase 6, Task 3.13 Binding decisions / Objective).
func TestRun_StoreAlwaysSkipped(t *testing.T) {
	results := Run(context.Background(), Target{})
	found := false
	for _, r := range results {
		if r.Name != "invoking/store" {
			continue
		}
		found = true
		if !r.Skipped || r.SkipReason != "enabled by Task 6.0" {
			t.Fatalf("store case = %+v, want Skipped with reason %q", r, "enabled by Task 6.0")
		}
	}
	if !found {
		t.Fatal("Run produced no invoking/store result")
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
