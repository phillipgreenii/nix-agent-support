// pg2-eqacu: the directory-resolution half of primary-push, at the RULE level.
//
// This rule used to hold a private effectiveDir that read `git -C` and the cwd only, so a
// `git -C $WT push` from a canonical clone on its primary branch resolved UP to that clone,
// read `cur == primary`, and REJECTED a push really headed for a nested worktree on a
// feature branch. These cases pin the three things that fixes: the shared seam is what
// resolves the directory, an unresolvable one is decisive rather than answered, and the
// reason says which of the two it was.
//
// The whole-chain half — the `cd`/`pushd` re-root, which is the ENGINE's, and the real
// on-disk nested-worktree fixture — is in enginedirresolve_test.go beside this file.
package primarypush

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// dirResolver answers per DIRECTORY rather than uniformly, which is what makes the
// AGREEMENT relation below meaningful: canonMain() would answer "canonical, on primary"
// for every directory, so a mis-resolved path and a correctly resolved one would be
// indistinguishable. Here `canonical` is a canonical clone on "main" and anything at or
// under `worktree` is a linked worktree (IsCanonical false), exactly as FileResolver
// reports the two.
type dirResolver struct {
	canonical, worktree string
	asked               []string
}

func (d *dirResolver) isWorktree(dir string) bool {
	return dir == d.worktree || strings.HasPrefix(dir, d.worktree+string(filepath.Separator))
}

func (d *dirResolver) IsCanonical(dir string) (bool, error) {
	d.asked = append(d.asked, dir)
	return !d.isWorktree(dir), nil
}
func (d *dirResolver) PrimaryBranch(string) (string, error) { return "main", nil }
func (d *dirResolver) CurrentBranch(dir string) (string, error) {
	if d.isWorktree(dir) {
		return "feat", nil
	}
	return "main", nil
}
func (d *dirResolver) PushDefault(string) (string, error) { return "", nil }
func (d *dirResolver) Aliases(string) (map[string]string, error) {
	return nil, nil
}

func newDirResolver() *dirResolver {
	return &dirResolver{canonical: "/repo/canonical", worktree: "/repo/canonical/.worktrees/feat"}
}

// decide is one hook evaluation of `cmd` in `mode` from `cwd`.
func decide(t *testing.T, res *dirResolver, mode, cwd, cmd string) hookio.RuleResult {
	t.Helper()
	return hookio.Verdict(New(res).Evaluate(&hookio.HookInput{
		ToolName: "Bash", ToolInput: mustJSON(cmd), CWD: cwd, PermissionMode: mode,
	}))
}

// TestPrimaryPush_DirectoryResolution is the verdict table for the three directory
// sources. The `cd`/`pushd` source appears here as an ALREADY-ADVANCED cwd, which is what
// the engine hands each leaf (EvaluateExpression, pg2-opclh) — including the verbatim
// join that leaves an unexpandable target IN the cwd, the `/repo/canonical/$WT` rows.
func TestPrimaryPush_DirectoryResolution(t *testing.T) {
	const canonical = "/repo/canonical"
	const worktree = "/repo/canonical/.worktrees/feat"

	tests := []struct {
		name    string
		cwd     string
		command string
		mode    string
		want    hookio.Decision
	}{
		// --- source 1: the session cwd. Unchanged behaviour, restated as the baseline. ---
		{"bypass: bare push in the canonical clone on primary", canonical, "git push", "bypassPermissions", hookio.Reject},
		{"default: bare push in the canonical clone on primary", canonical, "git push", "default", hookio.NoOpinion},
		{"bypass: bare push from inside the linked worktree", worktree, "git push", "bypassPermissions", hookio.NoOpinion},

		// --- source 2: `git -C <literal>`. THE DEFECT: before pg2-eqacu the -C was read
		// but the session cwd decided anything the -C could not. A literal -C into the
		// worktree was already right; these pin it against regression. ---
		{"bypass: literal -C into the worktree", canonical, "git -C " + worktree + " push", "bypassPermissions", hookio.NoOpinion},
		{"bypass: literal -C into the worktree, explicit refspec", canonical, "git -C " + worktree + " push origin HEAD", "bypassPermissions", hookio.NoOpinion},
		{"bypass: literal -C back into the canonical clone still denies", worktree, "git -C " + canonical + " push", "bypassPermissions", hookio.Reject},
		{"bypass: literal RELATIVE -C resolves (joins onto the cwd)", canonical, "git -C .worktrees/feat push", "bypassPermissions", hookio.NoOpinion},

		// --- source 3: the cwd the engine advanced past a `cd`/`pushd`. ---
		{"bypass: cwd advanced into the worktree", worktree, "git push", "bypassPermissions", hookio.NoOpinion},
		{"bypass: cwd advanced, then -C back to canonical", worktree, "git -C " + canonical + " push origin HEAD:main", "bypassPermissions", hookio.Reject},

		// --- UNRESOLVED: decisive in EVERY mode, and the resolver is never consulted. ---
		{"bypass: git -C $VAR push (unresolved)", canonical, "git -C $WT push", "bypassPermissions", hookio.Reject},
		// auto is not in primarycommit.AutoApprovingModes (it prompts on an Ask rather
		// than silently accepting one — operator-confirmed 2026-08-14/2026-08-15), so an
		// unresolved target now asks there exactly as an interactive session does.
		{"auto: git -C $VAR push (unresolved) now asks", canonical, "git -C $WT push", "auto", hookio.Ask},
		{"dontAsk: git -C ${VAR} push (unresolved)", canonical, "git -C ${WT} push", "dontAsk", hookio.Reject},
		{"default: git -C $VAR push asks (cannot reach approve)", canonical, "git -C $WT push", "default", hookio.Ask},
		{"plan: git -C $VAR push asks", canonical, "git -C $WT push", "plan", hookio.Ask},
		{"acceptEdits: git -C $VAR push asks", canonical, "git -C $WT push", "acceptEdits", hookio.Ask},
		{"empty mode: git -C $VAR push asks", canonical, "git -C $WT push", "", hookio.Ask},
		// Every expansion that can reach a PATH, not just `$`.
		{"default: git -C $(…) push asks", canonical, "git -C $(git rev-parse --show-toplevel) push", "default", hookio.Ask},
		{"default: git -C backtick push asks", canonical, "git -C `pwd` push", "default", hookio.Ask},
		{"default: git -C glob push asks", canonical, "git -C /repo/canonical/.worktrees/* push", "default", hookio.Ask},
		{"default: git -C tilde push asks", canonical, "git -C ~/repo push", "default", hookio.Ask},
		// The unresolved token reached through the CWD instead — the shape the engine's
		// verbatim join leaves behind for `cd $WT && git push`.
		{"bypass: unresolved cwd (cd $VAR joined verbatim)", canonical + "/$WT", "git push", "bypassPermissions", hookio.Reject},
		{"default: unresolved cwd (cd $VAR joined verbatim) asks", canonical + "/$WT", "git push", "default", hookio.Ask},
		// An explicit FEATURE refspec does not rescue it: which branch is "primary" is a
		// property of the unknown target repository. See inspectPush's DECLINED note. This
		// row is deliberately MORE restrictive than before pg2-eqacu (it approved).
		{"default: git -C $VAR push with a feature refspec still asks", canonical, "git -C $WT push origin feat:feat", "default", hookio.Ask},
		{"bypass: git -C $VAR push with a feature refspec rejects", canonical, "git -C $WT push origin feat:feat", "bypassPermissions", hookio.Reject},
		// An unresolved dir reached through an ALIAS is still caught.
		{"default: alias to a push with an unresolved -C asks", canonical, "git -C $WT p", "default", hookio.Ask},
		{"default: shell alias to a push with an unresolved -C asks", canonical, "git -C $WT sp", "default", hookio.Ask},

		// --- SCOPE: this rule only ever governs a push. An unresolved directory on any
		// other subcommand must not acquire a gate. ---
		{"bypass: git -C $VAR status untouched", canonical, "git -C $WT status", "bypassPermissions", hookio.NoOpinion},
		{"bypass: git -C $VAR fetch untouched", canonical, "git -C $WT fetch origin", "bypassPermissions", hookio.NoOpinion},
		{"default: git -C $VAR status untouched", canonical, "git -C $WT status", "default", hookio.NoOpinion},
		{"bypass: unresolved cwd, non-push git untouched", canonical + "/$WT", "git status", "bypassPermissions", hookio.NoOpinion},
		{"bypass: harmless alias with an unresolved -C untouched", canonical, "git -C $WT st", "bypassPermissions", hookio.NoOpinion},

		// --- pg2-wq3ki parity: a value the COMMAND ITSELF writes down is not a guess. It
		// resolves, and the verdict is the resolved directory's — which is the OTHER half
		// of the defect: before pg2-eqacu this rule ignored the in-command environment
		// entirely, so the first row here was a hard deny of a worktree push. ---
		{"bypass: in-command assignment into the worktree resolves", canonical, "WT=" + worktree + " && git -C \"$WT\" push", "bypassPermissions", hookio.NoOpinion},
		{"bypass: in-command assignment, braced form", canonical, "WT=" + worktree + "; git -C \"${WT}\" push", "bypassPermissions", hookio.NoOpinion},
		{"bypass: in-command assignment to the CANONICAL clone still denies", canonical, "WT=" + canonical + " && git -C \"$WT\" push", "bypassPermissions", hookio.Reject},
		{"default: in-command assignment into the worktree costs no prompt", canonical, "WT=" + worktree + " && git -C \"$WT\" push", "default", hookio.NoOpinion},
		// A `$(…)` value is NOT admissible (dirresolve.go's DECLINED section) — it stays
		// unresolved, so it stays fail-safe.
		{"default: assignment from a substitution stays unresolved", canonical, "WT=$(mktemp -d) && git -C \"$WT\" push", "default", hookio.Ask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := newDirResolver()
			// Aliases are attached here rather than per row so the alias rows read as
			// what they test (the -C, not the alias table).
			r := New(&aliasDirResolver{dirResolver: res, aliases: map[string]string{
				"p": "push origin HEAD:main", "sp": "!git push origin HEAD:main", "st": "status",
			}})
			got := hookio.Verdict(r.Evaluate(&hookio.HookInput{
				ToolName: "Bash", ToolInput: mustJSON(tt.command), CWD: tt.cwd, PermissionMode: tt.mode,
			}))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v (%s), want %v", got.Decision, got.Reason, tt.want)
			}
		})
	}
}

// aliasDirResolver is dirResolver with an alias table, for the rows that reach the rule
// through `git <alias>`.
type aliasDirResolver struct {
	*dirResolver
	aliases map[string]string
}

func (a *aliasDirResolver) Aliases(string) (map[string]string, error) { return a.aliases, nil }

// TestPrimaryPush_UnresolvedDirNeverApproves is the pg2-eqacu FAIL-CLOSED guard, and it is
// deliberately a RELATION rather than a per-mode expectation: the point is not which
// non-approving verdict comes out but that NO permission mode, and no spelling of an
// unresolvable directory, can produce one that a later rule turns into an approval. A
// verdict of Approve OR NoOpinion fails it — NoOpinion emits `{}`, which an auto-approving
// session accepts, so it is an approval by another route, and the generic git rule behind
// this one approves a non-force push outright.
//
// It also asserts the resolver was NEVER CONSULTED. That is the mechanical half of the
// defect: consulting it is what produced a confident wrong answer, because gitRoot walks
// UP from `<canonical>/$WT` onto the canonical clone.
func TestPrimaryPush_UnresolvedDirNeverApproves(t *testing.T) {
	commands := []string{
		"git push",        // via an unresolved CWD, below
		"git push origin", // via an unresolved CWD, below
		"git -C $WT push",
		"git -C ${WT} push",
		"git -C \"$WT\" push",
		"git -C $WT push origin HEAD",
		"git -C $WT push origin feat:feat",
		"git -C $WT push --force-with-lease",
		"git -C $WT/nested push",
		"git -C $(pwd) push",
		"git -C `git rev-parse --show-toplevel` push",
		"git -C /repo/canonical/.worktrees/*/ push",
		"git -C ~/repo push",
		"git -C ~ push",
		"WT=$(mktemp -d) && git -C \"$WT\" push",
	}
	// Both the -C route and the CWD route (the shape a `cd $WT` leaves behind once the
	// engine has joined the unexpanded token into the running cwd).
	cwds := []string{"/repo/canonical", "/repo/canonical/$WT", "/repo/canonical/$(pwd)"}
	modes := []string{"bypassPermissions", "auto", "dontAsk", "default", "plan", "acceptEdits", ""}
	for _, cwd := range cwds {
		for _, cmd := range commands {
			if cwd == "/repo/canonical" && !strings.Contains(cmd, "-C") {
				continue // fully resolved: that is the ordinary primary-push case
			}
			for _, mode := range modes {
				t.Run(mode+" "+cwd+" "+cmd, func(t *testing.T) {
					res := newDirResolver()
					got := decide(t, res, mode, cwd, cmd)
					if got.Decision == hookio.Approve || got.Decision == hookio.NoOpinion {
						t.Fatalf("Decision = %v (reason %q); an unresolvable push target MUST NOT reach Approve or an empty verdict", got.Decision, got.Reason)
					}
					if len(res.asked) != 0 {
						t.Errorf("resolver was consulted with %q; an unresolvable directory MUST NOT be resolved at all", res.asked)
					}
				})
			}
		}
	}
}

// TestPrimaryPush_ResolvedSpellingAgreesWithLiteral is the safety argument for the half of
// pg2-eqacu that is deliberately LESS restrictive. Reading a value the command itself
// assigns must not INVENT a permission — it must only make the variable spelling reach the
// verdict the literal spelling already reached. Stated as a RELATION between two spellings
// so it survives any later retuning of what that shared verdict is.
func TestPrimaryPush_ResolvedSpellingAgreesWithLiteral(t *testing.T) {
	const canonical = "/repo/canonical"
	const worktree = "/repo/canonical/.worktrees/feat"
	for _, dir := range []string{canonical, worktree} {
		for _, mode := range []string{"default", "bypassPermissions", "auto", "dontAsk"} {
			for _, tail := range []string{"push", "push origin HEAD", "push origin HEAD:main"} {
				t.Run(mode+" "+dir+" "+tail, func(t *testing.T) {
					literal := decide(t, newDirResolver(), mode, canonical, "git -C "+dir+" "+tail)
					viaC := decide(t, newDirResolver(), mode, canonical, "WT="+dir+" && git -C \"$WT\" "+tail)
					if literal.Decision != viaC.Decision {
						t.Errorf("`git -C $WT %s` = %v (%s) but literal `git -C %s %s` = %v; the resolved spelling must agree with the literal one",
							tail, viaC.Decision, viaC.Reason, dir, tail, literal.Decision)
					}
				})
			}
		}
	}
}

// TestPrimaryPush_ReasonNamesActualCause asserts the DIAGNOSIS half of pg2-eqacu, matching
// the wording convention pg2-h2npt introduced for primary-commit. The old text said only
// "refusing a push that advances the primary branch (main) of the canonical clone", which
// for a mis-resolved directory named a branch problem the agent did not have and never
// named the directory the rule had judged.
func TestPrimaryPush_ReasonNamesActualCause(t *testing.T) {
	t.Run("unresolved names the token, its source, and denies the primary reading", func(t *testing.T) {
		got := decide(t, newDirResolver(), "default", "/repo/canonical", "git -C $WT push")
		for _, want := range []string{
			"cannot determine which repository or branch",
			"$WT",
			"git -C $WT",
			"NOT a finding that the push targets a primary branch",
			"literally",
		} {
			if !strings.Contains(got.Reason, want) {
				t.Errorf("reason %q does not mention %q", got.Reason, want)
			}
		}
		// It MUST NOT read as the primary-branch finding it is not, and MUST NOT print the
		// best-effort directory — that value resolves onto the canonical clone, so printing
		// it is the fabricated provenance this branch exists to refuse.
		if strings.Contains(got.Reason, "refusing this push") {
			t.Errorf("unresolved reason reads as the primary-branch finding: %q", got.Reason)
		}
		if strings.Contains(got.Reason, "Directory evaluated") {
			t.Errorf("unresolved reason names a directory it could not establish: %q", got.Reason)
		}
	})

	t.Run("unresolved names the cwd when the cwd is what defeated it", func(t *testing.T) {
		got := decide(t, newDirResolver(), "default", "/repo/canonical/$WT", "git push")
		for _, want := range []string{"cannot determine which repository or branch", "working directory", "$WT"} {
			if !strings.Contains(got.Reason, want) {
				t.Errorf("reason %q does not mention %q", got.Reason, want)
			}
		}
	})

	t.Run("primary names the directory evaluated and how it was chosen", func(t *testing.T) {
		got := decide(t, newDirResolver(), "bypassPermissions", "/repo/canonical", "git push")
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
		got := decide(t, newDirResolver(), "bypassPermissions", "/repo/canonical/.worktrees/feat",
			"git -C /repo/canonical push origin HEAD:main")
		for _, want := range []string{"Directory evaluated: /repo/canonical", "`git -C /repo/canonical` option"} {
			if !strings.Contains(got.Reason, want) {
				t.Errorf("reason %q does not mention %q", got.Reason, want)
			}
		}
	})

	t.Run("primary names the assignment it read the directory out of", func(t *testing.T) {
		got := decide(t, newDirResolver(), "bypassPermissions", "/repo/canonical/.worktrees/feat",
			"WT=/repo/canonical && git -C \"$WT\" push")
		for _, want := range []string{"Directory evaluated: /repo/canonical", "earlier in the same command"} {
			if !strings.Contains(got.Reason, want) {
				t.Errorf("reason %q does not mention %q", got.Reason, want)
			}
		}
	})
}

// TestPrimaryPush_HasNoPrivateDirectoryResolution is the mechanical guard for "ONE
// implementation, not two that drift" (pg2-eqacu's acceptance criterion). The defect this
// bead fixes was not a wrong line of code, it was a SECOND COPY: this package's own
// effectiveDir, which never learned what pg2-h2npt taught primary-commit's. A reviewer
// cannot see a re-introduced copy in a diff that only adds a small helper, so the absence
// is asserted here instead — over the package's non-test sources, which is where a rule's
// resolution would have to live to affect a verdict.
func TestPrimaryPush_HasNoPrivateDirectoryResolution(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	resolveDirCalls := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(src)
		// A private directory-assembly helper is the exact shape that drifted. The two
		// tokens below are what such a helper cannot avoid: it must join a `-C` argument
		// onto something, and it must decide absoluteness.
		for _, banned := range []string{"func effectiveDir", "filepath.Join(", "filepath.IsAbs("} {
			if strings.Contains(body, banned) {
				t.Errorf("%s contains %q: primary-push MUST NOT assemble a directory itself — call primarycommit.ResolveDir (pg2-eqacu)", name, banned)
			}
		}
		resolveDirCalls += strings.Count(body, "primarycommit.ResolveDir(")
	}
	// Exactly one call site: more than one would mean two resolutions per verdict, which
	// is how a "best-effort" directory leaks past the Unresolved() branch.
	if resolveDirCalls != 1 {
		t.Errorf("primarycommit.ResolveDir call sites = %d, want 1", resolveDirCalls)
	}
}
