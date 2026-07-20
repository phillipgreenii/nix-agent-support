package poller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

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

func eqFillers(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = `{"type":"system","subtype":"noise"}`
	}
	return out
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

func newInlinePoller(sessionsDir, home string, pidAlive func(int) bool, now time.Time) *Poller {
	return &Poller{
		SessionsDir:   sessionsDir,
		ClaudeHome:    home,
		PidAlive:      pidAlive,
		IdleThreshold: time.Hour,
		Now:           func() time.Time { return now },
		Pricer:        &fakeCostPricer{},
	}
}

func newMonitorPoller(sessionsDir, home string, pidAlive func(int) bool, now time.Time) *Poller {
	p := newInlinePoller(sessionsDir, home, pidAlive, now)
	mon := corpus.New(home, &session.Discoverer{SessionsDir: sessionsDir, PidAlive: pidAlive})
	mon.Register(corpus.NewSessionSnapshotObserver())
	mon.Register(corpus.NewSubagentErrorObserver())
	p.Monitor = mon
	p.UseCorpusMonitor = true
	return p
}

func zeroVolatile(tree *aggregate.Tree) {
	if tree != nil {
		tree.GeneratedAt = time.Time{}
	}
}

func TestSnapshot_CorpusMonitorEqualsInline(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	now := time.Unix(1_776_000_300, 0)
	ctx := context.Background()

	inline, _, err := newInlinePoller(sessionsDir, home, pidAlive, now).Snapshot(ctx)
	if err != nil {
		t.Fatalf("inline Snapshot: %v", err)
	}
	delegated, _, err := newMonitorPoller(sessionsDir, home, pidAlive, now).Snapshot(ctx)
	if err != nil {
		t.Fatalf("delegated Snapshot: %v", err)
	}

	zeroVolatile(inline)
	zeroVolatile(delegated)
	if !reflect.DeepEqual(inline, delegated) {
		t.Fatalf("Monitor-backed tree != inline tree\n INLINE:    %+v\n DELEGATED: %+v", inline, delegated)
	}
}

// TestSnapshot_TitleAtLine500_CorrectedResolution asserts the one intended,
// documented divergence: a custom-title beyond the old 200-line cap resolves
// correctly under the Monitor path but not under the (capped) inline path.
func TestSnapshot_TitleAtLine500_CorrectedResolution(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// sessID "zzz" matches neither candidate file basename, so the inline path's
	// title arm (capped) fails and it falls to the newest file (Y); the Monitor
	// path finds the title at line ~501 (X).
	eqSession(t, sessionsDir, 900010, "zzz", "Late", "/w/late")
	// X: older, FirstPrompt "fromX", title "Late" at ~line 501.
	xLines := append([]string{eqUserPrompt("fromX")}, eqFillers(500)...)
	xLines = append(xLines, eqTitle("Late"), eqAssistant("m", 1, 1))
	eqTranscript(t, home, "/w/late", "xfile.jsonl", time.Unix(1_776_000_000, 0), xLines...)
	// Y: newer, untitled, FirstPrompt "fromY".
	eqTranscript(t, home, "/w/late", "yfile.jsonl", time.Unix(1_776_000_100, 0),
		eqUserPrompt("fromY"), eqAssistant("m", 1, 1))

	now := time.Unix(1_776_000_300, 0)
	pidAlive := func(int) bool { return true }
	ctx := context.Background()

	inline, _, _ := newInlinePoller(sessionsDir, home, pidAlive, now).Snapshot(ctx)
	delegated, _, _ := newMonitorPoller(sessionsDir, home, pidAlive, now).Snapshot(ctx)

	inlineFP := findSession(t, inline, "/w/late").FirstPrompt
	delegatedFP := findSession(t, delegated, "/w/late").FirstPrompt
	if inlineFP != "fromY" {
		t.Fatalf("inline FirstPrompt = %q, want fromY (capped path falls to newest)", inlineFP)
	}
	if delegatedFP != "fromX" {
		t.Fatalf("delegated FirstPrompt = %q, want fromX (Monitor resolves the late title)", delegatedFP)
	}
}
