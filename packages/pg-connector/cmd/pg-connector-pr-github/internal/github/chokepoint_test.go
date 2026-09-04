package github

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ghExecRE matches an `exec.Command`/`exec.CommandContext` whose BINARY is the
// literal "gh" — i.e. a direct gh subprocess, bypassing the token preflight.
var ghExecRE = regexp.MustCompile(`exec\.Command(?:Context)?\(\s*(?:[\w.]+\s*,\s*)?"gh"`)

// TestGHExecChokePoint is the module-wide half of bead pg2-ilzq9's guarantee:
// the four sites fixed there were fixed one at a time, and nothing stopped a
// fifth from appearing. It asserts that `gh` is spawned from exactly the
// choke-point file pairs below, one pair per backend that ports this
// mechanism:
//
//   - ghexec.go — the token-protected choke point (resolve, then exec).
//   - token.go  — the token RESOLVER, the one sanctioned exception; routing it
//     through the preflight would be circular.
//
// The pg-connector-ci-github-actions pair was added when that backend
// carried over this same mechanism into its own cmd/<binary>/internal/github
// tree [bead pg2-2j5ac.10; design: §4.6, §5.2] — Go's internal/ visibility
// rule makes each backend's copy independent, so each backend's own
// ghexec.go/token.go pair is allowlisted separately rather than shared.
//
// Every other package reaches gh through a backend's own github.CLI
// (Run/RunStdin, or Command for sites that wire up their own
// stdout/stderr/dir), so a new direct exec here means an unauthenticated gh
// became reachable again — which is what popped interactive auth screens
// under the launchd sync agent.
func TestGHExecChokePoint(t *testing.T) {
	root := moduleRoot(t)
	allowed := map[string]bool{
		filepath.Join("cmd", "pg-connector-pr-github", "internal", "github", "ghexec.go"):         true,
		filepath.Join("cmd", "pg-connector-pr-github", "internal", "github", "token.go"):          true,
		filepath.Join("cmd", "pg-connector-ci-github-actions", "internal", "github", "ghexec.go"): true,
		filepath.Join("cmd", "pg-connector-ci-github-actions", "internal", "github", "token.go"):  true,
	}

	var offenders []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if allowed[rel] {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if ghExecRE.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk module: %v", walkErr)
	}
	if len(offenders) > 0 {
		t.Fatalf("gh is exec'd directly outside the protected choke point:\n  %s\n\n"+
			"Route it through github.NewCLI(): Run/RunStdin for a whole invocation, or\n"+
			"Command when the call site needs the *exec.Cmd (working directory, own\n"+
			"stderr capture). See ghexec.go.",
			strings.Join(offenders, "\n  "))
	}
}

// moduleRoot walks up from the test's working directory to the directory holding
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test's working directory")
		}
		dir = parent
	}
}
