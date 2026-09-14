package query

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeParticipantRunner is a fake ParticipantRunner: it returns a canned
// stdout/exit code/error for every call, and records the last argv+stdin it
// was invoked with (for assertions on the request this package builds).
type fakeParticipantRunner struct {
	stdout   []byte
	exitCode int
	err      error

	gotArgv  []string
	gotStdin []byte
}

func (f *fakeParticipantRunner) Run(_ context.Context, argv []string, stdin []byte) ([]byte, int, error) {
	f.gotArgv = argv
	f.gotStdin = stdin
	return f.stdout, f.exitCode, f.err
}

func TestParticipantQuery_inlineEventsDecode(t *testing.T) {
	reply := `{"schemaVersion":"1","id":"qry-x","events":[` +
		`{"id":"e1","type":"work.ready","payload":{"id":"zr-1","type":"task","title":"T1","metadata":{"k":"v"}}},` +
		`{"id":"e2","type":"work.ready","payload":{"id":"zr-2","type":"task"}}` +
		`]}`
	runner := &fakeParticipantRunner{stdout: []byte(reply), exitCode: 0}
	q := ParticipantQuery{Meta: Meta{EmitTypes: []string{"work.ready"}}, Command: []string{"pg-router-ccpool-handler"}, Runner: runner}

	evts, err := q.Run(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(evts) != 2 {
		t.Fatalf("events = %+v, want 2", evts)
	}
	if evts[0].ID != "e1" || evts[0].Type != "work.ready" || evts[0].Item.ID != "zr-1" || evts[0].Item.Title != "T1" || evts[0].Item.Metadata["k"] != "v" {
		t.Fatalf("event[0] wrong: %+v", evts[0])
	}
	if evts[1].Item.ID != "zr-2" || evts[1].Item.Title != "" {
		t.Fatalf("event[1] wrong: %+v", evts[1])
	}

	// The request this client sent must be a schema-shaped source.query
	// message: schemaVersion/id/callback, "query" appended to Command.
	if len(runner.gotArgv) != 2 || runner.gotArgv[0] != "pg-router-ccpool-handler" || runner.gotArgv[1] != "query" {
		t.Fatalf("argv = %v, want [pg-router-ccpool-handler query]", runner.gotArgv)
	}
	var req map[string]any
	if err := json.Unmarshal(runner.gotStdin, &req); err != nil {
		t.Fatalf("request not JSON: %v", err)
	}
	if req["schemaVersion"] != "1" || req["id"] == "" || req["id"] == nil {
		t.Fatalf("request malformed: %+v", req)
	}
}

func TestParticipantQuery_deferredReplyOwesNothingNow(t *testing.T) {
	reply := `{"schemaVersion":"1","id":"qry-x","deferred":true}`
	runner := &fakeParticipantRunner{stdout: []byte(reply), exitCode: 0}
	q := ParticipantQuery{Command: []string{"src"}, Runner: runner}

	evts, err := q.Run(context.Background(), Env{})
	if err != nil {
		t.Fatalf("a deferred reply must not error: %v", err)
	}
	if len(evts) != 0 {
		t.Fatalf("a deferred reply must yield zero events THIS tick, got %+v", evts)
	}
}

func TestParticipantQuery_busyExitReportsErrBusy(t *testing.T) {
	runner := &fakeParticipantRunner{exitCode: 9}
	q := ParticipantQuery{Command: []string{"src"}, Runner: runner}

	if _, err := q.Run(context.Background(), Env{}); !errors.Is(err, ErrParticipantBusy) {
		t.Fatalf("exit 9 must report ErrParticipantBusy, got %v", err)
	}
}

func TestParticipantQuery_genericFailureExitPropagates(t *testing.T) {
	runner := &fakeParticipantRunner{exitCode: 1}
	q := ParticipantQuery{Command: []string{"src"}, Runner: runner}

	if _, err := q.Run(context.Background(), Env{}); err == nil {
		t.Fatal("a non-zero, non-busy exit must propagate as an error")
	}
}

func TestParticipantQuery_replyErrorFieldPropagates(t *testing.T) {
	reply := `{"schemaVersion":"1","id":"qry-x","error":"boom"}`
	runner := &fakeParticipantRunner{stdout: []byte(reply), exitCode: 0}
	q := ParticipantQuery{Command: []string{"src"}, Runner: runner}

	_, err := q.Run(context.Background(), Env{})
	if err == nil {
		t.Fatal("a reply carrying an error field must propagate it")
	}
}

func TestParticipantQuery_runnerErrorPropagates(t *testing.T) {
	runner := &fakeParticipantRunner{err: errors.New("exec failed")}
	q := ParticipantQuery{Command: []string{"src"}, Runner: runner}

	if _, err := q.Run(context.Background(), Env{}); err == nil {
		t.Fatal("a runner error (process could not even start) must propagate")
	}
}

func TestParticipantQuery_noCommandConfigured(t *testing.T) {
	q := ParticipantQuery{}
	if err := q.Validate(); err == nil {
		t.Fatal("Validate() must require Command")
	}
	if _, err := q.Run(context.Background(), Env{}); err == nil {
		t.Fatal("Run() with no Command must error rather than panic")
	}
}

func TestParticipantQuery_backingCommandIsArgv0(t *testing.T) {
	q := ParticipantQuery{Command: []string{"pg-router-ccpool-handler", "--flag"}}
	if got := q.BackingCommand(); got != "pg-router-ccpool-handler" {
		t.Fatalf("BackingCommand() = %q, want %q", got, "pg-router-ccpool-handler")
	}
	if got := (ParticipantQuery{}).BackingCommand(); got != "" {
		t.Fatalf("BackingCommand() with no Command = %q, want empty", got)
	}
}
