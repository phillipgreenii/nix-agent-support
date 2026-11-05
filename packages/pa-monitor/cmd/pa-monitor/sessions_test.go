package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/account"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/pa-monitor/internal/recorderfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// defaultPrices is the built-in price table (no config file).
func defaultPrices(t *testing.T) usage.PriceTable {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return account.LoadAccount(cfg).PriceTable()
}

const cliTranscript = `{"type":"user","uuid":"u1","sessionId":"example-s1","timestamp":"2026-10-29T10:00:00.000Z","cwd":"/example/work","gitBranch":"main","isSidechain":false,"message":{"role":"user","content":"please look at the flaky example test"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"example-s1","timestamp":"2026-10-29T10:00:05.000Z","cwd":"/example/work","gitBranch":"main","isSidechain":false,"message":{"id":"msg_1","role":"assistant","model":"example-model-a","content":[{"type":"text","text":"the payments example is flaky because of a clock"}],"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
`

func cliFacts(t *testing.T) (*clienttest.Env, *recorderfacts.Facts) {
	t.Helper()
	e := clienttest.Start(t)
	e.Write("projects/"+e.Project+"/example-s1.jsonl", cliTranscript)
	e.Settle()
	return e, recorderfacts.New(client.Options{Socket: e.Socket})
}

// TestWriteSessionsFromTheRecorder is the move's fact read in the unit tier: the
// real recorder client against an in-process recorder, no started daemon.
func TestWriteSessionsFromTheRecorder(t *testing.T) {
	_, f := cliFacts(t)
	var buf bytes.Buffer
	if err := writeSessions(&buf, f, time.Time{}, time.Time{}, defaultPrices(t), 10, time.Time{}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil || len(doc.Sessions) != 1 {
		t.Fatalf("%v %s", err, buf.String())
	}
	r := doc.Sessions[0]
	if r["session_id"] != "example-s1" || r["cwd"] != "/example/work" || r["branch"] != "main" || r["model"] != "example-model-a" {
		t.Errorf("record = %v", r)
	}
	if r["first_prompt"] != "please lo…" {
		t.Errorf("first_prompt = %q, want it truncated to 10 runes", r["first_prompt"])
	}
	if _, bad := r["dispatched"]; bad {
		t.Error("there must be no dispatched key")
	}
}

func TestWriteSessionsEmptyWindowIsEmptyArrayNotNull(t *testing.T) {
	_, f := cliFacts(t)
	var buf bytes.Buffer
	if err := writeSessions(&buf, f, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), time.Time{}, defaultPrices(t), 10, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != `{"sessions":[]}` {
		t.Errorf("output = %s", buf.String())
	}
}

func TestSearchSessionsFromTheRecorder(t *testing.T) {
	_, f := cliFacts(t)
	var buf bytes.Buffer
	if err := searchSessions(&buf, f, "flaky", "", time.Time{}, time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	var doc searchJSONDoc
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil || len(doc.Matches) == 0 {
		t.Fatalf("%v %s", err, buf.String())
	}
	if doc.Query != "flaky" || doc.Matches[0].SessionID != "example-s1" {
		t.Errorf("doc = %+v", doc)
	}
}

// TestSessionsAndSearchSayDaemonOffline: the recorder is stopped and the
// transcript file is still on disk; neither command reads it.
func TestSessionsAndSearchSayDaemonOffline(t *testing.T) {
	e, f := cliFacts(t)
	e.Stop()
	var buf bytes.Buffer
	if err := writeSessions(&buf, f, time.Time{}, time.Time{}, defaultPrices(t), 10, time.Time{}); err != session.ErrDaemonOffline {
		t.Errorf("sessions err = %v", err)
	}
	if err := searchSessions(&buf, f, "flaky", "", time.Time{}, time.Time{}, false); err != session.ErrDaemonOffline {
		t.Errorf("search err = %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("output written while offline: %s", buf.String())
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"héllo wörld", 6, "héllo…"},
		{"hello", 1, "…"},
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := truncateRunes(c.in, c.max); got != c.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestParseSessionsArgs(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	since, before, stats, err := parseSessionsArgs([]string{"--since", "7d", "--before", "2026-09-24T00:00:00Z", "--json"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-7 * 24 * time.Hour); !since.Equal(want) {
		t.Errorf("since = %v, want %v", since, want)
	}
	if want := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC); !before.Equal(want) {
		t.Errorf("before = %v, want %v", before, want)
	}
	if stats {
		t.Error("stats = true without --stats")
	}
	since, before, stats, err = parseSessionsArgs(nil, now)
	if err != nil || !since.IsZero() || !before.IsZero() || stats {
		t.Errorf("no args: got (%v, %v, %v, %v), want open bounds and no stats", since, before, stats, err)
	}
	if _, _, stats, err := parseSessionsArgs([]string{"--stats", "--since", "1d"}, now); err != nil || !stats {
		t.Errorf("--stats: got (%v, %v), want stats", stats, err)
	}
	for _, bad := range [][]string{
		{"--since", "not-a-time"}, {"--before", "nope"}, {"--since"}, {"--before"}, {"stray"},
	} {
		if _, _, _, err := parseSessionsArgs(bad, now); err == nil {
			t.Errorf("parseSessionsArgs(%v) = nil error, want an error", bad)
		}
	}
	if _, _, _, err := parseSessionsArgs([]string{"--since", "not-a-time"}, now); err == nil || !strings.Contains(err.Error(), "--since") {
		t.Errorf("error %v should name --since", err)
	}
}

// TestSessionsExitCodeAndHelp exercises the real binary: a bad bound exits 2
// naming --since on stderr, and with no recorder answering `sessions` says
// "daemon offline" and exits 1 instead of reading the transcript files.
func TestSessionsExitCodeAndHelp(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pa-monitor")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(home, ".state"), "PG_CLAUDE_CODE_RECORDER_SOCKET="+filepath.Join(home, "absent.sock"))

	cmd := exec.Command(bin, "sessions", "--since", "not-a-time")
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != exitUsage {
		t.Fatalf("bad --since: err = %v, want exit 2", err)
	}
	if !strings.Contains(stderr.String(), "--since") {
		t.Errorf("stderr %q should name --since", stderr.String())
	}

	cmd = exec.Command(bin, "sessions", "--since", "1d", "--json")
	cmd.Env = env
	stderr.Reset()
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	ee, ok = err.(*exec.ExitError)
	if !ok || ee.ExitCode() != exitGeneric || len(out) != 0 {
		t.Fatalf("no recorder: err = %v out = %q, want exit 1 and no output", err, out)
	}
	if !strings.Contains(stderr.String(), "daemon offline") {
		t.Errorf("stderr %q should say daemon offline", stderr.String())
	}
}

// TestSearchSubstringMatchesMidWordOnlyWhenAsked: the default is word search
// (a fragment of a word finds nothing); --substring is answered by the
// recorder scanning its copies of the tool-result bodies.
func TestSearchSubstringMatchesMidWordOnlyWhenAsked(t *testing.T) {
	e, f := cliFacts(t)
	e.Write("projects/"+e.Project+"/example-s2.jsonl", `{"type":"user","uuid":"t1","sessionId":"example-s2","timestamp":"2026-10-29T11:00:00.000Z","cwd":"/example/work","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x1","content":"build output: examplelibrary-v2 linked"}]}}
`)
	e.Settle()
	count := func(substring bool) int {
		var buf bytes.Buffer
		if err := searchSessions(&buf, f, "library-v2", "", time.Time{}, time.Time{}, substring); err != nil {
			t.Fatal(err)
		}
		var doc searchJSONDoc
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return len(doc.Matches)
	}
	if n := count(true); n != 1 {
		t.Errorf("substring search found %d hits for a mid-word fragment, want 1", n)
	}
	var buf bytes.Buffer
	if err := searchSessions(&buf, f, "lak", "", time.Time{}, time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "example-s1") {
		t.Errorf("word search matched a mid-word fragment: %s", buf.String())
	}
}

func TestSessionsWindowDefaultsToSevenDaysUnlessBoundedOrAll(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	wantStart := now.Add(-7 * 24 * time.Hour)
	since, before, def := sessionsWindow(time.Time{}, time.Time{}, false, now)
	if !since.Equal(wantStart) || !before.IsZero() || !def.Equal(wantStart) {
		t.Errorf("no window: got (%v, %v, %v), want since=default=%v", since, before, def, wantStart)
	}
	given := now.Add(-time.Hour)
	for name, c := range map[string]struct {
		since, before time.Time
		all           bool
	}{
		"since":  {given, time.Time{}, false},
		"before": {time.Time{}, given, false},
		"all":    {time.Time{}, time.Time{}, true},
	} {
		s, b, d := sessionsWindow(c.since, c.before, c.all, now)
		if !s.Equal(c.since) || !b.Equal(c.before) || !d.IsZero() {
			t.Errorf("%s: got (%v, %v, %v), want the bounds unchanged and no default", name, s, b, d)
		}
	}
	if _, _, _, err := parseSessionsArgs([]string{"--all"}, now); err != nil {
		t.Errorf("--all: %v", err)
	}
}

func TestWriteSessionsMarksADefaultedWindow(t *testing.T) {
	_, f := cliFacts(t)
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		def  time.Time
		want string
	}{{start, "2020-01-01T00:00:00Z"}, {time.Time{}, ""}} {
		var buf bytes.Buffer
		if err := writeSessions(&buf, f, start, time.Time{}, defaultPrices(t), 10, c.def); err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		got, _ := doc["defaulted_since"].(string)
		if got != c.want {
			t.Errorf("defaulted_since = %q, want %q (%s)", got, c.want, buf.String())
		}
	}
}
