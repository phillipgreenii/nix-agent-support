//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func TestClientNameReachesTheDaemonLog(t *testing.T) {
	sock, logbuf, _ := liveDaemon(t)
	// A successful request is logged at debug (counted in requests_total), so
	// the client name reaches the info-level log through a failed request.
	a, _, _ := testApp(nil)
	if code := a.run([]string{"processes", "--socket", sock, "--pid", "9999", "--client", "example-check"}); code != 4 {
		t.Fatalf("unknown pid: exit %d", code)
	}
	if !strings.Contains(logbuf.String(), `"client":"example-check"`) {
		t.Fatalf("log lacks the client name:\n%s", logbuf.String())
	}
}

func TestStatusAndCoverageAgainstALiveDaemon(t *testing.T) {
	sock, _, d := liveDaemon(t)
	deadline := time.Now().Add(10 * time.Second)
	for d.Watcher().LastSweep().IsZero() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	a, out, _ := testApp(nil)
	a.pollEvery = 5 * time.Millisecond
	if code := a.run([]string{"coverage", "--socket", sock, "--wait-caught-up", "--view", "files", "--client", "example-check"}); code != 0 {
		t.Fatalf("coverage exit %d\n%s", code, out)
	}
	if !strings.Contains(out.String(), "caught up: true") || !strings.Contains(out.String(), "copied=8") || !strings.Contains(out.String(), "root:") {
		t.Fatalf("coverage:\n%s", out)
	}
	a, out, _ = testApp(nil)
	if code := a.run([]string{"status", "--socket", sock}); code != 0 || !strings.HasPrefix(out.String(), "healthy:") {
		t.Fatalf("status %d\n%s", code, out)
	}
	a, out, _ = testApp(nil)
	if code := a.run([]string{"status", "--short", "--socket", sock}); code != 0 || strings.TrimSpace(out.String()) != "ok" {
		t.Fatalf("short %d %q", code, out)
	}
	a, out, _ = testApp(nil)
	if code := a.run([]string{"status", "--json", "--socket", sock}); code != 0 {
		t.Fatal(code)
	}
	var raw map[string]any
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil || raw["healthy"] != true || raw["protocol_version"] != float64(1) {
		t.Fatalf("%v %v", raw, err)
	}
}

func liveDaemon(t *testing.T) (sock string, logbuf *serve.SyncBuffer, d *serve.Daemon) {
	t.Helper()
	root := shortDir(t)
	claude := filepath.Join(root, "claude")
	_ = os.MkdirAll(filepath.Join(claude, "projects", "example-proj"), 0o755)
	tp := filepath.Join(claude, "projects", "example-proj", "a.jsonl")
	_ = os.WriteFile(tp, []byte("{\"n\":1}\n"), 0o600)
	cfg := config.Defaults(func(string) string { return "" })
	cfg.ClaudeDir, cfg.StateDir, cfg.Socket = claude, filepath.Join(root, "state"), filepath.Join(root, "r.sock")
	// The default floor is a production value (20 GiB); a test must not depend on the host's free space.
	cfg.MinFreeBytes = 1
	logbuf = &serve.SyncBuffer{}
	var err error
	d, err = serve.Open(serve.Options{Config: cfg, Logger: serve.NewLogger(logbuf), NoEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return cfg.Socket, logbuf, d
}

func TestProcessesListsShowsAndFilters(t *testing.T) {
	e := clienttest.RunRow(t, twoCrashes(cbase))
	code, out, _ := run(t, e, "processes", "--live", "--json")
	var ps []lifecycle.Process
	if err := json.Unmarshal([]byte(out), &ps); err != nil || code != 0 || len(ps) != 1 || ps[0].Pid != 603 || !ps[0].Alive || ps[0].Host == nil {
		t.Fatalf("%d %v %s", code, err, out)
	}
	_, text, _ := run(t, e, "processes")
	if !strings.Contains(text, "CONTROL_INBOX") || !strings.Contains(text, "603") || !strings.Contains(text, "601") {
		t.Fatalf("text %q", text)
	}
	code, out, _ = run(t, e, "processes", "--pid", "601")
	var p lifecycle.Process
	if err := json.Unmarshal([]byte(out), &p); err != nil || code != 0 || p.Pid != 601 || len(p.Versions) != 1 || p.Alive {
		t.Fatalf("%d %v %s", code, err, out)
	}
	if code, _, _ = run(t, e, "processes", "--pid", "9999"); code != 4 {
		t.Fatalf("unknown pid: exit %d", code)
	}
	if code, out, _ = run(t, e, "processes", "--session", "example-c2", "--json"); code != 0 || !strings.Contains(out, `"pid":602`) {
		t.Fatalf("--session: %d %q", code, out)
	}
}

func TestStatusCarriesCrashedRecentAndSuspectedRecent(t *testing.T) {
	base := time.Now().Add(-time.Hour).UnixMilli()
	row := twoCrashes(base)
	row.Steps = append(row.Steps, clienttest.LabelStep{Do: "registry", Pid: 604, Session: "example-stale", Ms: base + 1000, StartMs: base})
	// A crash and a clean close, the crash ten days ago (outside the 7-day window)
	// and the clean close inside it: neither may count.
	old := base - 10*24*int64(time.Hour/time.Millisecond)
	row.Steps = append(
		row.Steps,
		clienttest.LabelStep{Do: "start", Pid: 605, StartMs: old},
		clienttest.LabelStep{Do: "registry", Pid: 605, Session: "example-old-crash", Ms: old + 1000, StartMs: old},
		clienttest.LabelStep{Do: "exit_kept", Pid: 605, Ms: old + 60000},
		clienttest.LabelStep{Do: "start", Pid: 606, StartMs: base},
		clienttest.LabelStep{Do: "registry", Pid: 606, Session: "example-clean", Ms: base + 1000, StartMs: base},
		clienttest.LabelStep{Do: "exit_clean", Pid: 606, Ms: base + 30000},
	)
	e := clienttest.RunRow(t, row)
	// The status loop refreshes every 5 s; take the figures now.
	if err := e.Daemon.RefreshStatus(); err != nil {
		t.Fatal(err)
	}
	a, o, _ := testApp(nil)
	code := a.run([]string{"status", "--json", "--socket", e.Socket})
	out := o.String()
	var st wire.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil || code != 0 {
		t.Fatalf("%d %v %s", code, err, out)
	}
	if st.CrashedRecent == nil || *st.CrashedRecent != 2 || st.SuspectedRecent == nil || *st.SuspectedRecent != 3 {
		t.Fatalf("crashed_recent %s suspected_recent %s", ptr(st.CrashedRecent), ptr(st.SuspectedRecent))
	}
	// The counts agree with the authoritative lists.
	for flag, want := range map[string]int{"--crashed": *st.CrashedRecent, "--suspected-crash": *st.SuspectedRecent} {
		a, o, _ = testApp(nil)
		if code := a.run([]string{"conversations", flag, "--since", "7d", "--json", "--socket", e.Socket}); code != 0 {
			t.Fatalf("conversations %s: exit %d", flag, code)
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(o.Bytes(), &rows); err != nil || len(rows) != want {
			t.Fatalf("conversations %s lists %d rows (%v), status counted %d", flag, len(rows), err, want)
		}
	}
	a, o, _ = testApp(nil)
	a.run([]string{"status", "--socket", e.Socket})
	text := o.String()
	if !strings.Contains(text, "crashed (7d): 2") {
		t.Fatalf("status text %q", text)
	}
}

func TestAnEmptyCrashedResultExits0WithTheHintAndEmptyJSON(t *testing.T) {
	// One Conversation is unknown with a stale registry file (a reboot).
	e := clienttest.RunRow(t, clienttest.LabelRow{Name: "stale", Steps: []clienttest.LabelStep{
		{Do: "registry", Pid: 611, Session: "example-stale", Ms: cbase + 1000, StartMs: cbase},
	}})
	code, out, _ := run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z")
	if code != 0 || strings.TrimSpace(out) != "0 crashed; 1 unknown with a stale registry file (run --suspected-crash)" {
		t.Fatalf("%d %q", code, out)
	}
	code, out, _ = run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z", "--json")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("%d %q", code, out)
	}
	code, out, _ = run(t, e, "conversations", "--suspected-crash", "--since", "2000-01-01T00:00:00Z", "--json")
	var rows []lifecycle.Ended
	if err := json.Unmarshal([]byte(out), &rows); err != nil || code != 0 || len(rows) != 1 || rows[0].Label != lifecycle.LabelUnknown || !rows[0].RegistryStaleAtDiscovery {
		t.Fatalf("%d %v %s", code, err, out)
	}
	if !rows[0].EndTimeApprox || rows[0].EndTime == nil {
		t.Fatalf("a suspected row's end time is approximate: %+v", rows[0])
	}
	// No hint when no row has the stale fact.
	e2 := clienttest.RunRow(t, clienttest.LabelRow{Name: "none"})
	if _, out, _ = run(t, e2, "conversations", "--crashed"); strings.TrimSpace(out) != "0 crashed" {
		t.Fatalf("%q", out)
	}
}

func TestConversationsUsageErrorsExit2(t *testing.T) {
	e := clienttest.RunRow(t, twoCrashes(cbase))
	for _, args := range [][]string{
		{"--ended", "crashed", "--crashed"},
		{"--ended", "crashed", "--suspected-crash"},
		{"--crashed", "--suspected-crash"},
		{"--ended", "bogus"},
		{"--since", "nonsense"},
		{"--limit", "-1"},
		{"extra"},
	} {
		if code, _, errs := run(t, e, append([]string{"conversations"}, args...)...); code != 2 || !strings.HasPrefix(errs, "error: usage") {
			t.Errorf("%v: exit %d, stderr %q", args, code, errs)
		}
	}
	// closed and closed normally are the same label.
	code, out, _ := run(t, e, "conversations", "--ended", "closed normally", "--json", "--since", "2000-01-01T00:00:00Z")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("closed normally: %d %q", code, out)
	}
}

func TestConversationsSinceCwdAndLimit(t *testing.T) {
	e := clienttest.RunRow(t, twoCrashes(cbase))
	list := func(args ...string) []string {
		t.Helper()
		code, out, errs := run(t, e, append([]string{"conversations", "--crashed", "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errs)
		}
		var rows []lifecycle.Ended
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.SessionID)
		}
		return ids
	}
	at := func(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }
	if got := list("--since", at(cbase+90000)); strings.Join(got, ",") != "example-c2" {
		t.Errorf("--since between the two ends: %v", got)
	}
	if got := list("--since", at(cbase+1000)); len(got) != 2 {
		t.Errorf("--since before both ends: %v", got)
	}
	if got := list("--since", at(cbase+999999)); len(got) != 0 {
		t.Errorf("--since after both ends: %v", got)
	}
	if got := list("--since", "2000-01-01T00:00:00Z", "--cwd", "/example/work"); strings.Join(got, ",") != "example-c1" {
		t.Errorf("--cwd matches the path and its subpaths: %v", got)
	}
	if got := list("--since", "2000-01-01T00:00:00Z", "--cwd", "/example/wor"); len(got) != 0 {
		t.Errorf("--cwd must match a whole path component: %v", got)
	}
	if got := list("--since", "2000-01-01T00:00:00Z", "--limit", "1"); strings.Join(got, ",") != "example-c2" {
		t.Errorf("--limit 1 keeps the newest: %v", got)
	}
}

func TestConversationsRowCarriesEveryFieldAndJSONMatchesTheText(t *testing.T) {
	e := clienttest.RunRow(t, twoCrashes(cbase))
	e.Write("projects/example-proj/example-c1.jsonl",
		`{"type":"user","uuid":"a1","sessionId":"example-c1","timestamp":"2026-10-30T10:00:00Z","cwd":"/example/work/sub","message":{"role":"user"}}`+"\n")
	e.Settle()
	code, out, _ := run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("%v: %s", err, out)
	}
	newest, older := rows[0], rows[1]
	if newest["session_id"] != "example-c2" || older["session_id"] != "example-c1" {
		t.Fatalf("not newest first: %v %v", newest["session_id"], older["session_id"])
	}
	for _, k := range []string{
		"session_id", "cwd", "end_time", "label", "stage", "originals_exist", "resume", "registry_present_at_exit",
		"exit_observed", "clear_entry", "exit_entry", "registry_stale_at_discovery", "exit_time", "last_seen_alive",
	} {
		if _, ok := older[k]; !ok {
			t.Errorf("a row lacks %q: %v", k, older)
		}
	}
	if older["label"] != "crashed" || older["stage"] != "quiet" || older["originals_exist"] != true || older["end_time_approx"] != nil {
		t.Fatalf("row %v", older)
	}
	if older["resume"] != "cd /example/work/sub && claude --resume example-c1" {
		t.Fatalf("resume with originals %v", older["resume"])
	}
	if newest["originals_exist"] != false || newest["resume"] != "pg-claude-code-recorder extract example-c2 && cd /example/other && claude --resume example-c2" {
		t.Fatalf("resume without originals %v", newest["resume"])
	}
	_, text, _ := run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z")
	for _, r := range rows {
		for _, want := range []string{r["session_id"].(string), "crashed", r["cwd"].(string), "resume: " + r["resume"].(string), "originals"} {
			if !strings.Contains(text, want) {
				t.Errorf("the text output lacks %q:\n%s", want, text)
			}
		}
	}
}

func twoCrashes(base int64) clienttest.LabelRow {
	return clienttest.LabelRow{Name: "cmd", Steps: []clienttest.LabelStep{
		{Do: "start", Pid: 601, StartMs: base},
		{Do: "registry", Pid: 601, Session: "example-c1", Ms: base + 1000, StartMs: base, Cwd: "/example/work/sub"},
		{Do: "exit_kept", Pid: 601, Ms: base + 60000},
		{Do: "start", Pid: 602, StartMs: base},
		{Do: "registry", Pid: 602, Session: "example-c2", Ms: base + 1000, StartMs: base, Cwd: "/example/other"},
		{Do: "exit_kept", Pid: 602, Ms: base + 120000},
		{Do: "start", Pid: 603, StartMs: base},
		{Do: "registry", Pid: 603, Session: "example-live", Ms: base + 1000, StartMs: base},
	}}
}

func ptr(p *int) string {
	if p == nil {
		return "nil"
	}
	return strconv.Itoa(*p)
}
