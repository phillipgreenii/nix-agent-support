package git

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE ENV SPELLING THAT NAMES THE PROGRAM DIRECTLY (pg2-6c85x).
//
// pg2-a12rl closed the config-SOURCE env route (`GIT_CONFIG*`, which names a config
// file or key/value pairs). This is the same inconsistency one level down: a handful of
// `GIT_*` variables name the PROGRAM git executes, with no config key in between, and
// each is a `gatedConfigKeys` configSink entry by another name. Measured through real
// git 2.54.0 on 2026-08-13 (`scripts/probe-pg2-6c85x.sh`), `GIT_EXTERNAL_DIFF=<marker>
// git diff` and `GIT_SSH_COMMAND=<marker> git ls-remote ssh://…` RAN the marker while
// the argv spellings `git -c diff.external=<marker> diff` / `-c core.sshCommand=…` were
// already screened.
//
// EVERY ASSERTION HERE IS A RELATION, not a literal verdict, for the reason
// configenv_test.go states: a hardcoded pair goes stale the moment either route is
// retuned, and both have been retuned before (pg2-szadj, pg2-ur9zc, pg2-u0e0c). The
// approve-class subcommand list and the `dashC` argv spelling are REUSED from
// configenv_test.go rather than copied, so the two families cannot drift apart.
//
// THE TWO STYLES OF TEST BELOW ARE COMPLEMENTARY, AND THE MUTATION SURVEY SHOWS WHY.
// Disabling the demotion fails the relation, fail-closed and `{}` tests; re-shaping it
// into the `-c` route's pre-classify short-circuit fails only
// DoesNotWeakenADecisiveVerdict; widening the exact name match to a `GIT_` prefix fails
// only DoesNotOverreach and DeclinedVariablesStayUnscreened. And DELETING one variable
// from gitProgramEnvVars is invisible to every table-ITERATING test — the row simply
// stops being generated — so it is caught instead by FailsClosed and
// EmitsEmptyHookOutput, which NAME the variables literally. That is the one place a
// hardcoded spelling is the stronger assertion, and it is why both styles are kept.

// envProg is the env spelling of "run this program": one leading assignment of the
// variable, then the git command.
func envProg(name, prog, gitArgs string) string {
	return fmt.Sprintf("%s=%s git %s", name, prog, gitArgs)
}

// TestGit_ProgramEnvVar_TwinIsAConfigSinkInTheRealTable derives the screen's
// justification FROM `gatedConfigKeys` instead of restating it. Each screened variable
// declares the config key it is the env spelling of, and that key must resolve in this
// package's own table as a configSink — so the screen cannot claim a twin the table does
// not class as "git EXECUTES the value".
//
// IT NOW HAS NO EXCEPTIONS, AND THE ABSENCE OF THE EXCEPTION MAP IS THE ASSERTION
// (pg2-h1ori). pg2-6c85x carried an `absentTwins` allowance for `core.askPass`, which was
// absent from `gatedConfigKeys` altogether: `GIT_ASKPASS` was MEASURED reaching an exec
// sink (`git credential fill` ran the marker as the username prompt) so declining it would
// have left a measured sink unscreened, while ADDING the key changes the `git config
// core.askPass …` porcelain verdict — a route that bead did not measure. pg2-h1ori made
// that ruling and added the key, so the allowance is REMOVED rather than left standing over
// a resolved asymmetry. Every screened variable's twin must now resolve in the real table,
// with no per-key escape hatch: a NEW absent twin is a hard failure here and needs its own
// ruling, exactly as `core.askPass` got one.
func TestGit_ProgramEnvVar_TwinIsAConfigSinkInTheRealTable(t *testing.T) {
	if len(gitProgramEnvVars) == 0 {
		t.Fatal("gitProgramEnvVars is empty — the program-naming env screen was deleted")
	}
	for name, twin := range gitProgramEnvVars {
		_, id, ok := configKeyID(twin)
		if !ok {
			t.Errorf("%s: twin %q is not key-shaped, so configKeyID cannot resolve it against gatedConfigKeys", name, twin)
			continue
		}
		class, gated := gatedConfigKeys[id]
		if !gated {
			t.Errorf("%s: twin %q (id %q) is NOT in gatedConfigKeys — either the table entry was removed (then this variable's justification is gone) or a NEW twin is missing, which needs its own ruling and its own `git config` replay the way `core.askPass` got one (pg2-h1ori); an exception map here is what pg2-h1ori deleted and MUST NOT be reintroduced", name, twin, id)
			continue
		}
		if class != configSink {
			t.Errorf("%s: twin %q (id %q) is class %d, not configSink — this screen exists because git EXECUTES the value; a twin in another class needs its own ruling", name, twin, id, class)
		}
	}
}

// TestGit_ProgramEnvVar_MatchesTheDashCRoute is the acceptance criterion stated as a
// relation: for the SAME sink, the env spelling and the `-c` spelling of its twin key
// reach the SAME verdict, and neither is Approve.
//
// The Approve floor is not a duplicate of the equality — two routes could agree on
// Approve and satisfy equality while leaving the hole open.
func TestGit_ProgramEnvVar_MatchesTheDashCRoute(t *testing.T) {
	for name, twin := range gitProgramEnvVars {
		for _, sub := range approveClassSubcommands {
			envCmd := envProg(name, "/tmp/evil", sub)
			argvCmd := dashC(twin, "/tmp/evil", sub)
			envGot := evalCmd(t, envCmd)
			argvGot := evalCmd(t, argvCmd)
			if envGot.Decision != argvGot.Decision {
				t.Errorf("%s, `git %s`: env spelling got %s (%s) but the -c %s spelling got %s (%s) — one exec sink, two spellings, and they MUST reach the same verdict (pg2-6c85x); if a route was deliberately retuned, retune BOTH",
					name, sub, envGot.Decision, envGot.Reason, twin, argvGot.Decision, argvGot.Reason)
			}
			if envGot.Decision == hookio.Approve {
				t.Errorf("%s, `git %s`: env spelling got APPROVE — git runs the program this variable names (marker evidence in scripts/probe-pg2-6c85x.sh), exactly as `%s` does", name, sub, twin)
			}
		}
	}
}

// TestGit_ProgramEnvVar_IsNeverLessRestrictiveThanDashC is the relation that holds for
// EVERY git invocation, including the ones whose base verdict is decisive.
//
// The strict equality above is scoped to the Approve class for the reason
// configenv_test.go records: the `-c` route short-circuits BEFORE classify and so
// WEAKENS decisive Rejects (its own defect, pg2-6f4q9), while this screen only withdraws
// an Approve. On a decisive subcommand the env route is therefore strictly MORE
// restrictive, and equality would be the wrong assertion. This invariant survives either
// fixing pg2-6f4q9 or leaving it.
func TestGit_ProgramEnvVar_IsNeverLessRestrictiveThanDashC(t *testing.T) {
	subs := append([]string{
		"tag v1",
		"push --force origin main",
		"push -f origin main",
		"remote add upstream https://example.invalid/x.git",
		"config core.hooksPath /tmp/h",
		"config remote.origin.url https://evil.invalid/x.git",
		"clean -fdx",
		"reset --hard HEAD~1",
		"branch -D feat",
		"bisect start",
	}, approveClassSubcommands...)
	for name, twin := range gitProgramEnvVars {
		for _, sub := range subs {
			envGot := evalCmd(t, envProg(name, "/tmp/evil", sub))
			argvGot := evalCmd(t, dashC(twin, "/tmp/evil", sub))
			if envGot.Decision < argvGot.Decision {
				t.Errorf("%s, `git %s`: env spelling got %s (%s), which is LESS restrictive than the -c %s spelling's %s (%s) — the env route must never be the cheaper way around the same guard (pg2-6c85x)",
					name, sub, envGot.Decision, envGot.Reason, twin, argvGot.Decision, argvGot.Reason)
			}
		}
	}
}

// TestGit_ProgramEnvVar_FailsClosed is the acceptance criterion that an UNPARSEABLE or
// PARTIALLY-PARSED env prefix must not reach Approve.
//
// Every row is a spelling from which the VALUE cannot be read, or cannot be trusted to
// be the whole story: an empty value, a bare variable reference, a command
// substitution, a quoted multi-word value, a value that merely LOOKS benign. A
// value-reading screen would have to answer each of these individually and fail open on
// whichever it did not anticipate. The name-keyed screen answers all of them by
// construction, and this test is what proves the construction is load-bearing.
//
// THE BENIGN-LOOKING ROWS ARE THE POINT OF THE NAME-BLIND RULING. `GIT_PAGER=cat` really
// is harmless, and it is screened anyway — because the `-c` route is value-blind for
// `core.pager` too, so sparing it would make the env spelling WEAKER than
// `git -c core.pager=cat`, re-creating this bead's own asymmetry in the opposite
// direction. git also runs the pager THROUGH A SHELL, so "benign" would have to mean
// shell-parsing the value, not comparing it to a word list.
//
// TWO ROWS LEFT THIS TEST, AND THE GUARANTEE THEY PROTECTED IS UNCHANGED (pg2-6qh3p,
// operator ruling on pg2-agprs of 2026-08-13). `GIT_EDITOR=true git commit --amend` and
// `GIT_EDITOR=: git commit --amend` are now APPROVED, under the INERT-VALUE CARVE-OUT
// that same ruling authorized — 65 of this bead's 97 newly-prompting rows were exactly
// this idiom. They were removed rather than re-pointed because the ruling reverses their
// expectation, not because the property they asserted was dropped:
//
//   - The relation they served — the env spelling is never LESS restrictive than argv —
//     is intact and is still asserted by TestGit_ProgramEnvVar_MatchesTheDashCRoute and
//     TestGit_ProgramEnvVar_IsNeverLessRestrictiveThanDashC BELOW, UNMODIFIED. It holds
//     because the ruling carved out the ARGV spellings in the same change
//     (clearedConfigFlagPairs' `core.editor` / `sequence.editor` entries), so both
//     spellings clear for these two values and both screen for every other.
//   - The fail-closed property for the editor family is asserted, in more detail than
//     these two rows ever did, in editorcarveout_test.go: near-miss tokens (`truex`,
//     `true `, `/bin/true`, `:;evil`, `TRUE`, the QUOTED `"true"`) and non-literal values
//     (`$X`, `$(echo true)`) all still reach the screened verdict.
//
// The `${EDITOR:-vi}` row STAYS HERE and is the one that keeps the two tests honest: it
// is an editor variable whose value is not a literal, so it must be screened by this
// test's own claim as well as by the carve-out's fail-closed guard.
func TestGit_ProgramEnvVar_FailsClosed(t *testing.T) {
	cmds := []string{
		// The value is empty, dynamic, or not statically visible.
		"GIT_EXTERNAL_DIFF= git diff",
		"GIT_EXTERNAL_DIFF=$D git diff",
		"GIT_PAGER=$(command -v cat) git log",
		"GIT_EDITOR=${EDITOR:-vi} git commit --amend",
		`GIT_SSH_COMMAND="ssh -i /tmp/k -o StrictHostKeyChecking=no" git fetch origin`,
		`GIT_EXTERNAL_DIFF='difft --display=inline' git diff`,
		// Values that LOOK benign — screened anyway, deliberately.
		"GIT_PAGER=cat git log",
		"GIT_PAGER=cat git diff",
		// Position and wrapper forms: the assignment reaches pc.EnvVars either way.
		"env GIT_EXTERNAL_DIFF=/tmp/evil git diff",
		"GIT_DIR=/other GIT_PAGER=/tmp/evil git log",
		"GIT_EXTERNAL_DIFF=/tmp/evil git -C /repo diff",
		"GIT_ASKPASS=/tmp/evil git fetch origin",
		"GIT_SSH=/tmp/evil git fetch origin",
		// Two screened variables at once, and one alongside the pg2-a12rl family.
		"GIT_PAGER=/tmp/evil GIT_EDITOR=/tmp/evil git log",
		"GIT_CONFIG_GLOBAL=/tmp/evil.cfg GIT_PAGER=/tmp/evil git status",
	}
	for _, cmd := range cmds {
		if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — an env prefix that names a program git executes MUST NOT reach Approve, whatever its value says (pg2-6c85x)", cmd, got.Reason)
		}
	}
}

// TestGit_ProgramEnvVar_DoesNotWeakenADecisiveVerdict is the other half of fail-closed,
// and the reason this screen is a DEMOTION rather than the `-c` route's pre-classify
// short-circuit.
//
// A more-restrictive change must not make anything LESS restrictive as a side effect. If
// the screen answered before classify — the shape hasGitConfigInjection has, measured to
// turn `git tag` and a force-push from `deny` into an auto-approvable `{}`, filed as
// pg2-6f4q9 — then prefixing any of these variables would buy a way around every
// decisive verdict in the file. These rows fail the moment someone "simplifies" the call
// site to match the `-c` one.
func TestGit_ProgramEnvVar_DoesNotWeakenADecisiveVerdict(t *testing.T) {
	rows := []struct {
		sub  string
		want hookio.Decision
		why  string
	}{
		{"tag v1", hookio.Reject, "git tag is prohibited in this workflow"},
		{"push --force origin main", hookio.Reject, "force-push is an operator-ruled Reject"},
		{"push -f origin main", hookio.Reject, "same, short spelling"},
		{"remote add upstream https://example.invalid/x.git", hookio.Reject, "a remote mutation is an exfiltration vector"},
		{"config remote.origin.url https://evil.invalid/x.git", hookio.Reject, "the config spelling of `git remote set-url`"},
		{"config core.hooksPath /tmp/h", hookio.Ask, "a configSink porcelain write asks"},
		{"config clean.requireForce false", hookio.Ask, "a configInterlock porcelain write asks"},
	}
	for name := range gitProgramEnvVars {
		for _, row := range rows {
			cmd := envProg(name, "/tmp/evil", row.sub)
			if got := evalCmd(t, cmd); got.Decision != row.want {
				t.Errorf("cmd %q: got %s (%s), want %s — %s; a program-naming env prefix must never be the cheaper way around a decisive verdict (pg2-6c85x)", cmd, got.Decision, got.Reason, row.want, row.why)
			}
		}
	}
}

// TestGit_ProgramEnvVar_DoesNotOverreach pins the boundary of the screen. Each row is a
// command git itself treats as naming NO caller-supplied program, so gating it would be a
// false prompt on ordinary traffic.
func TestGit_ProgramEnvVar_DoesNotOverreach(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
		why  string
	}{
		// ENV VAR NAMES ARE CASE-SENSITIVE to git's getenv. Measured 2026-08-13: the
		// lowercase spellings did NOT run the marker, so git read them as ordinary
		// variables and so must this rule.
		{"git_external_diff=/tmp/evil git diff", hookio.Approve, "lowercase is not the variable git reads"},
		{"git_ssh_command=/tmp/evil git fetch origin", hookio.Approve, "same"},
		{"Git_Pager=/tmp/evil git log", hookio.Approve, "mixed case is not it either"},
		// Names that merely CONTAIN or EXTEND a screened name.
		{"GIT_PAGERX=/tmp/evil git log", hookio.Approve, "a longer name is a different variable"},
		{"MY_GIT_PAGER=/tmp/evil git log", hookio.Approve, "a name that merely ENDS with the spelling is not it"},
		{"GIT_EXTERNAL_DIFF_OPTS=/tmp/evil git diff", hookio.Approve, "not the variable git reads for the diff driver"},
		{"GIT_SSH_VARIANT=ssh git fetch origin", hookio.Approve, "selects a command-line dialect; it names no program"},
		// Ordinary traffic and the neighbouring policies, all unchanged.
		{"git status", hookio.Approve, "no assignment at all"},
		{"FOO=bar git status", hookio.Approve, "an unrelated assignment"},
		{"GIT_DIR=/other git log", hookio.Approve, "the redirect policy for a READ is unchanged"},
		{"GIT_DIR=/other git commit -m msg", hookio.Ask, "the redirect Ask for a WRITE is unchanged"},
		// TEXT IS NOT AN OPERATION — the pg2-5b901 class. A screened spelling quoted in
		// a message is an ARGUMENT, never an assignment, so this bead's own bookkeeping
		// stays runnable.
		{`git commit -m "screen GIT_EXTERNAL_DIFF/GIT_SSH_COMMAND (pg2-6c85x)"`, hookio.Approve, "a mention in a commit message is text"},
		{`git commit -m "GIT_PAGER=cat measured allow before the fix"`, hookio.Approve, "same, with an = in the text"},
	}
	for _, row := range rows {
		if got := evalCmd(t, row.cmd); got.Decision != row.want {
			t.Errorf("cmd %q: got %s (%s), want %s — %s", row.cmd, got.Decision, got.Reason, row.want, row.why)
		}
	}
}

// TestGit_ProgramEnvVar_DeclinedVariablesStayUnscreened makes each DECLINED variable's
// declination visible and intentional rather than an omission.
//
// Every member was MEASURED reaching an exec sink, so none is declined for want of
// evidence — the reasons are recorded in declinedGitProgramEnvVars and are about a
// CONFLICTING ruling or a MISSING MECHANISM elsewhere in this file. This test pins the
// consequence: a later edit that screens one of them fails here and has to remove the
// recorded reason deliberately.
//
// AN EMPTY MAP IS LEGITIMATE, AND THAT CHANGED WITH pg2-qi1jo. The map used to be required
// non-empty, which reads as "there will always be an outstanding declination" — but it is a
// register of OUTSTANDING declinations, and both of pg2-6c85x's original members have since
// left because the rulings they deferred to were MADE (`GIT_SEQUENCE_EDITOR` by pg2-6qh3p,
// `GIT_PROXY_COMMAND` by pg2-qi1jo). A `Fatal` on emptiness would make settling the last
// declination fail the suite, which is backwards: an empty register is the GOAL state. What
// this test actually guards is that no member is silently screened and no member carries an
// empty reason, and both hold at any size.
func TestGit_ProgramEnvVar_DeclinedVariablesStayUnscreened(t *testing.T) {
	for name, reason := range declinedGitProgramEnvVars {
		if reason == "" {
			t.Errorf("%s: declined with an EMPTY reason — the acceptance criterion is that every declination records why (pg2-6c85x)", name)
		}
		if _, screened := gitProgramEnvVars[name]; screened {
			t.Errorf("%s: appears in BOTH gitProgramEnvVars and declinedGitProgramEnvVars", name)
		}
	}
	// The declinations' own load-bearing consequences, spelled out.
	//
	// GIT_SEQUENCE_EDITOR: classify's rebase arm REQUIRES this variable to be present
	// before it will approve an interactive rebase, so a value-BLIND screen would demote
	// exactly the invocations the rule demands it on. pg2-a12rl's landed configenv_test.go
	// pins this Approve, and this bead must not modify that file.
	//
	// THE VARIABLE IS NO LONGER DECLINED — IT IS SCREENED WITH A VALUE CARVE-OUT
	// (pg2-6qh3p, operator ruling on pg2-agprs of 2026-08-13), so it has moved out of
	// declinedGitProgramEnvVars and into gitProgramEnvVars. THIS ASSERTION IS KEPT
	// UNCHANGED AND IS NOW LOAD-BEARING FOR BOTH RULINGS AT ONCE: `:` is one of the two
	// INERT literals the carve-out allows, so the rebase arm's mandated idiom keeps the
	// Approve pg2-a12rl pinned, while `GIT_SEQUENCE_EDITOR=<a real program>` is screened
	// like every other exec sink. If the carve-out is ever narrowed away, this row is
	// where it shows up.
	if got := evalCmd(t, "GIT_SEQUENCE_EDITOR=: git rebase -i main"); got.Decision != hookio.Approve {
		t.Errorf("`GIT_SEQUENCE_EDITOR=: git rebase -i main`: got %s (%s), want APPROVE — this rule's own rebase carve-out requires the variable, and configenv_test.go pins the verdict", got.Decision, got.Reason)
	}
	// GIT_PROXY_COMMAND: NO LONGER DECLINED (pg2-qi1jo, 2026-08-14). Its declination was
	// never about evidence — `git ls-remote git://…` ran the marker as `<host> 9418` — but
	// about `core.gitProxy` sitting in gatedConfigKeys' left-approved list under an explicit
	// instruction to settle the alternate-transport family "under one ruling". That ruling
	// was MADE and covers both routes, so the variable moved into gitProgramEnvVars and the
	// assertion here INVERTS: it must now be screened like every other measured sink, and
	// its `-c core.gitProxy` twin must agree with it (the relation the two
	// IsNeverLessRestrictive tests above enforce across the whole table).
	if got := evalCmd(t, "GIT_PROXY_COMMAND=/tmp/evil git fetch origin"); got.Decision == hookio.Approve {
		t.Errorf("`GIT_PROXY_COMMAND=/tmp/evil git fetch origin`: got APPROVE (%s) — pg2-qi1jo settled the alternate-transport family by SCREENING it; git executes this value as the transport proxy (marker evidence in scripts/probe-pg2-qi1jo.sh)", got.Reason)
	}
	// GIT_ALLOW_PROTOCOL: the one member that IS still declined, and its declination is a
	// table-shape consequence rather than a policy one. It is the env twin of
	// `protocol.<n>.allow`, which is a configINTERLOCK — it names no program, it removes a
	// refusal — so it has no home in the program-naming table, and this file has no
	// interlock env screen for it to join. The row is pinned so the day one appears, this
	// assertion is where the declination has to be revisited.
	if got := evalCmd(t, "GIT_ALLOW_PROTOCOL=ext git ls-remote origin"); got.Decision != hookio.Approve {
		t.Errorf("`GIT_ALLOW_PROTOCOL=ext git ls-remote origin`: got %s (%s), want APPROVE — screening it needs an INTERLOCK env screen this file does not have, and covering it while leaving `GIT_SSL_NO_VERIFY` open is the split pg2-qi1jo's family instruction existed to prevent", got.Decision, got.Reason)
	}
}

// TestGit_ProgramEnvVar_EmitsEmptyHookOutput is the BOUNDARY-LEVEL assertion, and it is
// not redundant with the rule-level tests: asserting the internal Decision cannot show
// what Claude Code actually RECEIVES. The withdrawn Approve must serialize to `{}` — the
// same output the `-c` route produces — and specifically must not carry
// `permissionDecision: "allow"`.
//
// The chain-level twin, proving no LATER rule re-approves the leaf, is
// TestIntegration_GitProgramEnvVar_EmitsEmptyObject in the engine suite.
func TestGit_ProgramEnvVar_EmitsEmptyHookOutput(t *testing.T) {
	for _, cmd := range []string{
		envProg("GIT_EXTERNAL_DIFF", "/tmp/evil", "diff"),
		envProg("GIT_SSH_COMMAND", "/tmp/evil", "fetch origin"),
		envProg("GIT_PAGER", "/tmp/evil", "log"),
		envProg("GIT_EDITOR", "/tmp/evil", "commit --amend"),
		envProg("GIT_ASKPASS", "/tmp/evil", "fetch origin"),
		envProg("GIT_SSH", "/tmp/evil", "fetch origin"),
	} {
		got := evalCmd(t, cmd)
		out := string(hookio.FormatOutput(got, nil))
		if out != "{}" {
			t.Errorf("cmd %q: emitted %s, want {} — `permissionDecision: \"allow\"` auto-approves a command that runs a program named in its own env prefix", cmd, out)
		}
		if strings.Contains(out, "allow") {
			t.Errorf("cmd %q: emitted %s, which contains an allow decision", cmd, out)
		}
	}
}
