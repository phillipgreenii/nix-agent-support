package main

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// TestParseSelfLogin/TestReadBeadsPrefix_*/TestPrecheckPrefix_*/TestPrecheck_*
// (and their shared fakeBR fixture) moved to
// packages/pg-router-ccpool-handler/cmd/pg-router-ccpool-handler/
// preflight_test.go along with the precheck/precheckPrefix/resolveSelf/
// parseSelfLogin/readBeadsPrefix functions they exercised (docket
// pg2-oju6w's Task 5.8 — see drain.go's own doc comment on that move).

// TestWarnTrackedConfig_ignoresLeakedGitDir is the regression pin for
// pg2-bh09g: git's repository discovery consults GIT_DIR/GIT_WORK_TREE before
// -C, so a `git commit` from a linked worktree that exports them into the
// process environment (mechanism write-up: pg2-67h4y) must not make
// warnTrackedConfig answer about the LEAKED repository instead of
// cfg.RepoRoot. target's copy of .pg-router/config.toml is untracked (no
// warning expected); leaked's copy is committed (tracked). Verified to FAIL
// against the pre-fix code (a bare exec.CommandContext with no cmd.Env,
// which inherits os.Environ() wholesale): it then warns about target even
// though target's own file is untracked, because the leaked GIT_DIR silently
// redirected the check at leaked's tracked copy.
func TestWarnTrackedConfig_ignoresLeakedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	target := gittest.New(t, gitfixture.RepoOptions{})
	if err := target.WriteFile(".pg-router/config.toml", "untracked"); err != nil {
		t.Fatal(err)
	}

	leaked := gittest.New(t, gitfixture.RepoOptions{})
	if _, err := leaked.Commit(context.Background(), "add tracked config",
		map[string]string{".pg-router/config.toml": "tracked"}); err != nil {
		t.Fatal(err)
	}

	// Simulate the leak vector: GIT_DIR/GIT_WORK_TREE set in the ambient
	// environment pointing at a DIFFERENT repository than cfg.RepoRoot.
	t.Setenv("GIT_DIR", filepath.Join(leaked.Dir, ".git"))
	t.Setenv("GIT_WORK_TREE", leaked.Dir)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	warnTrackedConfig(context.Background(), config.Config{RepoRoot: target.Dir})

	if strings.Contains(buf.String(), "tracked by git") {
		t.Fatalf("warnTrackedConfig warned that target's config.toml is tracked, but target's own copy is untracked -- a leaked GIT_DIR/GIT_WORK_TREE answered about the LEAKED repo instead of %s:\n%s", target.Dir, buf.String())
	}
}
