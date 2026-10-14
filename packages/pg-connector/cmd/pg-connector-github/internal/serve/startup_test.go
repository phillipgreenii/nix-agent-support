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

// The startup order and the op table: the daemon is up, the ops of Phases 2 and
// 3a answer and every other op is unknown_op.

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

func TestServeRegistersExactlyThePhase3aOps(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{})

	registered := map[string][]string{
		"pr": {
			scriptout.OpAuthStatus, "explain", scriptout.OpList, "status",
			"show", "files", "commits", "review_pending", "review_submit", "search", scriptout.OpListActivity, "refresh",
		},
		"ci": {scriptout.OpAuthStatus, "explain", "status", "list_runs", "get_logs", "rerun_failed", "refresh"},
	}
	for inst, ops := range registered {
		for _, op := range ops {
			resp := d.MustCall(t, inst, scriptout.Request{Op: op, Args: json.RawMessage(`{}`)}, 10*time.Second)
			if resp.Error != nil && resp.Error.Code == "unknown_op" {
				t.Errorf("%s %s must be registered, got unknown_op", inst, op)
			}
		}
	}
	// ci has no list op, and the change feed (Phase 3b) is not here yet.
	for _, c := range []struct{ inst, op string }{
		{"ci", scriptout.OpList},
		{"pr", "changes"},
		{"pr", "changes_ack"},
		{"ci", "changes"},
		{"ci", "changes_ack"},
		{"pr", "list_runs"},
		{"ci", "show"},
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

// indexOf is the position of the first line for change, or -1.
func indexOf(lines []map[string]any, change string) int {
	for i, l := range lines {
		if l["change"] == change {
			return i
		}
	}
	return -1
}

func TestServeStartupOrderIsObservableInTheLog(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed})
	d.Start(t)

	// First start on an empty state directory: the shell (lock, socket) comes
	// first, then the store, then the gate opens.
	lines := d.DaemonLog(t)
	shell, migrated, started := indexOf(lines, "shell_started"), indexOf(lines, "store_migrated"), indexOf(lines, "daemon_started")
	if !(0 <= shell && shell < migrated && migrated < started) {
		t.Errorf("order: shell_started %d, store_migrated %d, daemon_started %d", shell, migrated, started)
	}
	// The viewer was resolved before the gate opened: by the time the daemon
	// answers, the lookup is on the shim's log.
	if d.Shim.CountKind("viewer") != 1 {
		t.Errorf("the viewer must be resolved during startup, viewer calls = %d", d.Shim.CountKind("viewer"))
	}

	// A crash leaves a socket behind; the next start removes it under the lock,
	// before it listens, and opens the store without migrating again.
	d.Kill(t)
	d.Start(t)
	lines = d.DaemonLog(t)
	var after []map[string]any
	for i := started + 1; i < len(lines); i++ {
		after = append(after, lines[i])
	}
	stale, shell2, started2 := indexOf(after, "stale_socket_removed"), indexOf(after, "shell_started"), indexOf(after, "daemon_started")
	if !(0 <= stale && stale < shell2 && shell2 < started2) {
		t.Errorf("restart order: stale_socket_removed %d, shell_started %d, daemon_started %d", stale, shell2, started2)
	}
	if indexOf(after, "store_migrated") >= 0 {
		t.Error("a store at the current schema must not be migrated again")
	}
}
