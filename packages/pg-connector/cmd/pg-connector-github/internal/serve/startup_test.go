package serve_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The startup order and the op table: the daemon is up, the ops of Phase 2 answer
// and every other op is unknown_op.

func TestServeStartsAndAnswersTheListOpFromTheOrigin(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed})
	d.Start(t)

	// The viewer was resolved before the gate opened, through gh with the
	// configured host.
	if got := d.Shim.CountKind("viewer"); got != 1 {
		t.Errorf("viewer calls at start = %d, want 1", got)
	}
	for _, c := range d.Shim.OriginCalls() {
		if c.Host != servetest.Host {
			t.Errorf("a gh call carried GH_HOST=%q, want %q: %v", c.Host, servetest.Host, c.Args)
		}
	}

	res := mustList(t, d, servetest.QueryMine, nil)
	want := []string{prA.Ref(), prB.Ref(), prC.Ref()}
	if !slices.Equal(res.PresentIDs, want) {
		t.Errorf("present_ids = %v, want %v", res.PresentIDs, want)
	}
	if len(res.Entities) != 3 || res.Entities[0].Title == "" {
		t.Errorf("entities = %+v", res.Entities)
	}
	// The daemon refreshes its configured queries on its own as well: after
	// the dust settles each query ran once and the summaries were fetched in
	// batches (never one call per PR).
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 20*time.Second)
	if got := d.Shim.CountKind("search"); got != 2 {
		t.Errorf("search calls = %d, want 2 (mine and team, once each)", got)
	}
	if got := d.Shim.CountKind("nodes"); got < 1 || got > 3 {
		t.Errorf("nodes calls = %d, want 1 to 3 batches for three PRs", got)
	}

	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Errorf("SIGTERM exit = %d, want 0; output: %s", code, d.Stderr())
	}
}

func TestServeRegistersOnlyThePhase2Ops(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{})

	registered := map[string][]string{
		"pr": {scriptout.OpAuthStatus, "explain", scriptout.OpList, "status"},
		"ci": {scriptout.OpAuthStatus, "explain", "status"},
	}
	for inst, ops := range registered {
		for _, op := range ops {
			resp := d.MustCall(t, inst, scriptout.Request{Op: op, Args: json.RawMessage(`{}`)}, 10*time.Second)
			if resp.Error != nil && resp.Error.Code == "unknown_op" {
				t.Errorf("%s %s must be registered, got unknown_op", inst, op)
			}
		}
	}
	// ci has no list op, and no instance has a later-phase op.
	for _, c := range []struct{ inst, op string }{
		{"ci", scriptout.OpList},
		{"pr", "show"},
		{"pr", "files"},
		{"pr", "commits"},
		{"pr", "changes"},
		{"pr", "changes_ack"},
		{"pr", "review_submit"},
		{"ci", "rerun_failed"},
		{"ci", "list_runs"},
	} {
		resp := d.MustCall(t, c.inst, scriptout.Request{Op: c.op, Args: json.RawMessage(`{}`)}, 10*time.Second)
		if resp.Error == nil || resp.Error.Code != "unknown_op" {
			t.Errorf("%s %s = %+v, want unknown_op", c.inst, c.op, resp.Error)
		}
	}

	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Errorf("exit = %d; output: %s", code, d.Stderr())
	}
}

func TestServeStartupLogsTheStartAndTheDrain(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{})
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d; output: %s", code, d.Stderr())
	}
	lines := d.DaemonLog(t)
	for _, change := range []string{"lock_acquired", "shell_started", "daemon_started", "drain_started", "drain_done"} {
		if !servetest.HasChange(lines, change) {
			t.Errorf("daemon.log.jsonl has no %s line", change)
		}
	}
	// A second start on the same state directory works: the drain released
	// the lock and removed the socket.
	d.Start(t)
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Errorf("second run exit = %d; output: %s", code, d.Stderr())
	}
}

func TestServeRefusesASecondInstance(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{})
	second := servetest.NewDaemon(t, servetest.Options{NoWait: true, Before: func(s *servetest.Daemon) {
		// Same state directory and socket as the running daemon.
		s.StateDir, s.Socket = d.StateDir, d.Socket
		s.Rewrite(map[string]any{"state_dir": d.StateDir, "socket_path": d.Socket})
	}})
	second.Start(t)
	code := second.Wait(t, 15*time.Second)
	if code != 5 {
		t.Errorf("second serve exit = %d, want 5; output: %s", code, second.Stderr())
	}
	if !strings.Contains(second.Stderr(), "daemon.lock") {
		t.Errorf("the refusal must name the lock: %q", second.Stderr())
	}
	// The first daemon is unharmed.
	if resp := d.MustCall(t, "pr", scriptout.Request{Op: scriptout.OpAuthStatus}, 5*time.Second); resp.Error != nil {
		t.Errorf("the first daemon stopped answering: %+v", resp.Error)
	}
}
