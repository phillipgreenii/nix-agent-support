package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/account"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

var updateSessionsGolden = flag.Bool("update-sessions-golden", false, "rewrite testdata/sessions.golden")

const (
	sessionsFixtureProjects = "testdata/sessions/projects"
	sessionsGoldenPath      = "testdata/sessions.golden"
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

func renderSessions(t *testing.T, since, before time.Time, maxChars int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := writeSessions(&buf, sessionsFixtureProjects, since, before, defaultPrices(t), maxChars); err != nil {
		t.Fatalf("writeSessions: %v", err)
	}
	return buf.Bytes()
}

type decodedDoc struct {
	Sessions []map[string]any `json:"sessions"`
}

func decodeSessions(t *testing.T, b []byte) decodedDoc {
	t.Helper()
	var d decodedDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("output is not the sessions document: %v\n%s", err, b)
	}
	return d
}

func sessionByID(t *testing.T, d decodedDoc, id string) map[string]any {
	t.Helper()
	for _, s := range d.Sessions {
		if s["session_id"] == id {
			return s
		}
	}
	t.Fatalf("no session %q in %v", id, d.Sessions)
	return nil
}

// TestSessionsCharacterization pins the record shape byte-for-byte against a
// golden file. The fixture tree has no sessions/ directory (no PID files, no
// daemon socket) and holds a resumed pair, a midnight-spanning session, an
// unknown-model session and a session with no user turn.
func TestSessionsCharacterization(t *testing.T) {
	got := renderSessions(t, time.Time{}, time.Time{}, 200)
	if *updateSessionsGolden {
		if err := os.WriteFile(sessionsGoldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(sessionsGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s (rerun with -update-sessions-golden after reviewing):\n--- got ---\n%s\n--- want ---\n%s", sessionsGoldenPath, got, want)
	}
}

func TestSessionsResumedPairIsOneRecord(t *testing.T) {
	d := decodeSessions(t, renderSessions(t, time.Time{}, time.Time{}, 200))
	n := 0
	for _, s := range d.Sessions {
		if s["cwd"] == "/work/alpha" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("resumed pair produced %d records, want 1: %v", n, d.Sessions)
	}
	s := sessionByID(t, d, "orig")
	if s["started_at"] != "2026-09-18T09:00:00Z" || s["ended_at"] != "2026-09-18T09:30:00Z" {
		t.Errorf("started/ended = %v / %v, want transcript event times", s["started_at"], s["ended_at"])
	}
}

func TestSessionsMidnightSpanIsOneRecordOnItsStartDay(t *testing.T) {
	// A window covering only the start day includes it; the day after does not.
	startDay := decodeSessions(t, renderSessions(t,
		time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), 200))
	if len(startDay.Sessions) != 1 || startDay.Sessions[0]["session_id"] != "midnight" {
		t.Fatalf("start-day window = %v, want exactly the midnight session", startDay.Sessions)
	}
	s := startDay.Sessions[0]
	if s["started_at"] != "2026-09-20T23:50:00Z" || s["ended_at"] != "2026-09-21T00:10:00Z" {
		t.Errorf("started/ended = %v / %v", s["started_at"], s["ended_at"])
	}
	nextDay := decodeSessions(t, renderSessions(t,
		time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), 200))
	if len(nextDay.Sessions) != 0 {
		t.Errorf("next-day window = %v, want none (reported once, on its start day)", nextDay.Sessions)
	}
}

func TestSessionsEmptyWindowIsEmptyArrayNotNull(t *testing.T) {
	out := renderSessions(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), time.Time{}, 200)
	if strings.TrimSpace(string(out)) != `{"sessions":[]}` {
		t.Errorf("empty window output = %s, want {\"sessions\":[]}", out)
	}
}

func TestSessionsCostPresentForKnownModelAbsentForUnknown(t *testing.T) {
	d := decodeSessions(t, renderSessions(t, time.Time{}, time.Time{}, 200))
	known := sessionByID(t, d, "midnight") // claude-opus-4-7: 0.2*5 + 0.04*25 = 2.0
	if got, ok := known["cost_usd"].(float64); !ok || got != 2.0 {
		t.Errorf("known-model cost_usd = %v, want 2.0", known["cost_usd"])
	}
	unknown := sessionByID(t, d, "unknown")
	if _, present := unknown["cost_usd"]; present {
		t.Errorf("unknown-model session has cost_usd = %v, want key omitted", unknown["cost_usd"])
	}
}

func TestSessionsNoUserTurnHasEmptyFirstPromptKey(t *testing.T) {
	s := sessionByID(t, decodeSessions(t, renderSessions(t, time.Time{}, time.Time{}, 200)), "noprompt")
	fp, present := s["first_prompt"]
	if !present || fp != "" {
		t.Errorf("first_prompt = %v (present=%v), want present and empty", fp, present)
	}
	if s["user_turns"] != float64(0) {
		t.Errorf("user_turns = %v, want 0", s["user_turns"])
	}
}

func TestSessionsRecordKeysAndNoDispatched(t *testing.T) {
	d := decodeSessions(t, renderSessions(t, time.Time{}, time.Time{}, 200))
	s := sessionByID(t, d, "midnight")
	for _, k := range []string{
		"session_id", "cwd", "branch", "model", "started_at", "ended_at",
		"user_turns", "assistant_turns", "first_prompt", "tokens", "cost_usd",
	} {
		if _, ok := s[k]; !ok {
			t.Errorf("record missing key %q: %v", k, s)
		}
	}
	if _, ok := s["dispatched"]; ok {
		t.Error("record must not carry a dispatched key in this phase")
	}
	tok, _ := s["tokens"].(map[string]any)
	for _, k := range []string{"input", "output", "cache_read", "cache_write"} {
		if _, ok := tok[k]; !ok {
			t.Errorf("tokens missing %q: %v", k, tok)
		}
	}
	// Ordered by started_at ascending.
	var prev string
	for _, r := range d.Sessions {
		cur := r["started_at"].(string)
		if cur < prev {
			t.Errorf("records not ordered by started_at: %q after %q", cur, prev)
		}
		prev = cur
	}
}

func TestSessionsFirstPromptTruncatedToConfiguredLength(t *testing.T) {
	d := decodeSessions(t, renderSessions(t, time.Time{}, time.Time{}, 10))
	fp := sessionByID(t, d, "orig")["first_prompt"].(string)
	if n := len([]rune(fp)); n != 10 {
		t.Errorf("first_prompt = %q (%d runes), want exactly 10", fp, n)
	}
	if !strings.HasSuffix(fp, "…") {
		t.Errorf("truncated first_prompt %q should end with the … marker", fp)
	}
	// Short prompts and a larger limit are untouched.
	full := decodeSessions(t, renderSessions(t, time.Time{}, time.Time{}, 200))
	if got := sessionByID(t, full, "orig")["first_prompt"]; got != "Please summarize the repository layout." {
		t.Errorf("untruncated first_prompt = %q", got)
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
	since, before, err := parseSessionsArgs([]string{"--since", "7d", "--before", "2026-09-24T00:00:00Z", "--json"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-7 * 24 * time.Hour); !since.Equal(want) {
		t.Errorf("since = %v, want %v", since, want)
	}
	if want := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC); !before.Equal(want) {
		t.Errorf("before = %v, want %v", before, want)
	}
	since, before, err = parseSessionsArgs(nil, now)
	if err != nil || !since.IsZero() || !before.IsZero() {
		t.Errorf("no args: got (%v, %v, %v), want open bounds", since, before, err)
	}
	for _, bad := range [][]string{
		{"--since", "not-a-time"}, {"--before", "nope"}, {"--since"}, {"--before"}, {"stray"},
	} {
		if _, _, err := parseSessionsArgs(bad, now); err == nil {
			t.Errorf("parseSessionsArgs(%v) = nil error, want an error", bad)
		}
	}
	if _, _, err := parseSessionsArgs([]string{"--since", "not-a-time"}, now); err == nil || !strings.Contains(err.Error(), "--since") {
		t.Errorf("error %v should name --since", err)
	}
}

// TestSessionsExitCodeAndHelp exercises the real binary: a bad bound exits 3
// naming --since on stderr, and --help lists the subcommand. HOME is a temp dir
// with no daemon socket, proving the daemon is not required.
func TestSessionsExitCodeAndHelp(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pa-monitor")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))

	cmd := exec.Command(bin, "sessions", "--since", "not-a-time")
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 3 {
		t.Fatalf("bad --since: err = %v, want exit 3", err)
	}
	if !strings.Contains(stderr.String(), "--since") {
		t.Errorf("stderr %q should name --since", stderr.String())
	}

	// Empty HOME: no projects dir, no daemon -> {"sessions":[]} and exit 0.
	cmd = exec.Command(bin, "sessions", "--since", "1d", "--json")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sessions on an empty home: %v", err)
	}
	if strings.TrimSpace(string(out)) != `{"sessions":[]}` {
		t.Errorf("empty home output = %s", out)
	}

	cmd = exec.Command(bin, "--help")
	cmd.Env = env
	help, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(help), "\n  sessions\n") {
		t.Errorf("--help does not list sessions:\n%s", help)
	}
}
