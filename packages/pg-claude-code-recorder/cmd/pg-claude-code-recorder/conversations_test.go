package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

const cbase = int64(1793375000000)

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

func run(t *testing.T, e *clienttest.Env, args ...string) (int, string, string) {
	t.Helper()
	a, out, errw := testApp(nil)
	code := a.run(append(args, "--socket", e.Socket))
	return code, out.String(), errw.String()
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

// fakeList serves a canned conversation.list answer.
func fakeList(t *testing.T, ans lifecycle.ConversationList) *clienttest.Env {
	t.Helper()
	sock := shortDir(t) + "/r.sock"
	s := server.New(obs.New(nil), nil, func() any { return map[string]any{} })
	s.Register("conversation.list", server.KindQuery, func(context.Context, []byte) (any, *wire.Error) { return ans, nil })
	if err := s.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &clienttest.Env{Socket: sock}
}

func TestWhileTheRecorderIsBehindTheCrashQueryExits5WithThePartialList(t *testing.T) {
	end := time.UnixMilli(cbase).UTC()
	row := lifecycle.Conversation{
		SessionID: "example-partial", State: "closed", Stage: "quiet", EndTime: &end,
		Evidence: lifecycle.Evidence{Ended: true, ExitObserved: true, RegistryPresentAtExit: true, Cwd: "/example/work"},
	}
	e := fakeList(t, lifecycle.ConversationList{Conversations: []lifecycle.Conversation{row}, Degraded: true})
	code, out, errs := run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z", "--json")
	if code != 5 || !strings.Contains(out, "example-partial") || !strings.Contains(errs, "behind") {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	if code, _, _ = run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z", "--allow-partial"); code != 0 {
		t.Fatalf("--allow-partial: exit %d", code)
	}
	// An empty crashed result exits 5 too while behind.
	e = fakeList(t, lifecycle.ConversationList{Degraded: true})
	if code, out, _ = run(t, e, "conversations", "--crashed", "--json"); code != 5 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty while behind: %d %q", code, out)
	}
}

func TestAnUnreadableCwdStillGivesAResumeLine(t *testing.T) {
	end := time.UnixMilli(cbase).UTC()
	row := lifecycle.Conversation{
		SessionID: "example-nocwd", State: "closed", Stage: "quiet", EndTime: &end,
		Evidence: lifecycle.Evidence{Ended: true, ExitObserved: true, RegistryPresentAtExit: true},
	}
	e := fakeList(t, lifecycle.ConversationList{Conversations: []lifecycle.Conversation{row}})
	code, out, _ := run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z")
	if code != 0 || !strings.Contains(out, "(cwd unknown)") || !strings.Contains(out, "claude --resume example-nocwd") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestStatusCarriesCrashedRecentAndSuspectedRecent(t *testing.T) {
	base := time.Now().Add(-time.Hour).UnixMilli()
	row := twoCrashes(base)
	row.Steps = append(row.Steps, clienttest.LabelStep{Do: "registry", Pid: 604, Session: "example-stale", Ms: base + 1000, StartMs: base})
	e := clienttest.RunRow(t, row)
	code, out, _ := run(t, e, "status", "--json")
	var st wire.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil || code != 0 {
		t.Fatalf("%d %v %s", code, err, out)
	}
	if st.CrashedRecent == nil || *st.CrashedRecent != 2 || st.SuspectedRecent == nil || *st.SuspectedRecent != 3 {
		t.Fatalf("crashed_recent %v suspected_recent %v", st.CrashedRecent, st.SuspectedRecent)
	}
	_, text, _ := run(t, e, "status")
	if !strings.Contains(text, "crashed (7d): 2") {
		t.Fatalf("status text %q", text)
	}
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

func TestParseSinceTakesADurationOrATime(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if got, err := parseSince("", now); err != nil || !got.Equal(now.Add(-7*24*time.Hour)) {
		t.Errorf("default %v %v", got, err)
	}
	if got, err := parseSince("36h", now); err != nil || !got.Equal(now.Add(-36*time.Hour)) {
		t.Errorf("36h %v %v", got, err)
	}
	if got, err := parseSince("2d", now); err != nil || !got.Equal(now.Add(-48*time.Hour)) {
		t.Errorf("2d %v %v", got, err)
	}
	if got, err := parseSince("2026-10-01T00:00:00Z", now); err != nil || got.Day() != 1 {
		t.Errorf("RFC 3339 %v %v", got, err)
	}
	if _, err := parseSince("soon", now); err == nil {
		t.Error("soon parsed")
	}
}
