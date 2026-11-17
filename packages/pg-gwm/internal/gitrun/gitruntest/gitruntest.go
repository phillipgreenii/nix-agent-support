// Package gitruntest builds a hermetic gitrun.Git for tests: real git on
// cmdrun.Exec, isolated from the machine's git configuration.
package gitruntest

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
)

// New returns a Git on the real runner for repo's fixture: a temporary HOME,
// no system or global config, discovery capped at the fixture root's parent,
// and a fixed author and committer identity. With PG_GWM_COUNT_GIT set, each
// test logs how many git processes this Git started (run with -v), a
// load-independent measure of a test's cost.
func New(t *testing.T, repo *gitfixture.Repo) gitrun.Git {
	t.Helper()
	var spawns atomic.Int64
	if os.Getenv("PG_GWM_COUNT_GIT") != "" {
		t.Cleanup(func() { t.Logf("git spawns: %d", spawns.Load()) })
	}
	return gitrun.Git{
		Run: func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
			spawns.Add(1)
			return cmdrun.Exec(ctx, c)
		},
		Bin: "git",
		ExtraEnv: []string{
			"HOME=" + t.TempDir(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CEILING_DIRECTORIES=" + filepath.Dir(repo.Root()),
			"GIT_AUTHOR_NAME=pg-gwm test",
			"GIT_AUTHOR_EMAIL=pg-gwm@test.invalid",
			"GIT_COMMITTER_NAME=pg-gwm test",
			"GIT_COMMITTER_EMAIL=pg-gwm@test.invalid",
		},
	}
}
