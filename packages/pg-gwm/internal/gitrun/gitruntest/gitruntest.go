// Package gitruntest builds a hermetic gitrun.Git for tests: real git on
// cmdrun.Exec, isolated from the machine's git configuration.
package gitruntest

import (
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
)

// New returns a Git on the real runner for repo's fixture: a temporary HOME,
// no system or global config, discovery capped at the fixture root's parent,
// and a fixed author and committer identity.
func New(t *testing.T, repo *gitfixture.Repo) gitrun.Git {
	t.Helper()
	return gitrun.Git{
		Run: cmdrun.Exec,
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
