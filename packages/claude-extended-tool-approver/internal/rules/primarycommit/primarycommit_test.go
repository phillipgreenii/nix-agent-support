package primarycommit

import (
	"encoding/json"
	"errors"
	"strings"
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
		{"default: commit on primary (no friction)", "git commit -m x", "Bash", "default", canonMain(), hookio.NoOpinion},
		{"acceptEdits: commit on primary (does not auto-approve Bash)", "git commit -m x", "Bash", "acceptEdits", canonMain(), hookio.NoOpinion},
		{"plan: commit on primary", "git commit -m x", "Bash", "plan", canonMain(), hookio.NoOpinion},
		{"empty mode: commit on primary", "git commit -m x", "Bash", "", canonMain(), hookio.NoOpinion},
		{"bypass: off primary", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat"}, hookio.NoOpinion},
		{"bypass: linked worktree", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonical: false, primary: "main", cur: "main"}, hookio.NoOpinion},
		{"bypass: detached", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: ""}, hookio.NoOpinion},
		{"bypass: non-commit git", "git status", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: commit-tree", "git commit-tree abc", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: non-git bash", "ls -la", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: non-bash tool", "", "Read", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: resolver error (fail-open)", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonicalErr: errors.New("x")}, hookio.NoOpinion},
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
		{"bypass: config alias to commit off primary", "git ci", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat", aliases: map[string]string{"ci": "commit -am x"}}, hookio.NoOpinion},
		// harmless alias (not a commit) -> Abstain.
		{"bypass: harmless alias (st=status)", "git st", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"st": "status"}}, hookio.NoOpinion},
		// alias to commit-tree is NOT a commit -> Abstain (matches the bare commit-tree case).
		{"bypass: alias to commit-tree", "git ct", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ct": "commit-tree abc"}}, hookio.NoOpinion},
		// interactive mode: an alias to a primary commit must NOT prompt.
		{"default: config alias to commit on primary (no friction)", "git ci", "Bash", "default", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "commit -am x"}}, hookio.NoOpinion},
		// shell alias (`!…`) body re-parsed and its git commands re-checked.
		{"bypass: shell alias committing on primary", "git ci", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "!git commit -am x"}}, hookio.Reject},
		{"bypass: shell alias not a commit (echo)", "git ci", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "!echo hi"}}, hookio.NoOpinion},

		// --- pg2-h2npt: an UNRESOLVED target directory is decisive in EVERY mode ---
		// The stub is canonMain(), i.e. it would answer "canonical, on primary" for any
		// directory it is asked about — so a case that reached the resolver would look
		// exactly like the old false deny. These assert the resolver is never consulted.
		//
		// Auto-approving modes keep a Reject (an Ask is silently accepted there, and the
		// old behaviour was already a Reject — this change must not be more permissive).
		{"bypass: git -C $VAR commit (unresolved)", "git -C $WT commit -m x", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"auto: git -C $VAR commit (unresolved)", "git -C $WT commit -m x", "Bash", "auto", canonMain(), hookio.Reject},
		{"dontAsk: git -C ${VAR} commit (unresolved)", "git -C ${WT} commit -m x", "Bash", "dontAsk", canonMain(), hookio.Reject},
		// Interactive modes get Ask, NOT the fail-open not-applicable: the generic git
		// rule behind this one approves a plain `git commit`, so not-applicable would
		// let an unresolvable target reach Approve.
		{"default: git -C $VAR commit asks (cannot reach approve)", "git -C $WT commit -m x", "Bash", "default", canonMain(), hookio.Ask},
		{"plan: git -C $VAR commit asks", "git -C $WT commit -m x", "Bash", "plan", canonMain(), hookio.Ask},
		{"empty mode: git -C $VAR commit asks", "git -C $WT commit -m x", "Bash", "", canonMain(), hookio.Ask},
		// Every expansion that can reach a path, not just `$`.
		{"default: git -C $(pwd) commit asks", "git -C $(pwd) commit", "Bash", "default", canonMain(), hookio.Ask},
		{"default: git -C backtick commit asks", "git -C `pwd` commit", "Bash", "default", canonMain(), hookio.Ask},
		{"default: git -C glob commit asks", "git -C /repo/.worktrees/* commit", "Bash", "default", canonMain(), hookio.Ask},
		{"default: git -C tilde commit asks", "git -C ~/repo commit", "Bash", "default", canonMain(), hookio.Ask},
		// The check is SCOPED to a commit: an unresolved `-C` on any other subcommand is
		// none of this rule's business and must stay not-applicable.
		{"bypass: git -C $VAR status untouched", "git -C $WT status", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		{"bypass: git -C $VAR commit-tree untouched", "git -C $WT commit-tree abc", "Bash", "bypassPermissions", canonMain(), hookio.NoOpinion},
		// An unresolved dir reached through an ALIAS is still caught.
		{"default: alias to commit with unresolved -C asks", "git -C $WT ci", "Bash", "default", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "commit -am x"}}, hookio.Ask},
		{"default: shell alias to commit with unresolved -C asks", "git -C $WT ci", "Bash", "default", &stubResolver{canonical: true, primary: "main", cur: "main", aliases: map[string]string{"ci": "!git commit -am x"}}, hookio.Ask},
		// A LITERAL relative `-C` is resolved, not unresolved: it joins onto the cwd
		// deterministically, so it keeps its pre-change verdict.
		{"bypass: literal relative -C still resolves", "git -C sub commit", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
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
			// Return values discarded on purpose: this case asserts the resolver saw the
			// right effective dir (s.gotDir), not the verdict.
			_, _ = New(s).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(c.cmd), CWD: c.cwd, PermissionMode: "bypassPermissions"})
			if s.gotDir != c.wantDir {
				t.Errorf("effective dir = %q, want %q", s.gotDir, c.wantDir)
			}
		})
	}
}

// TestPrimaryCommit_UnresolvedDirNeverApproves is the pg2-h2npt FAIL-CLOSED guard. It
// is deliberately phrased as "never Approve" rather than as an expected decision per
// mode: the point is not which non-approving verdict comes out but that NO permission
// mode, and no spelling of an unresolvable directory, can produce one that a later rule
// (or auto-approve mode on an empty verdict) turns into an approval. A verdict of
// Approve OR NoOpinion fails it — NoOpinion emits `{}`, which an auto-approving session
// accepts, so it is an approval by another route.
func TestPrimaryCommit_UnresolvedDirNeverApproves(t *testing.T) {
	commands := []string{
		"git -C $WT commit -m x",
		"git -C ${WT} commit -m x",
		"git -C \"$WT\" commit -m x",
		"git -C $(pwd) commit",
		"git -C `git rev-parse --show-toplevel` commit",
		"git -C $WT/nested commit",
		"git -C /repo/.worktrees/*/ commit",
		"git -C ~/repo commit",
		"git -C ~ commit",
		"git commit -m x", // via an unresolved CWD, below
	}
	// Both the -C route and the CWD route (the shape a `cd $WT` leaves behind once the
	// engine has joined the unexpanded token into the running cwd).
	cwds := []string{"/repo", "/repo/$WT", "/repo/$(pwd)"}
	modes := []string{"bypassPermissions", "auto", "dontAsk", "default", "plan", "acceptEdits", ""}
	for _, cwd := range cwds {
		for _, cmd := range commands {
			if cwd == "/repo" && cmd == "git commit -m x" {
				continue // fully resolved: that is the ordinary primary-commit case
			}
			for _, mode := range modes {
				name := mode + " " + cwd + " " + cmd
				t.Run(name, func(t *testing.T) {
					// canonMain() answers "canonical, on primary" for ANY directory, so a
					// case that leaked through to the resolver would be indistinguishable
					// from the old false deny — and a case that reached the git rule would
					// be an approval. Neither is allowed.
					res := canonMain()
					in := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(cmd), CWD: cwd, PermissionMode: mode}
					got := hookio.Verdict(New(res).Evaluate(in))
					if got.Decision == hookio.Approve || got.Decision == hookio.NoOpinion {
						t.Fatalf("Decision = %v (reason %q); an unresolvable directory MUST NOT reach Approve or an empty verdict", got.Decision, got.Reason)
					}
					if res.gotDir != "" {
						t.Errorf("resolver was consulted with %q; an unresolvable directory MUST NOT be resolved at all", res.gotDir)
					}
				})
			}
		}
	}
}

// TestPrimaryCommit_ReasonNamesActualCause asserts the DIAGNOSIS half of pg2-h2npt: the
// reason must let an agent act. The old text said only "commit on the primary branch …
// (R-6)", which for a mis-resolved directory was actively misleading — it named a branch
// problem the agent did not have and never named the directory the rule had judged.
func TestPrimaryCommit_ReasonNamesActualCause(t *testing.T) {
	t.Run("unresolved names the token, its source, and denies the primary reading", func(t *testing.T) {
		got := hookio.Verdict(New(canonMain()).Evaluate(&hookio.HookInput{
			ToolName: "Bash", ToolInput: mustJSON("git -C $WT commit -m x"), CWD: "/repo", PermissionMode: "default",
		}))
		for _, want := range []string{
			"cannot determine which repository or branch",
			"$WT",
			"git -C $WT",
			"NOT a finding that you are on a primary branch",
			"literally",
		} {
			if !strings.Contains(got.Reason, want) {
				t.Errorf("reason %q does not mention %q", got.Reason, want)
			}
		}
		// It MUST NOT read as the primary-branch finding it is not.
		if strings.Contains(got.Reason, "refusing this commit") {
			t.Errorf("unresolved reason reads as the primary-branch finding: %q", got.Reason)
		}
	})

	t.Run("primary names the directory evaluated and how it was chosen", func(t *testing.T) {
		got := hookio.Verdict(New(canonMain()).Evaluate(&hookio.HookInput{
			ToolName: "Bash", ToolInput: mustJSON("git commit -m x"), CWD: "/repo/canonical", PermissionMode: "bypassPermissions",
		}))
		for _, want := range []string{
			"Directory evaluated: /repo/canonical",
			"no `git -C` given",
			"CANONICAL clone",
			"\"main\"",
			"R-6",
		} {
			if !strings.Contains(got.Reason, want) {
				t.Errorf("reason %q does not mention %q", got.Reason, want)
			}
		}
	})

	t.Run("primary names the -C option when one chose the directory", func(t *testing.T) {
		got := hookio.Verdict(New(canonMain()).Evaluate(&hookio.HookInput{
			ToolName: "Bash", ToolInput: mustJSON("git -C /other/clone commit -m x"), CWD: "/repo", PermissionMode: "bypassPermissions",
		}))
		for _, want := range []string{"Directory evaluated: /other/clone", "`git -C /other/clone` option"} {
			if !strings.Contains(got.Reason, want) {
				t.Errorf("reason %q does not mention %q", got.Reason, want)
			}
		}
	})
}

// TestUnresolvableToken pins the resolution TEST itself — which spellings are literal
// and which are not — independently of the rule that consumes it, so a widening of the
// marker set has to be stated as a change here.
func TestUnresolvableToken(t *testing.T) {
	unresolved := map[string]string{
		"/repo/$WT":        "$WT",
		"/repo/${WT}":      "${WT}",
		"$WT":              "$WT",
		"/repo/$(pwd)":     "$(pwd)",
		"/repo/`pwd`":      "`pwd`",
		"/repo/.wt/*":      "*",
		"/repo/wt-?":       "wt-?",
		"~/repo":           "~",
		"~":                "~",
		"/repo/$WT/nested": "$WT",
	}
	for p, wantToken := range unresolved {
		if got := unresolvableToken(p); got != wantToken {
			t.Errorf("unresolvableToken(%q) = %q, want %q", p, got, wantToken)
		}
	}
	// Literal paths — absolute, relative, dotted, spaced, and a name holding the
	// bracket characters deliberately left OUT of the marker set.
	for _, p := range []string{
		"/repo", "/repo/.worktrees/feat", "sub", "./sub", "../sib", "/repo/a b/c",
		"/repo/name[1]", "/repo/a~b", "", "/",
	} {
		if got := unresolvableToken(p); got != "" {
			t.Errorf("unresolvableToken(%q) = %q, want \"\" (literal)", p, got)
		}
	}
}

func TestPrimaryCommit_NilResolver(t *testing.T) {
	got := hookio.Verdict(New(nil).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON("git commit"), CWD: "/repo", PermissionMode: "bypassPermissions"})).Decision
	if got != hookio.NoOpinion {
		t.Errorf("Decision = %v, want Abstain", got)
	}
}
