package serve_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
)

// Identity and privacy for the Phase 3a groups and ops: the fixture token the
// shim hands out (and the only one it accepts) never reaches the store (job rows
// and every group's content included), the event log or the structured log,
// whatever the workload touched.

func TestPrivacy_AFixtureTokenNeverAppearsInAnyGroupAJobRowOrALogWhateverTheNewOpsDid(t *testing.T) {
	const token = "tok-fixture-3a77c1e90b5d42f8a61e"
	stateHome := t.TempDir()
	opts := writeOpts(stateHome)
	opts.Token = token
	opts.Hooks = "poll_interval=50ms,viewer_interval=300ms"
	// list_activity spends ten search requests in one call; the default budget of
	// twenty a minute refuses it (bead pg2-rgbco), so the test raises it.
	opts.Config = map[string]any{"rest_search_per_minute": 100}
	d := servetest.NewDaemon(t, opts)
	d.Start(t)
	settle(t, d)

	// Every new group and op: the reads, the write, the rerun, the pass-through
	// ops and the diagnostics.
	d.Shim.Set("search_prs.json", `[]`)
	callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()})
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()})
	callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()})
	callOK(t, d, "pr", "review_pending", map[string]any{"id": prA.Ref()})
	callOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref()})
	callOK(t, d, "pr", "review_submit", submitArgs())
	callOK(t, d, "ci", "rerun_failed", map[string]any{"pr_id": prA.Ref()})
	callOK(t, d, "pr", "search", map[string]any{"query": "is:open widget"})
	callOK(t, d, "pr", "list_activity", map[string]any{"before": "2026-10-13T00:00:00Z"})
	callOK(t, d, "ci", "get_logs", map[string]any{"run_id": "3001", "repo": prA.Repo})
	callOK(t, d, "pr", "refresh", map[string]any{"id": prA.Ref()})
	callOK(t, d, "ci", "refresh", map[string]any{"pr_id": prA.Ref()})
	callOK(t, d, "pr", "explain", map[string]any{"id": prA.Ref()})
	callOK(t, d, "ci", "explain", map[string]any{"id": prA.Ref()})
	// A viewer change, which flushes the identity-scoped data.
	d.Shim.Viewer("another-viewer")
	until(t, 20*time.Second, "the viewer_changed log line", func() bool { return servetest.HasChange(d.DaemonLog(t), "viewer_changed") })
	// Each kind of origin failure, as the new fetchers meet it (the secondary
	// limit last: its pause outlasts the test).
	for _, mode := range []string{"502", "401", "primary", "secondary", "ok"} {
		d.Shim.Mode(mode)
		callErrOrOK(t, d, "pr", "show", map[string]any{"id": prA.Ref(), "fresh": true})
		callErrOrOK(t, d, "pr", "files", map[string]any{"id": prA.Ref(), "fresh": true})
		callErrOrOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref(), "fresh": true})
		time.Sleep(700 * time.Millisecond) // a viewer refresh or two under this mode
	}
	d.Shim.Mode("ok")

	if d.Shim.CountKind("auth_token") == 0 || d.Shim.CountKind("jobs") == 0 {
		t.Fatal("fixture: the token was never requested or no job was fetched, so this test proves less than it claims")
	}
	var jobs int
	if err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM job`).Scan(&jobs); err != nil || jobs == 0 {
		t.Fatalf("fixture: no job row stored (%v)", err)
	}
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d; output: %s", code, d.Stderr())
	}

	// Everything the daemon wrote: the store with its WAL, the event log, the
	// structured log and their rotations, the sidecar, and what it printed.
	for _, dir := range []string{d.StateDir, stateHome} {
		if hits := filesWith(t, dir, []byte(token)); len(hits) != 0 {
			t.Errorf("the token appears in %v", hits)
		}
	}
	if strings.Contains(d.Stderr(), token) {
		t.Errorf("the token appears in the daemon's output: %s", d.Stderr())
	}
	for _, f := range []string{d.StorePath(), filepath.Join(d.StateDir, "events.jsonl"), filepath.Join(d.StateDir, "daemon.log.jsonl")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("fixture: %s was not written: %v", f, err)
		}
	}
	// The scan can find a token: plant one in the store directory and see it found.
	planted := filepath.Join(d.StateDir, "planted.txt")
	if err := os.WriteFile(planted, []byte("x"+token+"y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hits := filesWith(t, d.StateDir, []byte(token)); len(hits) != 1 || hits[0] != planted {
		t.Errorf("control: the scan must find the planted token, got %v", hits)
	}
}

// callErrOrOK makes a call whose answer does not matter to the test.
func callErrOrOK(t *testing.T, d *servetest.Daemon, instance, op string, args map[string]any) {
	t.Helper()
	d.MustCall(t, instance, req(op, args), 20*time.Second)
}
