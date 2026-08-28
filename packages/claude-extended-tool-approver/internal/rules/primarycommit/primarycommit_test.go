package primarycommit

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
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
		// auto PROMPTS on an Ask rather than silently accepting it (operator-confirmed
		// 2026-08-14/2026-08-15), so it moved out of AutoApprovingModes and no longer
		// gets the hard Reject — but it is still in GatedModes (nobody is necessarily
		// watching an unattended auto session), so it gets Ask, NOT the R-6 trust
		// interactive/default sessions get. A NotApplicable/NoOpinion here would let
		// this reach Approve via the generic git rule with NO prompt at all, which is
		// NOT the intended correction (measured and rejected during pg2-68w11: a naive
		// "just remove auto from the map" moved 153 corpus rows reject->approve).
		{"auto: commit on primary now asks (not hard-denied, not trusted either)", "git commit -m x", "Bash", "auto", canonMain(), hookio.Ask},
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
		// Modes that silently accept an Ask (AutoApprovingModes) keep a Reject here — an
		// Ask would never be seen there, and the old behaviour was already a Reject, so
		// this must not be more permissive. `auto` is NOT one of them (it prompts on an
		// Ask, operator-confirmed 2026-08-14/2026-08-15), so it now gets the same Ask an
		// interactive session gets — LESS restrictive than before, which is the intended
		// correction, not a regression: the old Reject there rested on the same wrong
		// "auto silently accepts" premise this bead fixes.
		{"bypass: git -C $VAR commit (unresolved)", "git -C $WT commit -m x", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"auto: git -C $VAR commit (unresolved) now asks", "git -C $WT commit -m x", "Bash", "auto", canonMain(), hookio.Ask},
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

// TestPrimaryCommit_MissingDir covers pg2-5adzj: a directory the command text itself
// established (an explicit `-C`, or a preceding `cd`/`pushd` leaf anywhere in the same
// compound) that does not exist on disk. Signalled here by a stubResolver returning
// ErrDirNotExist from IsCanonical — exactly what FileResolver returns for real — this
// asserts the RULE's handling of that signal without touching a real filesystem.
func TestPrimaryCommit_MissingDir(t *testing.T) {
	missing := func() *stubResolver { return &stubResolver{canonicalErr: ErrDirNotExist} }

	tests := []struct {
		name    string
		command string
		mode    string
		root    string // RootExpression; "" leaves it unset (falls back to command)
		want    hookio.Decision
	}{
		// An explicit `-C` on THIS invocation names the directory, so ErrDirNotExist is
		// decisive even with no cd/pushd anywhere.
		{"bypass: -C to a missing dir is denied", "git -C /gone commit -m x", "bypassPermissions", "", hookio.Reject},
		{"default: -C to a missing dir asks", "git -C /gone commit -m x", "default", "", hookio.Ask},
		{"auto: -C to a missing dir asks", "git -C /gone commit -m x", "auto", "", hookio.Ask},

		// A `cd` earlier in the SAME root expression also names the directory, even
		// though THIS leaf (the only text primarycommit.go sees under the engine) is
		// just the bare commit with no `-C` of its own.
		{
			"default: cd earlier in the compound + missing dir asks", "git commit -m x", "default",
			`cd /gone && git commit -m x`, hookio.Ask,
		},
		{
			"bypass: pushd earlier in the compound + missing dir is denied", "git commit -m x", "bypassPermissions",
			`pushd /gone && git commit -m x`, hookio.Reject,
		},

		// No `-C` and no `cd`/`pushd` anywhere in scope: a bare, un-redirected CWD.
		// This is the SESSION's own reported directory (or, in these tests, a stand-in
		// for it) — not something the command text established — so a stubResolver
		// reporting it missing must NOT be decisive: fail-open, matching every other
		// resolver-error case (TestPrimaryCommitRule's "resolver error (fail-open)").
		{"bypass: bare CWD reported missing stays fail-open", "git commit -m x", "bypassPermissions", "", hookio.NoOpinion},
		{"default: bare CWD reported missing stays fail-open", "git commit -m x", "default", "", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &hookio.HookInput{
				ToolName: "Bash", ToolInput: mustJSON(tt.command), CWD: "/repo",
				PermissionMode: tt.mode, RootExpression: tt.root,
			}
			if got := hookio.Verdict(New(missing()).Evaluate(in)).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPrimaryCommit_MissingDirReason pins the DIAGNOSIS half: the missing-directory
// reason must be distinct from BOTH the primary-branch finding (it is not one) and the
// unresolved-token finding (the path is fully literal, not "expanded by the shell").
func TestPrimaryCommit_MissingDirReason(t *testing.T) {
	got := hookio.Verdict(New(&stubResolver{canonicalErr: ErrDirNotExist}).Evaluate(&hookio.HookInput{
		ToolName: "Bash", ToolInput: mustJSON("git -C /gone commit -m x"), CWD: "/repo", PermissionMode: "default",
	}))
	for _, want := range []string{
		"cannot verify this commit's target",
		"/gone",
		"does not exist",
		"NOT a finding that you are on the primary branch",
	} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason %q does not mention %q", got.Reason, want)
		}
	}
	for _, mustNot := range []string{"refusing this commit", "CANONICAL clone", "expanded by the shell"} {
		if strings.Contains(got.Reason, mustNot) {
			t.Errorf("missing-dir reason %q wrongly reads as a different finding (%q)", got.Reason, mustNot)
		}
	}
}

// TestDirNamedByCommand pins the gate directly: which shapes count as the command TEXT
// having established a directory (worth a fail-safe verdict on ErrDirNotExist) versus a
// bare, un-redirected CWD (must stay fail-open).
func TestDirNamedByCommand(t *testing.T) {
	tests := []struct {
		name   string
		chdirs []string
		scope  string
		want   bool
	}{
		{"explicit -C, no scope text", []string{"/abs/worktree"}, "", true},
		{"cd leaf in scope, no -C", nil, "cd /abs/worktree && git commit -m x", true},
		{"pushd leaf in scope, no -C", nil, "pushd /abs/worktree && git commit -m x", true},
		{"cd earlier, semicolon-separated", nil, "W=/abs/worktree; cd \"$W\" && git commit -m x", true},
		{"bare commit, no -C, no cd anywhere", nil, "git commit -m x", false},
		{"unrelated cd elsewhere is still enough (coarse by design)", nil, "cd /a; git status; git commit -m x", true},
		{"empty scope, no -C", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// GUARD 3 (I7, pg2-x9452): dirNamedByCommand now takes the root
			// expression's ALREADY-PARSED leaves rather than re-parsing scope
			// text itself — a direct caller (this test) parses scope once,
			// here, exactly as primarycommit.go's own Evaluate does for a
			// non-engine caller with no threaded ParsedRoot.
			rootLeaves := cmdparse.Parse(tt.scope)
			if got := dirNamedByCommand(tt.chdirs, rootLeaves); got != tt.want {
				t.Errorf("dirNamedByCommand(%v, %q) = %v, want %v", tt.chdirs, tt.scope, got, tt.want)
			}
		})
	}
}

// TestPrimaryCommit_SelfCreatedDir covers pg2-70g51's literal-but-missing-dir shape
// (pg2-69i0d's row 426677): a `mkdir`/`git init` leaf earlier in the SAME command
// creates the exact directory a `git commit` targets, currently ABSENT from disk
// (simulated here, as TestPrimaryCommit_MissingDir does, via a stubResolver returning
// ErrDirNotExist unconditionally). The positive rows are "&&"-chained end to end, so
// the creating leaf's success is REQUIRED for the commit to run at all; the negative
// rows swap in a ";" (or a newline) between the creator and the rest, reproducing row
// 426677's own shape exactly — a FAILED creator there falls through to a commit that
// could land wherever the shell already was, so those rows MUST keep the pre-existing
// fail-safe verdict.
//
// Every row uses `-C` on the commit leaf, DELIBERATELY, never a bare `cd`: this rule
// (unlike the engine) does not itself model a `cd`'s effect on a LATER leaf's cwd — the
// engine's per-leaf cwd advancement is a different layer entirely (dirresolve.go's own
// DIRECTORY RESOLUTION comment) — so a raw Evaluate() call here always resolves every
// leaf against the SAME input.CWD regardless of an earlier `cd`. The "cd" spelling of
// both the positive and negative case IS covered, with the real engine's cwd modeling
// in play, by TestIntegration_PrimaryCommitSelfCreatedDir (internal/engine).
func TestPrimaryCommit_SelfCreatedDir(t *testing.T) {
	missing := func() *stubResolver { return &stubResolver{canonicalErr: ErrDirNotExist} }

	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{
			name:    "POSITIVE: mkdir && -C into it && commit",
			command: `mkdir -p /scratch/wt && git -C /scratch/wt init -q && git -C /scratch/wt commit -q -m seed`,
			want:    hookio.NoOpinion,
		},
		{
			name:    "POSITIVE: git init <dir> (no mkdir) && -C into it && commit",
			command: `git init -q /scratch/wt && git -C /scratch/wt commit -q -m seed`,
			want:    hookio.NoOpinion,
		},
		{
			name: "NEGATIVE: row 426677's own shape — ';'/newline separated, mkdir " +
				"then a SEPARATE line",
			command: "mkdir -p /scratch/wt; git -C /scratch/wt init -q\n" +
				"git -C /scratch/wt commit -q -m seed",
			want: hookio.Ask,
		},
		{
			name:    "NEGATIVE: no creating leaf at all, just an unrelated -C",
			command: `git -C /scratch/wt status; git -C /scratch/wt commit -q -m seed`,
			want:    hookio.Ask,
		},
		{
			name:    "NEGATIVE: mkdir targets a DIFFERENT directory",
			command: `mkdir -p /scratch/other && git -C /scratch/wt init -q && git -C /scratch/wt commit -q -m seed`,
			want:    hookio.Ask,
		},
		{
			// NO ";"/newline anywhere — this is ONE *syntax.Stmt, entirely "&&"/"||"
			// (left-associative, same precedence): `((mkdir || true) && init) &&
			// commit`. Without the "||"-anywhere-poisons-the-whole-statement rule
			// (cmdparse's AndChainID doc, containsOrStmt), a naive "just continue
			// through every '&&'" scheme would still relate mkdir to the commit here
			// — this row is what catches THAT mistake, not the ";" one above.
			name:    "NEGATIVE: an '||' anywhere in the statement disables tracking entirely",
			command: `mkdir -p /scratch/wt || true && git -C /scratch/wt init -q && git -C /scratch/wt commit -q -m seed`,
			want:    hookio.Ask,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &hookio.HookInput{
				ToolName: "Bash", ToolInput: mustJSON(tt.command), CWD: "/repo", PermissionMode: "default",
			}
			if got := hookio.Verdict(New(missing()).Evaluate(in)).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPrimaryCommit_SelfCreatedTempDir covers pg2-70g51's var-opaque shape (pg2-69i0d's
// rows 427912/428023): a variable bound, earlier in the SAME command, to nothing but
// the output of `mktemp -d` — unresolvable to a literal path, but PROVABLY not the
// canonical clone regardless of that path's actual value, since mktemp always creates
// a fresh, session-unique directory. The positive rows are "&&"-chained end to end; the
// negative rows swap in a ";" so the mktemp call's own success is no longer required
// for the commit to run, reproducing the SAME hazard TestPrimaryCommit_SelfCreatedDir's
// negative rows do for the literal shape.
//
// Every row uses `-C` on the commit leaf, for the SAME reason
// TestPrimaryCommit_SelfCreatedDir's own doc gives: this rule does not itself model a
// `cd`'s effect on a LATER leaf's cwd, so a bare `cd "$d"` here would leave cwd at
// input.CWD unchanged (never actually advancing to "$d"'s value, resolved or not) and
// exercise a DIFFERENT, uninteresting code path instead. The "cd" spelling, with the
// real engine's cwd modeling in play, is covered by
// TestIntegration_PrimaryCommitInCommandVars's ACCEPTANCE 5 block (internal/engine).
func TestPrimaryCommit_SelfCreatedTempDir(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{
			name:    "POSITIVE: mktemp -d assignment && -C into it && commit",
			command: `d=$(mktemp -d) && git -C "$d" commit -q -m seed`,
			want:    hookio.NoOpinion,
		},
		{
			name:    "POSITIVE: braced reference",
			command: `d=$(mktemp -d) && git -C "${d}" commit -q -m seed`,
			want:    hookio.NoOpinion,
		},
		{
			name:    "POSITIVE: an intervening git-init leaf stays in the same chain",
			command: `d=$(mktemp -d) && git -C "$d" init -q && git -C "$d" commit -q -m seed`,
			want:    hookio.NoOpinion,
		},
		{
			name: "NEGATIVE: rows 427912/428023's own shape — ';'/newline separated",
			command: "d=$(mktemp -d);\n" +
				"git -C \"$d\" init -q && git -C \"$d\" commit -q -m seed --allow-empty",
			want: hookio.Ask,
		},
		{
			name:    "NEGATIVE: ';'-separated, direct -C spelling",
			command: `d=$(mktemp -d); git -C "$d" commit -q -m seed`,
			want:    hookio.Ask,
		},
		{
			// Mirrors TestPrimaryCommit_SelfCreatedDir's own "||"-anywhere-poisons row:
			// still ONE *syntax.Stmt, no ";"/newline at all.
			name:    "NEGATIVE: an '||' anywhere in the statement disables tracking entirely",
			command: `d=$(mktemp -d) || true && git -C "$d" commit -q -m seed`,
			want:    hookio.Ask,
		},
		{
			name:    "NEGATIVE: a DIFFERENT variable is not this one's binding",
			command: `d=$(mktemp -d) && git -C "$other" commit -q -m seed`,
			want:    hookio.Ask,
		},
		{
			name:    "NEGATIVE: not a mktemp -d value at all",
			command: `d=$(git rev-parse --show-toplevel) && git -C "$d" commit -q -m seed`,
			want:    hookio.Ask,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &hookio.HookInput{
				ToolName: "Bash", ToolInput: mustJSON(tt.command), CWD: "/repo", PermissionMode: "default",
			}
			if got := hookio.Verdict(New(canonMain()).Evaluate(in)).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrimaryCommit_NilResolver(t *testing.T) {
	got := hookio.Verdict(New(nil).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON("git commit"), CWD: "/repo", PermissionMode: "bypassPermissions"})).Decision
	if got != hookio.NoOpinion {
		t.Errorf("Decision = %v, want Abstain", got)
	}
}
