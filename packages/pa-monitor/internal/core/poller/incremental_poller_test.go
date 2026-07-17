package poller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// makeSessionFixture writes a session registry file plus a transcript with the
// given body, and returns the dirs, the session cwd, and the transcript path so
// the test can append to it between polls.
func makeSessionFixture(t *testing.T, pid int, sessID, body string) (sessionsDir, claudeHome, cwd, tpath string) {
	t.Helper()
	root := t.TempDir()
	sessionsDir = filepath.Join(root, "sessions")
	claudeHome = filepath.Join(root, "claude-home")
	cwd = filepath.Join(root, "cwd")
	slug := strings.NewReplacer("/", "-", "_", "-").Replace(cwd)
	projectDir := filepath.Join(claudeHome, "projects", slug)
	for _, d := range []string{sessionsDir, projectDir, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionJSON := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":1776000000000,"kind":"interactive","entrypoint":"cli"}`, pid, sessID, cwd)
	if err := os.WriteFile(filepath.Join(sessionsDir, fmt.Sprintf("%d.json", pid)), []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	tpath = filepath.Join(projectDir, sessID+".jsonl")
	if err := os.WriteFile(tpath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return sessionsDir, claudeHome, cwd, tpath
}

func findSession(t *testing.T, tree *aggregate.Tree, cwd string) *aggregate.SessionView {
	t.Helper()
	for _, d := range tree.Dirs {
		if d.Path != cwd {
			continue
		}
		if len(d.Sessions) == 0 {
			t.Fatalf("dir %q has no sessions", cwd)
		}
		return d.Sessions[0]
	}
	t.Fatalf("session dir %q not found in tree", cwd)
	return nil
}

// After appending to a live session's transcript, the warm poller (which folds
// only the appended bytes through its cached accumulator) must produce exactly
// the same enrichment as a cold poller that parses the whole file from scratch.
// This is the poller-level analogue of the transcript equivalence property and
// guards the incremental wiring against double-counting or stale reuse.
func TestSnapshotIncrementalMatchesColdAfterAppend(t *testing.T) {
	initial := `{"type":"user","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"do the task"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-04-23T10:00:05Z","message":{"model":"claude-opus-4-7","role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Task"}],"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5}}}` + "\n"
	appended := `{"type":"user","timestamp":"2026-04-23T10:00:10Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok"}]}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-04-23T10:00:15Z","message":{"model":"claude-opus-4-7","role":"assistant","content":[{"type":"text","text":"finished"}],"usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":7}}}` + "\n"

	sessionsDir, claudeHome, cwd, tpath := makeSessionFixture(t, 99010, "inc-sess", initial)
	now := func() time.Time { return time.Date(2026, 4, 23, 10, 1, 0, 0, time.UTC) }

	warm := &Poller{SessionsDir: sessionsDir, ClaudeHome: claudeHome, PidAlive: func(int) bool { return true }, Now: now, Pricer: &fakeCostPricer{}}

	t1, _, err := warm.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := findSession(t, t1, cwd)
	if first.SessionTokens != 5 || first.SubagentCount != 1 {
		t.Fatalf("pre-append enrichment unexpected: tokens=%d subagents=%d (want 5,1)", first.SessionTokens, first.SubagentCount)
	}

	// Append the rest of the turn; bump mtime forward so the poller sees a change
	// even on coarse-granularity filesystems.
	f, err := os.OpenFile(tpath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(appended); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(tpath, future, future); err != nil {
		t.Fatal(err)
	}

	t2, _, err := warm.Snapshot(context.Background()) // incremental fold path
	if err != nil {
		t.Fatal(err)
	}
	got := findSession(t, t2, cwd)

	cold := &Poller{SessionsDir: sessionsDir, ClaudeHome: claudeHome, PidAlive: func(int) bool { return true }, Now: now, Pricer: &fakeCostPricer{}}
	t3, _, err := cold.Snapshot(context.Background()) // cold full parse
	if err != nil {
		t.Fatal(err)
	}
	want := findSession(t, t3, cwd)

	if got.SessionTokens != want.SessionTokens {
		t.Errorf("SessionTokens: warm=%d cold=%d", got.SessionTokens, want.SessionTokens)
	}
	if got.ContextTokens != want.ContextTokens {
		t.Errorf("ContextTokens: warm=%d cold=%d", got.ContextTokens, want.ContextTokens)
	}
	if got.Model != want.Model {
		t.Errorf("Model: warm=%q cold=%q", got.Model, want.Model)
	}
	if got.SubagentCount != want.SubagentCount {
		t.Errorf("SubagentCount: warm=%d cold=%d", got.SubagentCount, want.SubagentCount)
	}
	// Sanity: the append must actually have changed the numbers (cumulative
	// output 5+7=12, latest context 20, Task tu1 now closed).
	if want.SessionTokens != 12 || want.ContextTokens != 20 || want.SubagentCount != 0 {
		t.Fatalf("cold post-append enrichment unexpected: tokens=%d ctx=%d subagents=%d (want 12,20,0)", want.SessionTokens, want.ContextTokens, want.SubagentCount)
	}
}
