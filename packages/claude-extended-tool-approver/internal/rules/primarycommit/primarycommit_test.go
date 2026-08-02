package primarycommit

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// stubResolver implements PrimaryResolver. pushDefault/aliases (and their optional
// error fields) drive the tc-2phi8 alias cases; their zero values mean "no alias /
// unset", so existing constructors keep their pre-tc-2phi8 behavior.
type stubResolver struct {
	canonical      bool
	primary, cur   string
	canonicalErr   error
	priErr, curErr error
	pushDefault    string
	aliases        map[string]string
	pdErr, aliErr  error
	gotDir         string
}

func (s *stubResolver) IsCanonical(dir string) (bool, error) {
	s.gotDir = dir
	return s.canonical, s.canonicalErr
}
func (s *stubResolver) PrimaryBranch(string) (string, error) { return s.primary, s.priErr }
func (s *stubResolver) CurrentBranch(string) (string, error) { return s.cur, s.curErr }
func (s *stubResolver) PushDefault(string) (string, error)   { return s.pushDefault, s.pdErr }
func (s *stubResolver) Aliases(string) (map[string]string, error) {
	return s.aliases, s.aliErr
}

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func canonMain() *stubResolver { return &stubResolver{canonical: true, primary: "main", cur: "main"} }

func TestPrimaryCommitRule(t *testing.T) {
	tests := []struct {
		name    string
		command string
		tool    string
		mode    string
		res     *stubResolver
		want    hookio.Decision
	}{
		{"bypass: commit on primary", "git commit -m x", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: commit --amend on primary", "git commit --amend", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"auto: commit on primary", "git commit -m x", "Bash", "auto", canonMain(), hookio.Reject},
		{"dontAsk: commit on primary", "git commit -m x", "Bash", "dontAsk", canonMain(), hookio.Reject},
		{"default: commit on primary (no friction)", "git commit -m x", "Bash", "default", canonMain(), hookio.Abstain},
		{"acceptEdits: commit on primary (does not auto-approve Bash)", "git commit -m x", "Bash", "acceptEdits", canonMain(), hookio.Abstain},
		{"plan: commit on primary", "git commit -m x", "Bash", "plan", canonMain(), hookio.Abstain},
		{"empty mode: commit on primary", "git commit -m x", "Bash", "", canonMain(), hookio.Abstain},
		{"bypass: off primary", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat"}, hookio.Abstain},
		{"bypass: linked worktree", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonical: false, primary: "main", cur: "main"}, hookio.Abstain},
		{"bypass: detached", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: ""}, hookio.Abstain},
		{"bypass: non-commit git", "git status", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: commit-tree", "git commit-tree abc", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: non-git bash", "ls -la", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: non-bash tool", "", "Read", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: resolver error (fail-open)", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonicalErr: errors.New("x")}, hookio.Abstain},
		{"bypass: compound commit && push", "git commit -m x && git push", "Bash", "bypassPermissions", canonMain(), hookio.Reject},

		// --- tc-2phi8: alias hides the commit subcommand -> expand and gate ---
		// injected `-c alias.ci='commit -am x' ci` on the canonical primary.
		{"bypass: injected alias to commit on primary", "git -c alias.ci='commit -am x' ci", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		// config-defined alias (via the resolver), bare `git ci`, on the canonical primary.
		{"bypass: config alias to commit on primary", "git ci", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "commit -am x"}}, hookio.Reject},
		// alias name lookup is case-insensitive.
		{"bypass: injected alias case-insensitive name", "git -c alias.CI='commit -am x' ci", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		// injected `-c` beats a config alias of the same name.
		{"bypass: injected alias overrides config (to commit)", "git -c alias.ci='commit -am x' ci", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "status"}}, hookio.Reject},
		// same alias but OFF primary -> Abstain.
		{"bypass: config alias to commit off primary", "git ci", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"ci": "commit -am x"}}, hookio.Abstain},
		// harmless alias (not a commit) -> Abstain.
		{"bypass: harmless alias (st=status)", "git st", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"st": "status"}}, hookio.Abstain},
		// alias to commit-tree is NOT a commit -> Abstain (matches the bare commit-tree case).
		{"bypass: alias to commit-tree", "git ct", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ct": "commit-tree abc"}}, hookio.Abstain},
		// interactive mode: an alias to a primary commit must NOT prompt.
		{"default: config alias to commit on primary (no friction)", "git ci", "Bash", "default", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "commit -am x"}}, hookio.Abstain},
		// shell alias (`!…`) body re-parsed and its git commands re-checked.
		{"bypass: shell alias committing on primary", "git ci", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "!git commit -am x"}}, hookio.Reject},
		{"bypass: shell alias not a commit (echo)", "git ci", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "!echo hi"}}, hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.res)
			in := &hookio.HookInput{ToolName: tt.tool, ToolInput: mustJSON(tt.command), CWD: "/repo", PermissionMode: tt.mode}
			if got := r.Evaluate(in).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrimaryCommit_DashC_EffectiveDir(t *testing.T) {
	cases := []struct{ cmd, cwd, wantDir string }{
		{"git -C /abs/repo commit", "/cwd", "/abs/repo"},
		{"git -C sub commit", "/cwd", "/cwd/sub"},
		{"git -C a -C b commit", "/cwd", "/cwd/a/b"},
		{"git commit", "/cwd", "/cwd"},
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			s := canonMain()
			New(s).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(c.cmd), CWD: c.cwd, PermissionMode: "bypassPermissions"})
			if s.gotDir != c.wantDir {
				t.Errorf("effective dir = %q, want %q", s.gotDir, c.wantDir)
			}
		})
	}
}

func TestPrimaryCommit_NilResolver(t *testing.T) {
	got := New(nil).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON("git commit"), CWD: "/repo", PermissionMode: "bypassPermissions"}).Decision
	if got != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain", got)
	}
}
