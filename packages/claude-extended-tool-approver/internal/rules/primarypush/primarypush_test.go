package primarypush

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// stubResolver implements primarycommit.PrimaryResolver for the rule under test.
type stubResolver struct {
	canonical      bool
	primary, cur   string
	canonicalErr   error
	priErr, curErr error
	gotDir         string
}

func (s *stubResolver) IsCanonical(dir string) (bool, error) {
	s.gotDir = dir
	return s.canonical, s.canonicalErr
}
func (s *stubResolver) PrimaryBranch(string) (string, error) { return s.primary, s.priErr }
func (s *stubResolver) CurrentBranch(string) (string, error) { return s.cur, s.curErr }

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// canonMain: canonical clone checked out on primary "main" (steady state, R-1).
func canonMain() *stubResolver { return &stubResolver{canonical: true, primary: "main", cur: "main"} }

// canonFeat: canonical clone currently on a feature branch.
func canonFeat() *stubResolver { return &stubResolver{canonical: true, primary: "main", cur: "feat"} }

func TestPrimaryPushRule(t *testing.T) {
	tests := []struct {
		name    string
		command string
		tool    string
		mode    string
		res     *stubResolver
		want    hookio.Decision
	}{
		// --- advances primary in an auto-approving session -> Reject ---
		{"bypass: bare push on primary", "git push", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: push origin (remote only) on primary", "git push origin", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: HEAD:main refspec", "git push origin HEAD:main", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: <local>:main refspec", "git push origin feat:main", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: worktree-branch:main refspec", "git push origin worktree-x:main", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: same-name main refspec", "git push origin main", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: refs/heads/main refspec", "git push origin HEAD:refs/heads/main", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: delete main refspec", "git push origin :main", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: set-upstream flag skipped, origin main -> primary", "git push --set-upstream origin main", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"auto: bare push on primary", "git push", "Bash", "auto", canonMain(), hookio.Reject},
		{"dontAsk: HEAD:main", "git push origin HEAD:main", "Bash", "dontAsk", canonMain(), hookio.Reject},
		{"bypass: compound commit && push on primary", "git commit -m x && git push", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: custom primary branch (trunk)", "git push origin HEAD:trunk", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "trunk", cur: "trunk"}, hookio.Reject},

		// --- review finding #1: same-name HEAD/@ source resolves to current branch ---
		{"bypass: push origin HEAD while ON primary", "git push origin HEAD", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: push origin @ while ON primary", "git push origin @", "Bash", "bypassPermissions", canonMain(), hookio.Reject},

		// --- review finding #2: --all / --mirror push primary regardless of current branch ---
		{"bypass: push --all from feature branch", "git push --all origin", "Bash", "bypassPermissions", canonFeat(), hookio.Reject},
		{"bypass: push --mirror from feature branch", "git push --mirror origin", "Bash", "bypassPermissions", canonFeat(), hookio.Reject},

		// --- review finding #3: injected push.default=matching pushes all same-name branches ---
		{"bypass: -c push.default=matching bare push from feature", "git -c push.default=matching push", "Bash", "bypassPermissions", canonFeat(), hookio.Reject},

		// --- review finding #4: dynamic remote side cannot be proven safe -> fail safe ---
		{"bypass: dynamic refspec remote ($BR)", "git push origin HEAD:$BR", "Bash", "bypassPermissions", canonFeat(), hookio.Reject},
		{"bypass: dynamic refspec remote (cmd subst)", "git push origin \"HEAD:$(printf main)\"", "Bash", "bypassPermissions", canonFeat(), hookio.Reject},

		// --- feature-branch pushes stay Approve (Abstain from this rule) ---
		{"bypass: push origin HEAD on feature branch", "git push origin HEAD", "Bash", "bypassPermissions", canonFeat(), hookio.Abstain},
		{"bypass: -c push.default=simple bare push from feature", "git -c push.default=simple push", "Bash", "bypassPermissions", canonFeat(), hookio.Abstain},
		{"bypass: push-option value 'main' not a refspec (feature push)", "git push origin --push-option main feat:feat", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: -o value 'main' not a refspec (feature push)", "git push -o main origin feat:feat", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: bare push on feature branch", "git push", "Bash", "bypassPermissions", canonFeat(), hookio.Abstain},
		{"bypass: feature refspec", "git push origin feat:feat", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: same-name feature push", "git push origin feat", "Bash", "bypassPermissions", canonFeat(), hookio.Abstain},
		{"bypass: HEAD:feat refspec", "git push origin HEAD:feat", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},

		// --- worktree / non-canonical stays Approve (worktree discipline is the control) ---
		{"bypass: push to main from linked worktree", "git push origin HEAD:main", "Bash", "bypassPermissions", &stubResolver{canonical: false, primary: "main", cur: "feat"}, hookio.Abstain},

		// --- interactive modes: no friction ---
		{"default: push to main (no friction)", "git push origin HEAD:main", "Bash", "default", canonMain(), hookio.Abstain},
		{"acceptEdits: push to main (does not auto-approve Bash)", "git push", "Bash", "acceptEdits", canonMain(), hookio.Abstain},
		{"plan: push to main", "git push origin HEAD:main", "Bash", "plan", canonMain(), hookio.Abstain},
		{"empty mode: push to main", "git push", "Bash", "", canonMain(), hookio.Abstain},

		// --- non-push / non-git / non-bash / errors: Abstain ---
		{"bypass: non-push git (fetch)", "git fetch origin", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: non-git bash", "ls -la", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: non-bash tool", "", "Read", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: detached HEAD, bare push", "git push", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: ""}, hookio.Abstain},
		{"bypass: resolver error (fail-open)", "git push origin HEAD:main", "Bash", "bypassPermissions", &stubResolver{canonicalErr: errors.New("x")}, hookio.Abstain},
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

func TestPrimaryPush_DashC_EffectiveDir(t *testing.T) {
	cases := []struct{ cmd, cwd, wantDir string }{
		{"git -C /abs/repo push", "/cwd", "/abs/repo"},
		{"git -C sub push", "/cwd", "/cwd/sub"},
		{"git -C a -C b push", "/cwd", "/cwd/a/b"},
		{"git push", "/cwd", "/cwd"},
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

func TestPrimaryPush_NilResolver(t *testing.T) {
	got := New(nil).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON("git push origin HEAD:main"), CWD: "/repo", PermissionMode: "bypassPermissions"}).Decision
	if got != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain", got)
	}
}
