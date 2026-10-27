//go:build integration

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/servetest"
)

// The status mode, through the real binary against the real daemon child and
// against no daemon: the health report and its exit code (0 healthy, 2 degraded,
// 3 down), with counts only and never issue content.

// statusRun runs `status` of d's binary under an empty environment.
func statusRun(t *testing.T, d *servetest.Daemon, args ...string) servetest.ClientResult {
	t.Helper()
	return servetest.RunBinary(t, d.Bin(), "", []string{}, append([]string{"--config", d.ConfigPath, "status"}, args...)...)
}

func decodeStatus(t *testing.T, res servetest.ClientResult) status.Report {
	t.Helper()
	var r status.Report
	if err := json.Unmarshal([]byte(res.Stdout), &r); err != nil {
		t.Fatalf("stdout is not a status document: %v\nstdout: %s\nstderr: %s", err, res.Stdout, res.Stderr)
	}
	return r
}

func TestStatusModeAgainstALiveAndAStoppedDaemon(t *testing.T) {
	d := confDaemon(t)
	confAwaitBaseline(t, d, servetest.QueryMine)

	// Daemon up: the document parses, exit 0, every section of a daemon's report.
	res := statusRun(t, d, "--json")
	if res.Exit != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", res.Exit, res.Stdout, res.Stderr)
	}
	up := decodeStatus(t, res)
	if up.Health != status.Healthy || up.Source != status.SourceDaemon || up.UptimeSeconds == nil || up.Socket != d.Socket ||
		!up.Store.Present || up.Store.Identity == "" || up.Requests == nil || up.Queue == nil || up.Auth == nil ||
		up.Changes == nil || up.Tracked == nil || len(up.Queries) != 2 || up.Supervisor.Label != servetest.SupervisorLabel {
		t.Errorf("%+v", up)
	}
	if strings.Contains(res.Stdout, "Example issue") {
		t.Errorf("a status report carries counts, never issue content: %s", res.Stdout)
	}

	// The text form is the same report.
	text := statusRun(t, d)
	if text.Exit != 0 || !strings.Contains(text.Stdout, "health: healthy") || !strings.Contains(text.Stdout, "socket: "+d.Socket) {
		t.Errorf("exit %d\n%s", text.Exit, text.Stdout)
	}

	// Daemon stopped: the local report and exit 3.
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("daemon exit = %d", code)
	}
	res = statusRun(t, d, "--json")
	if res.Exit != 3 {
		t.Fatalf("exit %d, want 3 (down)\nstdout: %s\nstderr: %s", res.Exit, res.Stdout, res.Stderr)
	}
	down := decodeStatus(t, res)
	wantLogs := status.Logs{
		Events: filepath.Join(d.StateDir, "events.jsonl"), Daemon: filepath.Join(d.StateDir, "daemon.log.jsonl"), Client: filepath.Join(d.StateDir, "client.log.jsonl"),
	}
	if down.Health != status.Down || down.Source != status.SourceLocal || down.Socket != d.Socket || down.Logs != wantLogs ||
		down.Store.Path != d.StorePath() || !down.Store.Present || down.Store.SizeBytes == 0 ||
		down.Supervisor.Kind == "" || down.Supervisor.State == "" || down.Supervisor.Label != servetest.SupervisorLabel {
		t.Errorf("%+v", down)
	}
	if len(down.Reasons) == 0 || !strings.Contains(down.Reasons[0], d.Socket) {
		t.Errorf("the reason names the socket: %v", down.Reasons)
	}
	textDown := statusRun(t, d)
	if textDown.Exit != 3 || !strings.Contains(textDown.Stdout, "health: down") || !strings.Contains(textDown.Stdout, "socket: "+d.Socket) {
		t.Errorf("exit %d\n%s", textDown.Exit, textDown.Stdout)
	}
}

// TestStatusModeExitsTwoWhileTheOriginPausesTheDaemon: an origin that answers 429
// pauses the background work; the report is degraded, exit 2, and names the pause.
func TestStatusModeExitsTwoWhileTheOriginPausesTheDaemon(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{Hooks: "poll_interval=50ms", Config: map[string]any{"default_pause": "10m"}})
	d.Shim.Fail(429)
	// A read that has to ask the origin meets the 429 and starts the pause.
	resp := d.MustCall(t, servetest.Op("show", `{"id":"EXA-77"}`), 20*time.Second)
	if resp.Error == nil {
		t.Fatalf("show of an unstored issue answered %s while the origin refuses", resp.Result)
	}
	res := statusRun(t, d, "--json")
	if res.Exit != 2 {
		t.Fatalf("exit %d, want 2 (degraded)\n%s %s", res.Exit, res.Stdout, res.Stderr)
	}
	r := decodeStatus(t, res)
	if r.Health != status.Degraded || r.Pause == nil || !strings.Contains(r.Pause.Reason, "429") {
		t.Errorf("%+v", r)
	}
}

func TestStatusModeRejectsAnUnknownArgument(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{})
	res := statusRun(t, d, "--yaml")
	if res.Exit != 2 || !strings.Contains(res.Stderr, "--yaml") {
		t.Errorf("exit %d stderr %q", res.Exit, res.Stderr)
	}
}
