package poller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// eqPrices is the shared price table used by BOTH equivalence arms (the inline
// NativePricer and the Monitor's UsagePricing observer), so a non-nil block is
// priced identically on both sides.
var eqPrices = usage.PriceTable{Default: usage.ModelPrices{InputPerMTok: 5, OutputPerMTok: 25}}

// --- equivalence-test corpus builders (local to package poller) ---

func eqSlug(cwd string) string { return strings.NewReplacer("/", "-", "_", "-").Replace(cwd) }

func eqUserPrompt(text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":%q}]}}`, text)
}

func eqAssistant(model string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[]}}`, model, in, out)
}

func eqTitle(title string) string {
	return `{"type":"custom-title","customTitle":"` + title + `"}`
}

func eqAPIError(kind, text, ts string) string {
	return `{"type":"assistant","isApiErrorMessage":true,"error":"` + kind + `","timestamp":"` + ts +
		`","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
}

func eqSession(t *testing.T, sessionsDir string, pid int, sessID, name, cwd string) {
	t.Helper()
	nameField := ""
	if name != "" {
		nameField = fmt.Sprintf(`,"name":%q`, name)
	}
	js := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":1776000000000,"kind":"interactive","entrypoint":"cli"%s}`,
		pid, sessID, cwd, nameField)
	if err := os.WriteFile(filepath.Join(sessionsDir, fmt.Sprintf("%d.json", pid)), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
}

func eqTranscript(t *testing.T, home, cwd, filename string, mtime time.Time, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "projects", eqSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func eqSubagent(t *testing.T, mainPath, agentName string, mtime time.Time, lines ...string) {
	t.Helper()
	sub := strings.TrimSuffix(mainPath, ".jsonl") + "/subagents"
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, agentName)
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// buildEquivalenceCorpus writes a shared ~/.claude covering the cases where the
// inline and Monitor-backed paths MUST agree (titles within the old 200-line
// cap), and returns the dirs + the PidAlive hook.
func buildEquivalenceCorpus(t *testing.T) (sessionsDir, home string, pidAlive func(int) bool) {
	t.Helper()
	root := t.TempDir()
	sessionsDir = filepath.Join(root, "sessions")
	home = filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_776_000_000, 0)
	t1 := time.Unix(1_776_000_100, 0)
	t2 := time.Unix(1_776_000_200, 0)

	// 1) Named "Alpha", no main error -> subagent terminal error IS surfaced.
	eqSession(t, sessionsDir, 900001, "alpha", "Alpha", "/w/a")
	aMain := eqTranscript(t, home, "/w/a", "alpha.jsonl", t1,
		eqUserPrompt("hi-alpha"), eqTitle("Alpha"), eqAssistant("claude-a", 100, 40))
	eqSubagent(t, aMain, "agent-1.jsonl", t1, eqAPIError("server_error", "boom", "2026-07-20T10:00:00Z"))

	// 2) Main terminal error present -> subagent error must NOT override it (gate).
	eqSession(t, sessionsDir, 900002, "mainerr", "", "/w/b")
	bMain := eqTranscript(t, home, "/w/b", "mainerr.jsonl", t1,
		eqUserPrompt("hi-b"), eqAssistant("claude-b", 50, 20),
		eqAPIError("server_error", "main-boom", "2026-07-20T11:00:00Z"))
	eqSubagent(t, bMain, "agent-1.jsonl", t1, eqAPIError("rate_limit", "sub-boom", "2026-07-20T10:00:00Z"))

	// 3) Dead PID with subagents NEWER than transcript: dead branch uses
	//    TranscriptMTime, not maxActivity — both paths must agree.
	eqSession(t, sessionsDir, 900003, "dead", "", "/w/c")
	cMain := eqTranscript(t, home, "/w/c", "dead.jsonl", t0,
		eqUserPrompt("hi-c"), eqAssistant("claude-c", 10, 5))
	eqSubagent(t, cMain, "agent-1.jsonl", t2) // newer, but no error

	// 4/5) Shared cwd: two named sessions each resolve to their own titled file.
	eqSession(t, sessionsDir, 900004, "sh1", "Sh1", "/w/d")
	eqSession(t, sessionsDir, 900005, "sh2", "Sh2", "/w/d")
	eqTranscript(t, home, "/w/d", "sh1.jsonl", t0, eqUserPrompt("hi-sh1"), eqTitle("Sh1"), eqAssistant("m", 3, 2))
	eqTranscript(t, home, "/w/d", "sh2.jsonl", t1, eqUserPrompt("hi-sh2"), eqTitle("Sh2"), eqAssistant("m", 7, 4))

	pidAlive = func(pid int) bool { return pid != 900003 } // 900003 is dead
	return sessionsDir, home, pidAlive
}

// newMonitorPoller builds a poller backed by a corpus Monitor with the full
// observer set (SessionSnapshot, SubagentError, UsagePricing, Limits) over the
// given corpus. The old inline path was removed in pg2-66h9g, so the Monitor is
// the only corpus reader; the source-level equivalence vs the native pricer /
// sibling limits source lives in corpus_pricing_equivalence_test.go.
func newMonitorPoller(sessionsDir, home string, pidAlive func(int) bool, now time.Time) *Poller {
	mon := corpus.New(home, &session.Discoverer{SessionsDir: sessionsDir, PidAlive: pidAlive})
	mon.Register(corpus.NewSessionSnapshotObserver())
	mon.Register(corpus.NewSubagentErrorObserver())
	mon.Register(corpus.NewUsagePricingObserver(eqPrices))
	mon.Register(corpus.NewLimitsObserver())
	return &Poller{
		SessionsDir:   sessionsDir,
		ClaudeHome:    home,
		PidAlive:      pidAlive,
		IdleThreshold: time.Hour,
		Now:           func() time.Time { return now },
		Monitor:       mon,
	}
}

// TestSnapshot_CorpusMonitor_MetricParity verifies the pg2-sewtz metric contract
// is preserved after delegation: the Monitor tail emits transcript.scan
// full/cache_hit modes and the "discover" phase (re-homed from the poller),
// while the provider cache emits the subprocess metrics (git_branch) from their
// new home. The recorder is wired once via p.SetPhaseRecorder, which fans out to
// the Monitor and (on lazy-default) the provider cache.
func TestSnapshot_CorpusMonitor_MetricParity(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	now := time.Unix(1_776_000_300, 0)
	ctx := context.Background()

	rec := newFakeRec()
	p := newMonitorPoller(sessionsDir, home, pidAlive, now)
	p.SetPhaseRecorder(rec)

	if _, _, err := p.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.scans["full"] == 0 {
		t.Errorf("cold scan: RecordScan(full) not emitted from the Monitor tail; scans=%v", rec.scans)
	}
	if rec.phases["discover"] == 0 {
		t.Errorf("discover phase not emitted from the Monitor; phases=%v", rec.phases)
	}
	if rec.spawns["git_branch"] == 0 {
		t.Errorf("git_branch subprocess metric not emitted from the poller; spawns=%v", rec.spawns)
	}
	// Second scan on the unchanged corpus -> cache_hit from the Monitor tail.
	if _, _, err := p.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.scans["cache_hit"] == 0 {
		t.Errorf("second scan: RecordScan(cache_hit) not emitted from the Monitor tail; scans=%v", rec.scans)
	}
}
