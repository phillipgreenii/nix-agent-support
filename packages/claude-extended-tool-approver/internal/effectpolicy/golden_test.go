package effectpolicy

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

var update = flag.Bool("update", false, "regenerate golden .mmd files")

// fixture builds a throwaway project root (with .git and README.md) and a
// separate HOME, returning both realpath-resolved so substitution matches what
// patheval resolves.
func fixture(t *testing.T) (root, home string) {
	t.Helper()
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{"WORKSPACE_ROOT", "CETA_EXTRA_READWRITE_ROOTS", "CETA_EXTRA_READONLY_ROOTS", "CETA_DENIED_ROOTS", "XDG_DATA_HOME"} {
		t.Setenv(v, "")
	}
	real := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	return real(root), real(home)
}

func TestGolden(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    evalcontract.Decision
	}{
		{"cat_readme", "cat README.md", evalcontract.Approve},
		// /nix/store is zoned read-only by string prefix, so this case is
		// portable. A /etc/hosts variant was dropped: on NixOS it is a symlink
		// into the store and rejects, on any other host it is unzoned and
		// abstains, so the same golden cannot pass on both.
		{"cat_redirect_nix_store", "cat README.md > /nix/store/hosts", evalcontract.Reject},
		{"cat_ssh_key", "cat ~/.ssh/id_rsa", evalcontract.Reject},
		{"cat_unknown_flag", "cat --weird-flag README.md", evalcontract.Abstain},
		{"cat_dynamic", `cat "$F"`, evalcontract.Abstain},
		{"head_n_readme", "head -n 5 README.md", evalcontract.Approve},
		{"frobnicate_readme", "frobnicate README.md", evalcontract.Abstain},
		{"cat_pipe_frobnicate", "cat README.md | frobnicate", evalcontract.Abstain},
	}
	root, home := fixture(t)
	reg := cmddesc.DefaultRegistry()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := Evaluate(evalcontract.Request{Command: tc.command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies())
			if resp.Decision != tc.want {
				t.Errorf("decision = %s, want %s (reason: %s)", resp.Decision, tc.want, resp.Reason)
			}
			if resp.Reason == "" {
				t.Error("empty reason")
			}
			compareGolden(t, filepath.Join("testdata", tc.name, "structural.mmd"), normalize(effectgraph.Mermaid(resp.Structural), root, home))
			compareGolden(t, filepath.Join("testdata", tc.name, "interpreted.mmd"), normalize(effectgraph.Mermaid(resp.Interpreted), root, home))
		})
	}
}

func normalize(s, root, home string) string {
	s = strings.ReplaceAll(s, root, "<ROOT>")
	return strings.ReplaceAll(s, home, "<HOME>")
}

func compareGolden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update)", path, err)
	}
	if string(want) != got {
		t.Errorf("golden mismatch %s\n--- want\n%s--- got\n%s", path, want, got)
	}
}

// TestUnparseableAbstains: a parse failure lands on Abstain with the parser's
// reason and empty graphs.
func TestUnparseableAbstains(t *testing.T) {
	root, _ := fixture(t)
	resp := Evaluate(evalcontract.Request{Command: "cat 'unterminated", CWD: root, ProjectRoot: root}, cmddesc.DefaultRegistry(), DefaultPolicies())
	if resp.Decision != evalcontract.Abstain {
		t.Fatalf("decision = %s, want abstain", resp.Decision)
	}
	if !strings.HasPrefix(resp.Reason, "unparseable") {
		t.Errorf("reason = %q", resp.Reason)
	}
	if len(resp.Interpreted.Nodes) != 0 || len(resp.Structural.Nodes) != 0 {
		t.Error("expected empty graphs")
	}
}

// TestProjectRootDetected: an empty ProjectRoot is detected from CWD.
func TestProjectRootDetected(t *testing.T) {
	root, _ := fixture(t)
	resp := Evaluate(evalcontract.Request{Command: "cat README.md", CWD: root}, cmddesc.DefaultRegistry(), DefaultPolicies())
	if resp.Decision != evalcontract.Approve {
		t.Fatalf("decision = %s, want approve (%s)", resp.Decision, resp.Reason)
	}
}

// TestForbiddenOutranksInsufficient: a no-schema leaf with a forbidden
// redirect is Reject, not Abstain — the builder-level effects are still judged.
// /nix/** is zoned read-only by string prefix, so it is portable. (A path under
// the fixture HOME would NOT do: both fixture dirs live under /tmp, which
// patheval zones read-write ahead of the ~/.claude rule.)
func TestForbiddenOutranksInsufficient(t *testing.T) {
	root, _ := fixture(t)
	resp := Evaluate(evalcontract.Request{Command: "frobnicate > /nix/store/x", CWD: root, ProjectRoot: root}, cmddesc.DefaultRegistry(), DefaultPolicies())
	if resp.Decision != evalcontract.Reject {
		t.Fatalf("decision = %s, want reject (%s)", resp.Decision, resp.Reason)
	}
}
