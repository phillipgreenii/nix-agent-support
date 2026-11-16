package provision_test

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun/gitruntest"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/provision/provisiontest"
	"github.com/phillipgreenii/x/gitfixture"
)

func TestWorktreeConformance(t *testing.T) {
	provisiontest.Run(t, func(t *testing.T, repo *gitfixture.Repo) provision.Provisioner {
		return provision.NewWorktree(repo.Dir, gitruntest.New(t, repo), time.Minute, 2*time.Minute)
	})
}
