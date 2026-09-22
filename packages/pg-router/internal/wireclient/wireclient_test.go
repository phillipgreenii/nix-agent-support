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

// fixedCommand returns a CommandFor test double that appends the subcommand
// it is called with onto a fixed prefix — mirroring handlerCommandFor's own
// fallback-branch shape ([cfg.HandlerCommand, subcommand]) so existing
// argv-shape assertions below stay meaningful now that CommandFor resolves
// the subcommand's POSITION itself (pg2-ymb3v) rather than having it
// appended by wireclient's own call sites.
func fixedCommand(argv ...string) CommandFor {
	return func(_ roles.Role, subcommand string) ([]string, error) {
		full := make([]string, 0, len(argv)+1)
		full = append(full, argv...)
		full = append(full, subcommand)
		return full, nil
	}
}

// TestDispatch_sendsSchemaLegalRequestAndAppendsSubcommand locks the exact
// request shape (packages/pg-router/schemas/handler.dispatch.schema.json)
// and confirms "dispatch" lands as the LAST token of the resolved command
// argv (DEC-WIRE-1: "invokes a participant as `<command> <subcommand>`") —
// fixedCommand's own doc comment above is what actually appends it now.
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

// TestDispatch_exitCode9WithReasonBodyExposesReason proves Dispatch reads an
// OPTIONAL reply body on exit 9 (bead pg2-j4uwg; DEC-WIRE-1 / interfaces.md's
// "Coarse outcome, rich reply") and exposes it via errors.As(err,
// *BusyDecline), while errors.Is(err, ErrBusy) still matches exactly as it
// did before this bead (TestDispatch_exitCode9IsBusy, unchanged).
func TestDispatch_exitCode9WithReasonBodyExposesReason(t *testing.T) {
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","reason":"capacity-unknown"}`), exitCode: 9}
	c := &Client{Runner: run, Command: fixedCommand("h")}
	_, err := c.Dispatch(context.Background(), roles.Role{Name: "r"}, eventqueue.Event{ID: "e", Type: "t"})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want errors.Is(err, ErrBusy)", err)
	}
	var bd *BusyDecline
	if !errors.As(err, &bd) {
		t.Fatalf("err = %v, want errors.As(err, *BusyDecline)", err)
	}
	if bd.Reason != "capacity-unknown" {
		t.Fatalf("Reason = %q, want %q", bd.Reason, "capacity-unknown")
	}
}

// TestDispatch_exitCode9WithNoBodyLeavesReasonEmpty proves the pre-existing,
// body-less busy decline (DEC-WIRE-1: "no body required") stays fully legal:
// Reason reads back as "" rather than erroring or panicking.
func TestDispatch_exitCode9WithNoBodyLeavesReasonEmpty(t *testing.T) {
	run := &fakeRunner{exitCode: 9}
	c := &Client{Runner: run, Command: fixedCommand("h")}
	_, err := c.Dispatch(context.Background(), roles.Role{Name: "r"}, eventqueue.Event{ID: "e", Type: "t"})
	var bd *BusyDecline
	if !errors.As(err, &bd) {
		t.Fatalf("err = %v, want errors.As(err, *BusyDecline)", err)
	}
	if bd.Reason != "" {
		t.Fatalf("Reason = %q, want \"\" for a body-less busy decline", bd.Reason)
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

// TestDispatch_passesSubcommandToCommandFor proves Dispatch hands "dispatch"
// to CommandFor as a PARAMETER rather than appending it itself after the
// call (pg2-ymb3v's widened seam) — a CommandFor implementation can
// therefore place extra tokens (e.g. a per-role --role-config path) AFTER
// the subcommand, which the retired append-at-the-end shape could never do
// without landing those tokens before the subcommand and getting rejected
// by the participant's own CLI as an unknown subcommand.
func TestDispatch_passesSubcommandToCommandFor(t *testing.T) {
	var gotSubcommand string
	cmd := func(role roles.Role, subcommand string) ([]string, error) {
		gotSubcommand = subcommand
		return []string{"h", subcommand, "--role-config", role.Name + ".json"}, nil
	}
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","id":"dsp-x","outcome":"delivered"}`), exitCode: 0}
	c := &Client{Runner: run, Command: cmd}
	if _, err := c.Dispatch(context.Background(), roles.Role{Name: "worker"}, eventqueue.Event{ID: "e", Type: "t"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if gotSubcommand != "dispatch" {
		t.Errorf("subcommand passed to CommandFor = %q, want %q", gotSubcommand, "dispatch")
	}
	want := []string{"h", "dispatch", "--role-config", "worker.json"}
	if len(run.gotArgv) != len(want) {
		t.Fatalf("argv = %v, want %v", run.gotArgv, want)
	}
	for i, a := range want {
		if run.gotArgv[i] != a {
			t.Fatalf("argv = %v, want %v (subcommand MUST be argv[1], before any flags)", run.gotArgv, want)
		}
	}
}

// TestPostStartup_passesSubcommandToCommandFor / TestPreShutdown_... lock the
// other two of the three CommandFor call sites (Dispatch above is the
// third): both go through lifecycleCall, which must pass its own literal
// subcommand name through unmodified.
func TestPostStartup_passesSubcommandToCommandFor(t *testing.T) {
	var gotSubcommand string
	cmd := func(_ roles.Role, subcommand string) ([]string, error) {
		gotSubcommand = subcommand
		return []string{"h", subcommand}, nil
	}
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","id":"x"}`), exitCode: 0}
	c := &Client{Runner: run, Command: cmd}
	if _, err := c.PostStartup(context.Background(), roles.Role{Name: "r"}); err != nil {
		t.Fatalf("PostStartup: %v", err)
	}
	if gotSubcommand != "postStartup" {
		t.Errorf("subcommand passed to CommandFor = %q, want %q", gotSubcommand, "postStartup")
	}
}

func TestPreShutdown_passesSubcommandToCommandFor(t *testing.T) {
	var gotSubcommand string
	cmd := func(_ roles.Role, subcommand string) ([]string, error) {
		gotSubcommand = subcommand
		return []string{"h", subcommand}, nil
	}
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","id":"x"}`), exitCode: 0}
	c := &Client{Runner: run, Command: cmd}
	if _, err := c.PreShutdown(context.Background(), roles.Role{Name: "r"}); err != nil {
		t.Fatalf("PreShutdown: %v", err)
	}
	if gotSubcommand != "preShutdown" {
		t.Errorf("subcommand passed to CommandFor = %q, want %q", gotSubcommand, "preShutdown")
	}
}

// TestNew_defaultsRunnerToOSRunner locks New's documented default.
func TestNew_defaultsRunnerToOSRunner(t *testing.T) {
	c := New(fixedCommand("h"))
	if _, ok := c.Runner.(OSRunner); !ok {
		t.Fatalf("New must default Runner to OSRunner{}, got %T", c.Runner)
	}
}
