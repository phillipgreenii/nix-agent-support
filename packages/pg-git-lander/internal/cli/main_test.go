package cli_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
)

// tpl is the one prepared repository (a single initial commit) every test
// copies with gittest.FromTemplate instead of paying a fresh git init per test.
var tpl *gitfixture.Template

func TestMain(m *testing.M) { os.Exit(runMain(m)) }

func runMain(m *testing.M) int {
	root, err := os.MkdirTemp("", "cli-template-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "template root:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()
	tpl, err = gitfixture.NewTemplate(context.Background(), root, gitfixture.RepoOptions{InitialCommit: true, Suite: "template"}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "template:", err)
		return 1
	}
	return m.Run()
}
