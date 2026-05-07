package poller

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

// makeRateLimitFixture writes a session file + transcript with a single
// synthetic rate-limit event whose reset time is the supplied resetISO. Returns
// (sessionsDir, claudeHome).
func makeRateLimitFixture(t *testing.T, resetISO string) (string, string) {
	t.Helper()
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	claudeHome := filepath.Join(root, "claude-home")
	cwd := filepath.Join(root, "cwd")
	slug := strings.NewReplacer("/", "-", "_", "-").Replace(cwd)
	projectDir := filepath.Join(claudeHome, "projects", slug)
	for _, d := range []string{sessionsDir, projectDir, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionJSON := fmt.Sprintf(`{"pid":99001,"sessionId":"rl-sess","cwd":%q,"startedAt":1776000000000,"kind":"interactive","entrypoint":"cli"}`, cwd)
	if err := os.WriteFile(filepath.Join(sessionsDir, "99001.json"), []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// The encoded project dir name is "-<basename>-cwd" produced from cwd above.
	transcript := `{"type":"assistant","timestamp":"` + resetISO + `","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your limit · resets 1pm (UTC)"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, "rl-sess.jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	return sessionsDir, claudeHome
}

func TestSnapshotZeroesStaleRateLimitResetsAt(t *testing.T) {
	// Synthetic rate-limit event written 2026-05-05 at 00:00 UTC says
	// "resets 1pm (UTC)" → reset = 2026-05-05 13:00 UTC. With Now() far past that
	// (2026-05-06), the enrichment must show RateLimitResetsAt zero.
	sessionsDir, claudeHome := makeRateLimitFixture(t, "2026-05-05T00:00:00Z")
	p := &Poller{
		SessionsDir: sessionsDir,
		ClaudeHome:  claudeHome,
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC) },
		CCUsageFn:   func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil },
	}
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range tree.Dirs {
		for _, s := range d.Sessions {
			if !s.SessionEnrichment.RateLimitResetsAt.IsZero() {
				t.Errorf("RateLimitResetsAt = %v, want zero (stale beyond grace)", s.SessionEnrichment.RateLimitResetsAt)
			}
		}
	}
	if !tree.WindowResetsAt.IsZero() {
		t.Errorf("WindowResetsAt = %v, want zero (no live pauses)", tree.WindowResetsAt)
	}
}

func TestSnapshotKeepsRecentRateLimitResetsAt(t *testing.T) {
	// Reset at 2026-05-05 13:00 UTC, Now() = 2026-05-05 13:02 UTC (only 2 min
	// past — within stalePauseGrace). The reset MUST be preserved so the auto-
	// resume path can still fire.
	sessionsDir, claudeHome := makeRateLimitFixture(t, "2026-05-05T00:00:00Z")
	p := &Poller{
		SessionsDir: sessionsDir,
		ClaudeHome:  claudeHome,
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Date(2026, 5, 5, 13, 2, 0, 0, time.UTC) },
		CCUsageFn:   func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil },
	}
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 5, 5, 13, 0, 0, 0, time.UTC)
	found := false
	for _, d := range tree.Dirs {
		for _, s := range d.Sessions {
			if s.SessionEnrichment.RateLimitResetsAt.Equal(want) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("RateLimitResetsAt was filtered prematurely; tree=%+v", tree.Dirs)
	}
}

func TestSnapshotProducesTree(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../tests/fixtures/sessions",
		ClaudeHome:  "../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.CCUsageFn = func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil }
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tree == nil {
		t.Fatal("nil tree")
	}
}

func TestSnapshotEnrichmentFields(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../tests/fixtures/sessions",
		ClaudeHome:  "../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.CCUsageFn = func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil }
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Find the session with cwd "/tmp/x" (abc-def)
	var found *struct {
		sessionTokens int
		contextTokens int
		model         string
	}
	for _, d := range tree.Dirs {
		if d.Path != "/tmp/x" {
			continue
		}
		for _, s := range d.Sessions {
			found = &struct {
				sessionTokens int
				contextTokens int
				model         string
			}{
				sessionTokens: s.SessionEnrichment.SessionTokens,
				contextTokens: s.SessionEnrichment.ContextTokens,
				model:         s.SessionEnrichment.Model,
			}
		}
	}
	if found == nil {
		t.Fatal("session for /tmp/x not found in tree")
	}
	if found.sessionTokens != 42 {
		t.Errorf("SessionTokens = %d, want 42", found.sessionTokens)
	}
	if found.contextTokens != 100 {
		t.Errorf("ContextTokens = %d, want 100", found.contextTokens)
	}
	if found.model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want claude-sonnet-4-6", found.model)
	}
	// Directory token total must equal session total (only one session in /tmp/x)
	for _, d := range tree.Dirs {
		if d.Path == "/tmp/x" && d.TotalTokens != 42 {
			t.Errorf("Directory /tmp/x TotalTokens = %d, want 42", d.TotalTokens)
		}
	}
}

func TestSnapshotPopulatesTerminalHostCache(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../tests/fixtures/sessions",
		ClaudeHome:  "../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.CCUsageFn = func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil }

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.terminalHostCache) == 0 {
		t.Error("terminalHostCache should be populated after Snapshot")
	}
}

func TestSnapshotPopulatesTranscriptCache(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../tests/fixtures/sessions",
		ClaudeHome:  "../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.CCUsageFn = func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil }

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.transcriptCache) == 0 {
		t.Error("transcriptCache should be populated after Snapshot")
	}
}

func TestSnapshotTerminalHostCacheRetainsAcrossPolls(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../tests/fixtures/sessions",
		ClaudeHome:  "../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.CCUsageFn = func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil }

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstCache := maps.Clone(p.terminalHostCache)

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	for pid, host := range firstCache {
		if got := p.terminalHostCache[pid]; got != host {
			t.Errorf("terminalHostCache[%d]: first=%q second=%q (changed unexpectedly)", pid, host, got)
		}
	}
}

func TestSnapshotTranscriptCacheRetainsAcrossPolls(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../tests/fixtures/sessions",
		ClaudeHome:  "../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.CCUsageFn = func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil }

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstCache := make(map[string]string, len(p.transcriptCache))
	for id, entry := range p.transcriptCache {
		firstCache[id] = entry.path
	}

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, path := range firstCache {
		if got := p.transcriptCache[id].path; got != path {
			t.Errorf("transcriptCache[%s].path: first=%q second=%q (changed unexpectedly)", id, path, got)
		}
	}
}

func TestSnapshotPRLookupCalledOncePerDir(t *testing.T) {
	type call struct{ cwd, branch string }
	var calls []call

	p := &Poller{
		SessionsDir: "../../tests/fixtures/sessions",
		ClaudeHome:  "../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
		CCUsageFn:   func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil },
		PRLookupFn: func(_ context.Context, cwd, branch string) (*session.PRInfo, error) {
			calls = append(calls, call{cwd, branch})
			return nil, nil
		},
	}
	_, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// PRLookupFn must not be called for sessions with empty branch.
	for _, c := range calls {
		if c.branch == "" {
			t.Errorf("PRLookupFn called with empty branch for cwd=%q", c.cwd)
		}
	}
	// Must be called at most once per cwd.
	cwdCount := map[string]int{}
	for _, c := range calls {
		cwdCount[c.cwd]++
	}
	for cwd, count := range cwdCount {
		if count > 1 {
			t.Errorf("PRLookupFn called %d times for cwd=%q, want at most 1", count, cwd)
		}
	}
}
