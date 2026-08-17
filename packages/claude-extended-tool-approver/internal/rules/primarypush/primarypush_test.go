package primarypush

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// stubResolver implements primarycommit.PrimaryResolver for the rule under test.
// pushDefault/aliases (and their optional error fields) drive the tc-2phi8 alias and
// ambient-push.default cases; their zero values (pushDefault "", aliases nil) mean
// "no alias / unset", i.e. the pre-tc-2phi8 behavior, so existing constructors are
// unaffected.
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
		// auto PROMPTS on an Ask rather than silently accepting it (operator-confirmed
		// 2026-08-14/2026-08-15), so it moved out of primarycommit.AutoApprovingModes
		// and no longer gets the hard Reject — but it is still in
		// primarycommit.GatedModes (nobody is necessarily watching an unattended auto
		// session), so it gets Ask, NOT the R-6 trust interactive/default sessions get.
		// A NotApplicable/NoOpinion here would let this reach Approve via the generic
		// git rule with NO prompt at all, which is NOT the intended correction
		// (measured and rejected during pg2-68w11: a naive "just remove auto from the
		// map" moved corpus rows reject->approve).
		{"auto: bare push on primary now asks (not hard-denied, not trusted either)", "git push", "Bash", "auto", canonMain(), hookio.Ask},
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
		{"bypass: push origin HEAD on feature branch", "git push origin HEAD", "Bash", "bypassPermissions", canonFeat(), hookio.NoOpinion},
		{"bypass: -c push.default=simple bare push from feature", "git -c push.default=simple push", "Bash", "bypassPermissions", canonFeat(), hookio.NoOpinion},
		{"bypass: push-option value 'main' not a refspec (feature push)", "git push origin --push-option main feat:feat", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: -o value 'main' not a refspec (feature push)", "git push -o main origin feat:feat", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: bare push on feature branch", "git push", "Bash", "bypassPermissions", canonFeat(), hookio.NoOpinion},
		{"bypass: feature refspec", "git push origin feat:feat", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: same-name feature push", "git push origin feat", "Bash", "bypassPermissions", canonFeat(), hookio.NoOpinion},
		{"bypass: HEAD:feat refspec", "git push origin HEAD:feat", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},

		// --- tc-2phi8: alias hides the push subcommand -> expand and gate ---
		// injected `-c alias.p='push origin HEAD:main' p` from a feature branch: the alias
		// body pushes to primary, so the hidden push is caught.
		{"bypass: injected alias to primary push", "git -c alias.p='push origin HEAD:main' p", "Bash", "bypassPermissions", canonFeat(), hookio.Reject},
		// config-defined alias (via the resolver) to a primary push, bare `git p`.
		{"bypass: config alias to primary push", "git p", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"p": "push origin HEAD:main"}}, hookio.Reject},
		// alias name lookup is case-insensitive (git config keys are); injected `alias.P`.
		{"bypass: injected alias case-insensitive name", "git -c alias.P='push origin HEAD:main' p", "Bash", "bypassPermissions", canonFeat(), hookio.Reject},
		// injected `-c` beats a config alias of the same name.
		{"bypass: injected alias overrides config (to primary)", "git -c alias.p='push origin HEAD:main' p", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"p": "status"}}, hookio.Reject},
		// alias to a FEATURE push -> not primary -> Abstain.
		{"bypass: config alias to feature push", "git p", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"p": "push origin HEAD:feat"}}, hookio.NoOpinion},
		// harmless alias that is not a push at all -> Abstain.
		{"bypass: harmless alias (st=status)", "git st", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"st": "status"}}, hookio.NoOpinion},
		// interactive mode: an alias to a primary push must NOT prompt every push.
		{"default: config alias to primary push (no friction)", "git p", "Bash", "default", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"p": "push origin HEAD:main"}}, hookio.NoOpinion},

		// --- tc-2phi8: ambient push.default=matching (set in git config, not injected) ---
		// bare push from a feature branch advances primary via matching -> Reject.
		{"bypass: ambient push.default=matching bare push from feature", "git push", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", pushDefault: "matching"}, hookio.Reject},
		// push.default=simple leaves a feature bare push harmless -> Abstain.
		{"bypass: ambient push.default=simple bare push from feature", "git push", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", pushDefault: "simple"}, hookio.NoOpinion},
		// value is compared case-insensitively.
		{"bypass: ambient push.default=Matching (case-insensitive)", "git push", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", pushDefault: "Matching"}, hookio.Reject},
		// interactive mode: ambient matching must NOT prompt every push.
		{"default: ambient push.default=matching (no friction)", "git push", "Bash", "default", &stubResolver{canonical: true, primary: "main", cur: "feat", pushDefault: "matching"}, hookio.NoOpinion},

		// --- tc-2phi8: shell alias (`!…`) body re-parsed and its git commands re-checked ---
		{"bypass: shell alias pushing to primary", "git p", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"p": "!git push origin HEAD:main"}}, hookio.Reject},
		{"bypass: shell alias not a push (echo)", "git p", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"p": "!echo hi"}}, hookio.NoOpinion},
		{"bypass: shell alias pushing to feature", "git p", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"p": "!git push origin HEAD:feat"}}, hookio.NoOpinion},
		{"default: shell alias pushing to primary (no friction)", "git p", "Bash", "default", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"p": "!git push origin HEAD:main"}}, hookio.NoOpinion},

		// --- worktree / non-canonical stays Approve (worktree discipline is the control) ---
		{"bypass: push to main from linked worktree", "git push origin HEAD:main", "Bash", "bypassPermissions", &stubResolver{canonical: false, primary: "main", cur: "feat"}, hookio.NoOpinion},

		// --- interactive modes: no friction ---
		{"default: push to main (no friction)", "git push origin HEAD:main", "Bash", "default", canonMain(), hookio.NoOpinion},
		{"acceptEdits: push to main (does not auto-approve Bash)", "git push", "Bash", "acceptEdits", canonMain(), hookio.NoOpinion},
		{"plan: push to main", "git push origin HEAD:main", "Bash", "plan", canonMain(), hookio.NoOpinion},
		{"empty mode: push to main", "git push", "Bash", "", canonMain(), hookio.NoOpinion},

		// --- non-push / non-git / non-bash / errors: Abstain ---
		{"bypass: non-push git (fetch)", "git fetch origin", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: non-git bash", "ls -la", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: non-bash tool", "", "Read", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: detached HEAD, bare push", "git push", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: ""}, hookio.NoOpinion},
		{"bypass: resolver error (fail-open)", "git push origin HEAD:main", "Bash", "bypassPermissions", &stubResolver{canonicalErr: errors.New("x")}, hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.res)
			in := &hookio.HookInput{ToolName: tt.tool, ToolInput: mustJSON(tt.command), CWD: "/repo", PermissionMode: tt.mode}
			if got := hookio.Verdict(r.Evaluate(in)).Decision; got != tt.want {
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
			// Return values discarded on purpose: this case asserts the resolver saw the
			// right effective dir (s.gotDir), not the verdict.
			_, _ = New(s).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(c.cmd), CWD: c.cwd, PermissionMode: "bypassPermissions"})
			if s.gotDir != c.wantDir {
				t.Errorf("effective dir = %q, want %q", s.gotDir, c.wantDir)
			}
		})
	}
}

func TestPrimaryPush_NilResolver(t *testing.T) {
	got := hookio.Verdict(New(nil).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON("git push origin HEAD:main"), CWD: "/repo", PermissionMode: "bypassPermissions"})).Decision
	if got != hookio.NoOpinion {
		t.Errorf("Decision = %v, want Abstain", got)
	}
}
