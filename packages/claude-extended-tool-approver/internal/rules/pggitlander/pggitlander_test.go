package pggitlander

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

func TestSurface(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	bare := t.TempDir()
	pe := patheval.New(root)
	cases := []struct {
		cmd  string
		cwd  string
		want bool
	}{
		// read-only verbs
		{"pg-git-lander status", root, true},
		{"pg-git-lander status --json --repo " + root, root, true},
		{"pg-git-lander log drain/x --tail 20 --repo " + root, root, true},
		{"pg-git-lander config check --json", root, true},
		{"pg-git-lander config check --repo " + root + " --json", root, true},
		{"pg-git-lander config check --repo " + root + " --file " + root + "/x.toml", root, true},
		{"pg-git-lander config check --repo /home/nobody/x", root, false},
		{"pg-git-lander status --abort", root, false},
		{"pg-git-lander status --repo /home/nobody/x", root, false},
		{"pg-git-lander status a b", root, false},
		{"pg-git-lander config check extra", root, false},
		// resume forms
		{"pg-git-lander land drain/x --abort --repo " + root, root, true},
		{"pg-git-lander land drain/x --quit --repo " + root, bare, true},
		{"pg-git-lander land --abort", bare, true},
		{"pg-git-lander rebase drain/x --abort --repo " + root, root, true},
		{"pg-git-lander land drain/x --continue --repo " + root, root, true},
		{"pg-git-lander land drain/x --skip --json --wait 20m", root, true},
		// --continue/--skip run repo hooks: need a trusted working directory
		{"pg-git-lander land drain/x --continue --repo " + root, bare, false},
		// starting an operation, or widening a resume, stays unapproved
		{"pg-git-lander land drain/x --repo " + root, root, false},
		{"pg-git-lander rebase drain/x --onto main", root, false},
		{"pg-git-lander land drain/x --abort --source-worktree " + root, root, false},
		{"pg-git-lander land drain/x --abort --continue", root, false},
		{"pg-git-lander land drain/x --continue --no-verify", root, false},
		{"pg-git-lander land drain/x --abort --no-verify", root, false},
		{"pg-git-lander land drain/x --abort --keep-source-branch", root, false},
		{"pg-git-lander land a b --abort", root, false},
		{"pg-git-lander land drain/x --abort --repo /home/nobody/x", root, false},
		{"pg-git-lander land drain/x --abort --repo", root, false},
		{"pg-git-lander land drain/x --abort --repo $R", root, false},
		{"pg-git-lander land drain/x --abort > out", root, false},
		{"pg-git-lander land $(echo x) --abort", root, false},
		{"/usr/bin/pg-git-lander land drain/x --abort", root, false},
		{"pg-git-lander", root, false},
		{"pg-git-lander frobnicate --abort", root, false},
		// an unrelated command with --abort is not newly allowed
		{"git rebase --abort", root, false},
		{"other-tool land drain/x --abort", root, false},
	}
	for _, c := range cases {
		ti, _ := json.Marshal(map[string]string{"command": c.cmd})
		res, err := New(pe).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: ti, CWD: c.cwd})
		got := err == nil && res.Decision == hookio.Approve
		if got != c.want {
			t.Errorf("%q (cwd %s) approve = %v, want %v", c.cmd, c.cwd, got, c.want)
		}
	}
}
