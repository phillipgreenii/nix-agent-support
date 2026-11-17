package manager

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// baseRepo is one prebuilt repository with an initial commit, built once per
// test binary and copied per test (a copy costs a directory copy and a few git
// config calls, instead of the git init, add and commit spawns a fresh
// repository pays under -race). It is read-only: each test uses its own copy.
var baseRepo *gitfixture.Template

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "pg-gwm-template-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	baseRepo, err = gitfixture.NewTemplate(context.Background(), root, gitfixture.RepoOptions{Suite: "manager", InitialCommit: true}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "building the template repository:", err)
		_ = os.RemoveAll(root)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

// newBaseRepo returns this test's own copy of baseRepo.
func newBaseRepo(t *testing.T) *gitfixture.Repo {
	t.Helper()
	return gittest.FromTemplate(t, baseRepo, "manager")
}
