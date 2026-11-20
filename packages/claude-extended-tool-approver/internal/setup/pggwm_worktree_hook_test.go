package setup

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/pggwmworktree"
)

// pgGwmRepo makes a real git repository with an initial commit, a linked
// worktree of it, and a pg-gwm configuration (under the XDG_CONFIG_HOME
// withXDGConfig set) that lists it, with one standing exception.
func pgGwmRepo(t *testing.T, listed bool) (repo, linked, other string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(root, "zr")
	other = filepath.Join(root, "other")
	for _, d := range []string{repo, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"init", "-q", "-b", "main"},
			{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		} {
			cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", d}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
	linked = filepath.Join(root, "zr-linked")
	if out, err := exec.CommandContext(context.Background(), "git", "-C", repo, "worktree", "add", "-q", "-b", "lnk", linked).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if listed {
		cfg := "[repos.\"" + repo + "\"]\nmin = 1\n\n" +
			"[[repos.\"" + repo + "\".exceptions]]\npath = \"" + repo + "/.worktrees/slot-*\"\nreason = \"slots\"\nowner = \"o\"\nreview-by = \"2027-01-01\"\n"
		writePgGwmConfig(t, cfg)
	}
	return repo, linked, other
}

func writePgGwmConfig(t *testing.T, content string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pg-gwm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hookVerdict(t *testing.T, cwd, tool string, input map[string]any) hookio.RuleResult {
	t.Helper()
	ti, _ := json.Marshal(input)
	in := &hookio.HookInput{
		SessionID: "sess-1", CWD: cwd, ToolName: tool, ToolInput: ti,
		PermissionMode: "default", HookEventName: "PreToolUse", ToolUseID: "t",
	}
	verdict, _ := NewEngineForCWDLive(cwd, nil).EvaluateHookBoth(in)
	return verdict
}

// TestLiveHook_PgGwmWorktreeDeny drives the real production rule chain (no mock)
// against real git repositories and a real pg-gwm config file.
func TestLiveHook_PgGwmWorktreeDeny(t *testing.T) {
	withXDGConfig(t, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(pggwmworktree.ExceptionEnv, "")
	repo, linked, other := pgGwmRepo(t, true)

	bash := func(cwd, cmd string) hookio.RuleResult {
		return hookVerdict(t, cwd, "Bash", map[string]any{"command": cmd})
	}
	type tc struct {
		name string
		got  func() hookio.RuleResult
		deny bool
	}
	cases := []tc{
		{"EnterWorktree", func() hookio.RuleResult { return hookVerdict(t, repo, "EnterWorktree", map[string]any{}) }, true},
		{"EnterWorktree from a linked worktree", func() hookio.RuleResult { return hookVerdict(t, linked, "EnterWorktree", map[string]any{}) }, true},
		{"Agent isolation worktree", func() hookio.RuleResult {
			return hookVerdict(t, repo, "Agent", map[string]any{"isolation": "worktree", "prompt": "x"})
		}, true},
		{"Agent without isolation", func() hookio.RuleResult {
			return hookVerdict(t, repo, "Agent", map[string]any{"prompt": "x"})
		}, false},
		{"git worktree add plain", func() hookio.RuleResult { return bash(repo, "git worktree add "+repo+"/.worktrees/new -b nb") }, true},
		{"git worktree add with -C", func() hookio.RuleResult {
			return bash(other, "git -C "+repo+" worktree add "+repo+"/.worktrees/new -b nb")
		}, true},
		{"git worktree add after cd", func() hookio.RuleResult {
			return bash(other, "cd "+repo+" && git worktree add "+repo+"/.worktrees/new -b nb")
		}, true},
		{"git worktree add in a compound", func() hookio.RuleResult {
			return bash(repo, "echo hi && git worktree add "+repo+"/.worktrees/new -b nb && echo done")
		}, true},
		{"git worktree add from a linked worktree", func() hookio.RuleResult { return bash(linked, "git worktree add "+linked+"/.worktrees/new2 -b nb2") }, true},
		{"bin/worktree create", func() hookio.RuleResult { return bash(repo, "bin/worktree create feature") }, true},
		{"./bin/worktree create absolute", func() hookio.RuleResult { return bash(other, repo+"/bin/worktree create feature") }, true},
		{"git worktree list stays allowed", func() hookio.RuleResult { return bash(repo, "git worktree list") }, false},
		{"git worktree remove stays allowed", func() hookio.RuleResult { return bash(repo, "git worktree remove "+linked) }, false},
		{"non-pg-gwm repo allowed", func() hookio.RuleResult { return bash(other, "git worktree add "+other+"/.worktrees/o2 -b ob") }, false},
		{"standing exception path allowed", func() hookio.RuleResult { return bash(repo, "git worktree add "+repo+"/.worktrees/slot-a -b sa") }, false},
		{"empty reason denied", func() hookio.RuleResult {
			return bash(repo, "PG_GWM_EXCEPTION= git worktree add "+repo+"/.worktrees/new -b nb")
		}, true},
		{"exception reason allowed", func() hookio.RuleResult {
			return bash(repo, "PG_GWM_EXCEPTION='bisecting an old release' git worktree add "+repo+"/.worktrees/new -b nb")
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := c.got()
			if c.deny {
				if v.Decision != hookio.Reject || v.Module != "pg-gwm-worktree" {
					t.Fatalf("got %v by %q (%s), want Reject by pg-gwm-worktree", v.Decision, v.Module, v.Reason)
				}
				if !strings.Contains(v.Reason, "pg-gwm lease --name <name> -b <branch> --print=path") {
					t.Errorf("deny message lacks the lease command: %s", v.Reason)
				}
			} else if v.Decision == hookio.Reject {
				t.Fatalf("got Reject by %q (%s), want it allowed", v.Module, v.Reason)
			}
		})
	}
}

func TestLiveHook_PgGwmWorktreeSessionEnvException(t *testing.T) {
	withXDGConfig(t, "")
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	repo, _, _ := pgGwmRepo(t, true)

	t.Setenv(pggwmworktree.ExceptionEnv, "operator asked for a raw worktree")
	for _, tool := range []string{"EnterWorktree", "Agent"} {
		in := map[string]any{}
		if tool == "Agent" {
			in["isolation"] = "worktree"
		}
		if v := hookVerdict(t, repo, tool, in); v.Decision == hookio.Reject {
			t.Fatalf("%s with a session exception: got Reject (%s)", tool, v.Reason)
		}
	}
	c := pggwmworktree.ReadCounters(filepath.Join(state, "claude-extended-tool-approver"))
	if c.Exceptions["EnterWorktree"] != 1 || c.Exceptions["Agent"] != 1 || c.Denies != 0 {
		t.Errorf("counters = %+v, want one EnterWorktree and one Agent exception, no denies", c)
	}
}

func TestLiveHook_PgGwmWorktreeUnreadableConfigFailsOpen(t *testing.T) {
	withXDGConfig(t, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(pggwmworktree.ExceptionEnv, "")
	repo, _, _ := pgGwmRepo(t, false)
	// A directory where the file should be: opening succeeds, reading fails.
	if err := os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pg-gwm", "config.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if v := hookVerdict(t, repo, "EnterWorktree", map[string]any{}); v.Decision == hookio.Reject {
		t.Fatalf("unreadable config must fail open, got Reject (%s)", v.Reason)
	}
}
