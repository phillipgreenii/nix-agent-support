package wireclient

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// fakeRunner is a scripted Runner test double: it records the argv/stdin it
// was called with and returns the scripted (stdout, exitCode, err) triple.
type fakeRunner struct {
	stdout   []byte
	exitCode int
	err      error

	gotArgv  []string
	gotStdin []byte
}

func (f *fakeRunner) Run(_ context.Context, argv []string, stdin []byte) ([]byte, int, error) {
	f.gotArgv = argv
	f.gotStdin = stdin
	return f.stdout, f.exitCode, f.err
}

func fixedCommand(argv ...string) CommandFor {
	return func(roles.Role) ([]string, error) { return argv, nil }
}

// TestDispatch_sendsSchemaLegalRequestAndAppendsSubcommand locks the exact
// request shape (packages/pg-router/schemas/handler.dispatch.schema.json)
// and confirms "dispatch" is appended to the resolved command argv
// (DEC-WIRE-1: "invokes a participant as `<command> <subcommand>`").
func TestDispatch_sendsSchemaLegalRequestAndAppendsSubcommand(t *testing.T) {
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","id":"dsp-x","outcome":"delivered"}`), exitCode: 0}
	c := &Client{Runner: run, Command: fixedCommand("pg-router-ccpool-handler", "--role-config", "/rc.json")}
	role := roles.Role{Name: "worker", Binds: []string{"work.ready"}}
	evt := eventqueue.Event{ID: "evt-1", Type: "work.ready", Payload: map[string]any{"item": map[string]any{"id": "zr-1"}}}

	if _, err := c.Dispatch(context.Background(), role, evt); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	wantArgv := []string{"pg-router-ccpool-handler", "--role-config", "/rc.json", "dispatch"}
	if len(run.gotArgv) != len(wantArgv) {
		t.Fatalf("argv = %v, want %v", run.gotArgv, wantArgv)
	}
	for i, a := range wantArgv {
		if run.gotArgv[i] != a {
			t.Fatalf("argv = %v, want %v", run.gotArgv, wantArgv)
		}
	}

	var req map[string]any
	if err := json.Unmarshal(run.gotStdin, &req); err != nil {
		t.Fatalf("request is not valid JSON: %v; body=%s", err, run.gotStdin)
	}
	if req["schemaVersion"] != "1" {
		t.Errorf("schemaVersion = %v, want \"1\"", req["schemaVersion"])
	}
	id, _ := req["id"].(string)
	if id == "" {
		t.Errorf("request must carry a non-empty dispatch tracking id")
	}
	evField, ok := req["event"].(map[string]any)
	if !ok {
		t.Fatalf("request must carry an event object; got %v", req)
	}
	if evField["id"] != "evt-1" || evField["type"] != "work.ready" {
		t.Errorf("event id/type = %v/%v, want evt-1/work.ready", evField["id"], evField["type"])
	}
	payload, ok := evField["payload"].(map[string]any)
	if !ok {
		t.Fatalf("event must carry the payload verbatim; got %v", evField)
	}
	if _, ok := payload["item"]; !ok {
		t.Errorf("payload must be passed through opaquely; got %v", payload)
	}
}

// TestDispatch_freshIDPerCall confirms every Dispatch call mints its own
// tracking id (re-offer mints a fresh id, matching internal/eventqueue's
// own Task 2.2 minting behavior for a re-offered attempt).
func TestDispatch_freshIDPerCall(t *testing.T) {
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","id":"ignored","outcome":"delivered"}`), exitCode: 0}
	c := &Client{Runner: run, Command: fixedCommand("h")}
	role := roles.Role{Name: "r"}
	evt := eventqueue.Event{ID: "e", Type: "t"}

	ids := map[string]bool{}
	for i := 0; i < 5; i++ {
		if _, err := c.Dispatch(context.Background(), role, evt); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		var req map[string]any
		if err := json.Unmarshal(run.gotStdin, &req); err != nil {
			t.Fatal(err)
		}
		id, _ := req["id"].(string)
		ids[id] = true
	}
	if len(ids) != 5 {
		t.Fatalf("Dispatch must mint a fresh id every call; got %d distinct ids across 5 calls: %v", len(ids), ids)
	}
}

// TestDispatch_replyEchoesOutcomeUnmodified locks Task 5.4's Acceptance
// criterion: Dispatch never interprets Outcome, just returns it verbatim.
func TestDispatch_replyEchoesOutcomeUnmodified(t *testing.T) {
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","id":"dsp-x","outcome":"{\"actions\":[]}"}`), exitCode: 0}
	c := &Client{Runner: run, Command: fixedCommand("h")}
	reply, err := c.Dispatch(context.Background(), roles.Role{Name: "r"}, eventqueue.Event{ID: "e", Type: "t"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if reply.Outcome != `{"actions":[]}` {
		t.Errorf("Outcome = %q, want the raw string unmodified", reply.Outcome)
	}
	if reply.Deferred {
		t.Errorf("Deferred = true, want false")
	}
}

// TestDispatch_deferredReplySetsDeferred locks the deferred-ack shape
// (DEC-WIRE-1: "{ deferred: true }").
func TestDispatch_deferredReplySetsDeferred(t *testing.T) {
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","id":"dsp-x","deferred":true}`), exitCode: 0}
	c := &Client{Runner: run, Command: fixedCommand("h")}
	reply, err := c.Dispatch(context.Background(), roles.Role{Name: "r"}, eventqueue.Event{ID: "e", Type: "t"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !reply.Deferred {
		t.Errorf("Deferred = false, want true")
	}
}

// TestDispatch_exitCode9IsBusy locks DEC-WIRE-1's coarse exit codes: 9 maps
// to ErrBusy, the wireclient successor to the retired executor.ErrBusy.
func TestDispatch_exitCode9IsBusy(t *testing.T) {
	run := &fakeRunner{exitCode: 9}
	c := &Client{Runner: run, Command: fixedCommand("h")}
	_, err := c.Dispatch(context.Background(), roles.Role{Name: "r"}, eventqueue.Event{ID: "e", Type: "t"})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want errors.Is(err, ErrBusy)", err)
	}
}

// TestDispatch_otherNonZeroExitIsError locks that any other coarse exit code
// (1 unexpected error, 2 usage) is a plain error, never confused with busy.
func TestDispatch_otherNonZeroExitIsError(t *testing.T) {
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","error":"boom"}`), exitCode: 1}
	c := &Client{Runner: run, Command: fixedCommand("h")}
	_, err := c.Dispatch(context.Background(), roles.Role{Name: "r"}, eventqueue.Event{ID: "e", Type: "t"})
	if err == nil {
		t.Fatal("expected an error on exit 1")
	}
	if errors.Is(err, ErrBusy) {
		t.Fatal("exit 1 must not be classified as ErrBusy")
	}
}

// TestDispatch_noCommandForIsError proves a misconfigured Client (no
// CommandFor injected) fails loudly rather than panicking or silently
// invoking an empty argv.
func TestDispatch_noCommandForIsError(t *testing.T) {
	c := &Client{Runner: &fakeRunner{}}
	_, err := c.Dispatch(context.Background(), roles.Role{Name: "r"}, eventqueue.Event{ID: "e", Type: "t"})
	if err == nil {
		t.Fatal("expected an error with no CommandFor configured")
	}
}

// TestNew_defaultsRunnerToOSRunner locks New's documented default.
func TestNew_defaultsRunnerToOSRunner(t *testing.T) {
	c := New(fixedCommand("h"))
	if _, ok := c.Runner.(OSRunner); !ok {
		t.Fatalf("New must default Runner to OSRunner{}, got %T", c.Runner)
	}
}
