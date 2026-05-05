package poller

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

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
