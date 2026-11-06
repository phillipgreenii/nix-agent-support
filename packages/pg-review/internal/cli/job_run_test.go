package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

// jobEnv points the tool at throwaway XDG directories; scope true writes a
// config.toml defining the scope "demo".
func jobEnv(t *testing.T, scope bool) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	if scope {
		dir := filepath.Join(root, "config", "pg-review")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[[scope]]\nname = \"demo\"\nprefixes = [\"/work/demo\"]\n[[lens]]\nid = \"agent-workflow\"\nproviders = []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMissingConfigJobRunScopeExits2 extends the config packet's
// TestMissingConfigUsesBuiltins: with no config.toml at all the built-in lens
// resolves but no scope does.
func TestMissingConfigJobRunScopeExits2(t *testing.T) {
	jobEnv(t, false)
	code, _, stderr := run(t, "job", "run", "agent-workflow", "--scope", "x", "--duration", "1h")
	if code != exitcode.Usage || !strings.Contains(stderr, "no scopes are configured") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	jobEnv(t, true)
	code, _, stderr = run(t, "job", "run", "agent-workflow", "--scope", "x", "--duration", "1h")
	if code != exitcode.Usage || !strings.Contains(stderr, "demo") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestJobRunUsageErrors(t *testing.T) {
	jobEnv(t, true)
	for _, args := range [][]string{
		{"job", "run", "agent-workflow", "--scope", "demo"},
		{"job", "run", "agent-workflow", "--scope", "demo", "--from", "2026-03-10T00:00:00Z"},
		{"job", "run", "agent-workflow", "--scope", "demo", "--duration", "0h"},
		{"job", "run", "agent-workflow", "--scope", "demo", "--from", "2026-03-11T00:00:00Z", "--to", "2026-03-10T00:00:00Z"},
		{"job", "run", "agent-workflow", "--scope", "demo", "--duration", "1h", "--analyst", "robot"},
	} {
		if code, _, stderr := run(t, args...); code != exitcode.Usage {
			t.Errorf("%v: exit %d %s", args, code, stderr)
		}
	}
}

func TestJobRunRunKeyAndShowRun(t *testing.T) {
	jobEnv(t, true)
	args := []string{
		"job", "run", "agent-workflow", "--scope", "demo", "--from", "2026-03-10T00:00:00-05:00", "--to", "2026-03-11T00:00:00-05:00",
		"--run-key", "agent-workflow/demo/24h/2026-03-11T05:00:00Z", "--schedule-id", "s1", "--analyst", "none",
	}
	code, out, stderr := run(t, args...)
	if code != exitcode.OK || !strings.Contains(out, "run 1 Filed") {
		t.Fatalf("exit %d %q %s", code, out, stderr)
	}
	if code, _, _ := run(t, args...); code != exitcode.Skipped {
		t.Errorf("repeat exit %d", code)
	}
	// A manual run over the same window runs.
	manual := append([]string(nil), args[:len(args)-6]...)
	if code, out, _ := run(t, manual...); code != exitcode.OK || !strings.Contains(out, "run 2 Filed") {
		t.Errorf("manual exit %d %q", code, out)
	}
	code, out, stderr = run(t, "job", "show-run", "1")
	if code != exitcode.OK {
		t.Fatalf("exit %d %s", code, stderr)
	}
	for _, want := range []string{
		"run 1", "lens agent-workflow", "scope demo", "state Filed", "2026-03-10T05:00:00Z .. 2026-03-11T05:00:00Z",
		"run key agent-workflow/demo/24h/2026-03-11T05:00:00Z", "profile deep-review", "analyst  none", "queries 0", "items    filed 0", "evidence kept until", "evidence_ttl_days 14",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show-run lacks %q:\n%s", want, out)
		}
	}
	if code, _, stderr := run(t, "job", "show-run", "99"); code != exitcode.ValidationRejected || !strings.Contains(stderr, "not found") {
		t.Errorf("unknown id: %d %s", code, stderr)
	}
	if code, _, _ := run(t, "job", "show-run", "abc"); code != exitcode.Usage {
		t.Errorf("bad id: %d", code)
	}
}

func TestShowRunExpiredPrintsDateAndExits4(t *testing.T) {
	jobEnv(t, true)
	l, err := ledger.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	l.SetClock(func() time.Time { return old })
	if err := l.WithTx(context.Background(), func(tx *ledger.Tx) error {
		r, err := tx.InsertRun(context.Background(), ledger.Run{Lens: "agent-workflow", Scope: "demo", State: ledger.RunFiled, EndedAt: old})
		_ = r
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	code, _, stderr := run(t, "job", "show-run", "1")
	if code != exitcode.ValidationRejected || !strings.Contains(stderr, "expired on 2020-01-15") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestJobRunSkippedExits3(t *testing.T) {
	jobEnv(t, true)
	l, err := ledger.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		if err := l.WithTx(ctx, func(tx *ledger.Tx) error {
			_, err := tx.CreateTopic(ctx, ledger.NewTopic{DefinedIn: "a.md", SubjectKey: "s" + string(rune('a'+i)), Kind: "stale", Lens: "agent-workflow"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = l.Close()
	code, _, stderr := run(t, "job", "run", "agent-workflow", "--scope", "demo", "--duration", "1h")
	if code != exitcode.Skipped || !strings.Contains(stderr, "pending cap") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestJobRunHasNoRouterConcept(t *testing.T) {
	jobEnv(t, true)
	_, help, _ := run(t, "job", "run", "--help")
	words := regexp.MustCompile(`(?i)\b(router|event|timer|calendar|cron)\b`)
	if m := words.FindString(help); m != "" {
		t.Errorf("job run help mentions %q:\n%s", m, help)
	}
	for _, p := range []string{"job_run.go", "../runner/pipeline.go", "../runner/window.go", "../runner/gate.go", "../runner/lock.go", "../runner/reaper.go"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		// The adapter-set label flag --schedule-id is tolerated.
		text := strings.ReplaceAll(string(b), "router adapter", "")
		text = strings.ReplaceAll(text, "schedule-id", "")
		// Comments name the adapter; code never does.
		if m := words.FindString(stripComments(text)); m != "" {
			t.Errorf("%s mentions %q outside comments", p, m)
		}
	}
}

func stripComments(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if i := strings.Index(l, "//"); i >= 0 {
			l = l[:i]
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
