//go:build integration

// Regression guard for pg2-jwilh: Claude Code's worktree-isolation guard refuses
// any command whose program is not a known launcher and that has a git-named
// word among its operands (`rtk git commit -m x`), and it judges the REWRITE
// ceta hands back as updatedInput. So in a linked worktree ceta must drop such
// a rewrite (the original command runs) while keeping every other rewrite.
//
// This drives the REAL compiled binary with the REAL rtk (no mock processor):
// the whole point is that rtk's rewrite of `git ...` depends on Claude Code
// permission allow rules, which a mock cannot reproduce (V-3).

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// requireRTK returns the path of the real rtk, or skips with a reason when it is
// absent and CETA_REQUIRE_RTK is unset. The nix check sets CETA_REQUIRE_RTK=1 so
// a missing rtk fails there instead of silently skipping.
func requireRTK(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("rtk")
	if err != nil {
		if os.Getenv("CETA_REQUIRE_RTK") != "" {
			t.Fatalf("rtk is not on PATH but CETA_REQUIRE_RTK is set: %v", err)
		}
		t.Skipf("rtk not on PATH (set CETA_REQUIRE_RTK=1 to make this a failure): %v", err)
	}
	return p
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func updatedCommand(result map[string]any) (string, bool) {
	hso, ok := result["hookSpecificOutput"].(map[string]any)
	if !ok {
		return "", false
	}
	ui, ok := hso["updatedInput"].(map[string]any)
	if !ok {
		return "", false
	}
	c, ok := ui["command"].(string)
	return c, ok
}

func TestIntegration_InputProcessor_RtkRewriteDroppedInLinkedWorktree(t *testing.T) {
	rtk := requireRTK(t)

	// Build the binary BEFORE HOME is redirected: testBinary runs `go build`, and
	// under a temp HOME it would populate a read-only module cache there that
	// TempDir cleanup cannot remove.
	testBinary(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"permissions":{"allow":["Bash(git add:*)","Bash(git commit:*)","Bash(git push:*)","Bash(git status:*)","Bash(ls:*)"]}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CETA_INPUT_PROCESSORS", "rtk rewrite")

	repo := gittest.New(t, gitfixture.RepoOptions{})
	if _, err := repo.Commit(t.Context(), "init", map[string]string{"README.md": "hi\n"}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	primary := repo.Dir
	worktree := filepath.Join(primary, "wt")
	if out, err := repo.Client.Run(t.Context(), "-C", primary, "worktree", "add", "-q", "-b", "feat", worktree); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}

	// Precondition, asserted directly so a future rtk change cannot make the
	// cases below vacuous: under this HOME rtk rewrites `git commit -m x`.
	pre := exec.Command(rtk, "rewrite", "git commit -m x")
	pre.Dir = primary
	pre.Env = os.Environ()
	out, err := pre.Output()
	if err != nil {
		t.Fatalf("precondition: rtk rewrite 'git commit -m x' failed (it must exit 0 under this HOME): %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "rtk git commit -m x" {
		t.Fatalf("precondition: rtk rewrite printed %q, want %q", got, "rtk git commit -m x")
	}

	hook := func(cwd, command string) (map[string]any, string) {
		t.Helper()
		in := `{"tool_name":"Bash","tool_input":{"command":` + jsonString(command) + `},"cwd":` + jsonString(cwd) + `}`
		return runHookOnce(t, in)
	}

	// (a) linked worktree: every git command keeps its decision but loses the
	// rtk rewrite, so Claude Code runs the original.
	for _, cmd := range []string{"git add -A", "git commit -m x", "git push", "git status"} {
		res, stderr := hook(worktree, cmd)
		if !strings.Contains(stderr, "rewrite dropped") {
			t.Errorf("(a) %q: stderr does not report the dropped rewrite: %q", cmd, stderr)
		}
		if d := getDecision(res); d != "allow" {
			t.Errorf("(a) %q in a linked worktree: decision = %q, want allow", cmd, d)
		}
		if c, ok := updatedCommand(res); ok {
			t.Errorf("(a) %q in a linked worktree: updatedInput.command = %q, want NO updatedInput (the isolation guard refuses an rtk git rewrite)", cmd, c)
		}
	}

	// (b) linked worktree: non-git compaction is kept.
	res, _ := hook(worktree, "ls -la")
	if c, ok := updatedCommand(res); !ok || c != "rtk ls -la" {
		t.Errorf("(b) ls -la in a linked worktree: updatedInput = %q (present=%v), want %q", c, ok, "rtk ls -la")
	}

	// (c) primary checkout: no isolation guard can apply, so git compaction stays.
	res, _ = hook(primary, "git commit -m x")
	if c, ok := updatedCommand(res); !ok || c != "rtk git commit -m x" {
		t.Errorf("(c) git commit -m x in the primary checkout: updatedInput = %q (present=%v), want %q", c, ok, "rtk git commit -m x")
	}
}
