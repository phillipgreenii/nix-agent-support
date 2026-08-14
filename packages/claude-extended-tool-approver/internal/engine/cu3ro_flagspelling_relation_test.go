// cu3ro_flagspelling_relation_test.go — pg2-cu3ro asserted END TO END, through the
// production rule chain, as a RELATION across the THREE SPELLINGS of one flag.
//
// THE RELATION: for any path P and any file-taking flag F, `cmd F=P`, `cmd F P` and the
// short form must reach the SAME verdict.
//
// It is stated as an equality rather than as pinned verdicts on purpose. The verdicts here
// are still moving — pg2-ygjs5 will strengthen the grep/rg rows, and pg2-9zgso will change
// what a QUOTED glued value resolves to — but the relation is invariant under both, because
// what it forbids is a SPELLING-DEPENDENT gate. That is the whole defect class: `gh
// --body-file=` satisfied it while `git --file=` violated it, so the coverage was incidental
// to which spelling the caller happened to use.
//
// WHY THE GLUED FORM WAS THE ONE THAT LEAKED. `--file=<path>` is ONE argv token, so a caller
// skipping any token that begins with `-` — correct for a flag NAME, which is not a filename —
// discarded the VALUE with the name. The space form survived only because the path was a
// SEPARATE token. Measured on main @6737a0ea: `git commit --file=~/.ssh/id_rsa` ALLOW against
// `-F` and `--file ` both DENY.
package engine_test

import (
	"strings"
	"testing"
)

// TestCu3ro_GluedFlagValueMatchesTheSpaceSpelling asserts the relation over a matrix of
// commands x file-taking flags x paths. The matrix rather than hand-picked rows is the point:
// the defect is generic to the token shape, so any command with any file-taking flag has it.
func TestCu3ro_GluedFlagValueMatchesTheSpaceSpelling(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	// Each entry is a command prefix plus a flag whose VALUE the command OPENS. The three
	// spellings are then generated mechanically so none can be forgotten.
	cases := []struct{ prefix, long, short string }{
		{"git commit", "--file", "-F"},
		{"gh pr create", "--body-file", ""},
		{"gh pr comment 1", "--body-file", "-F"},
		{"bd comment x", "--file", ""},
		{"cat", "--file", ""},
	}
	// Paths the SECRETS rule owns — deny-listed or secret-shaped. pg2-cu3ro's fix operates
	// there, so the relation must hold exactly.
	//
	// PATHS THAT ARE ONLY OUT OF ZONE ARE DELIBERATELY ABSENT, and their absence is the
	// recorded boundary of this fix rather than an oversight — see the exception block below.
	paths := []string{
		"/Users/testuser/.ssh/id_rsa",
		"secrets/notes.txt", ".env",
		projectRoot + "/NOTES.md",
	}

	for _, c := range cases {
		for _, p := range paths {
			glued := c.prefix + " " + c.long + "=" + p
			spaced := c.prefix + " " + c.long + " " + p
			gv := eng.EvaluateHook(provenanceInput(projectRoot, glued))
			sv := eng.EvaluateHook(provenanceInput(projectRoot, spaced))
			if gv.Decision != sv.Decision {
				t.Errorf("FLAG-SPELLING DISAGREEMENT: %q is %s but %q is %s — the same file named the same way must not depend on the `=` spelling\n  glued reason:  %s\n  spaced reason: %s",
					glued, gv.Decision, spaced, sv.Decision, gv.Reason, sv.Reason)
			}
			if c.short == "" {
				continue
			}
			shortForm := c.prefix + " " + c.short + " " + p
			shv := eng.EvaluateHook(provenanceInput(projectRoot, shortForm))
			if shv.Decision != sv.Decision {
				t.Errorf("FLAG-SPELLING DISAGREEMENT: %q is %s but %q is %s — the short and long spellings of one flag must agree\n  short reason: %s\n  long reason:  %s",
					shortForm, shv.Decision, spaced, sv.Decision, shv.Reason, sv.Reason)
			}
		}
	}
}

// TestCu3ro_ZoneOnlyPathsStillDisagreeBySpelling records the HALF pg2-cu3ro DID NOT FIX, as a
// failing-when-fixed assertion rather than as a comment, so the gap is visible in the suite
// instead of living only in a bead.
//
// pg2-cu3ro routed the glued value into the SECRETS rule (the deny-list). safecmds' ZONE model
// has ELEVEN separate `strings.HasPrefix(a, "-")` skips of its own, so a path that is merely
// OUT OF ZONE — not deny-listed — is still invisible in the glued spelling. That is pg2-wxbr9,
// and it is the pg2-zpct4 shape once more: two path models disagreeing about one command.
//
// WHEN pg2-wxbr9 LANDS THIS TEST MUST FAIL, and deleting it is that bead's acceptance signal.
// It is written to assert the CURRENT wrong behaviour on purpose; a reader must not "fix" it by
// relaxing the assertion.
func TestCu3ro_ZoneOnlyPathsStillDisagreeBySpelling(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	// Out-of-zone but NOT deny-listed, so the secrets rule has no opinion and only the zone
	// model would gate them.
	for _, p := range []string{"/etc/shadow", "/Users/testuser/.aws/credentials"} {
		glued := eng.EvaluateHook(provenanceInput(projectRoot, "cat --file="+p))
		spaced := eng.EvaluateHook(provenanceInput(projectRoot, "cat --file "+p))
		if glued.Decision == spaced.Decision {
			t.Errorf("pg2-wxbr9 APPEARS FIXED for %q (glued %s == spaced %s) — if that is intended, DELETE this test; it exists only to keep the known gap visible",
				p, glued.Decision, spaced.Decision)
		}
	}
}

// TestCu3ro_MessageFlagValuesAreStillSkippedInBothSpellings is the OPPOSITE-DIRECTION half,
// and it is the one that stops the fix from being a regression.
//
// pg2-cu3ro and pg2-ia640.5 pull in OPPOSITE directions on the SAME token shape: this bead
// says an `--opt=value` value must be TESTED, and that bead says a MESSAGE value must be
// SKIPPED. Both are right, because the discriminator is not the token shape but whether the
// command OPENS the value. So the two are pinned in one adjacent table, as pg2-cu3ro's
// acceptance criteria require.
//
// The prose values below all NAME credential paths and must still not be gated: a message is
// STORED AS TEXT, so a path spelled inside one grants no access, while prompting on it costs
// the human a paragraph-length retype (the ~40-line bead comment of asklog row 325419).
func TestCu3ro_MessageFlagValuesAreStillSkippedInBothSpellings(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	// prose deliberately mentions a deny-listed path AND a generic secrets component.
	const prose = "'see the agent note under secrets/prod.env for context'"

	for _, c := range []struct{ prefix, flag string }{
		{"git commit", "--message"},
		{"bd close x", "--reason"},
		{"bd close x", "--notes"},
		{"bd create", "--title"},
		{"bd create", "--description"},
		{"gh pr comment 1", "--body"},
	} {
		glued := c.prefix + " " + c.flag + "=" + prose
		spaced := c.prefix + " " + c.flag + " " + prose
		gv := eng.EvaluateHook(provenanceInput(projectRoot, glued))
		sv := eng.EvaluateHook(provenanceInput(projectRoot, spaced))
		if gv.Decision.String() == "reject" {
			t.Errorf("MESSAGE CARVE-OUT REGRESSED (glued): %q is %s — a message value is stored as text, never opened, so gating it is the pg2-ia640.5 false positive\n  reason: %s",
				glued, gv.Decision, gv.Reason)
		}
		if gv.Decision != sv.Decision {
			t.Errorf("MESSAGE SPELLING DISAGREEMENT: %q is %s but %q is %s — the carve-out must cover both spellings\n  glued reason:  %s\n  spaced reason: %s",
				glued, gv.Decision, spaced, sv.Decision, gv.Reason, sv.Reason)
		}
	}
}

// TestCu3ro_GluedValueDoesNotResurrectSkippedOperands guards the third direction: the fix
// must not undo the grep/rg and jq operand skips, which are keyed on the SPACE spelling and
// whose glued spellings now flow through the same value extraction.
//
// A pattern or glob is not a file the command opens, so these must stay ungated in BOTH
// spellings — the pg2-ia640.2 false positives.
func TestCu3ro_GluedValueDoesNotResurrectSkippedOperands(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	for _, cmd := range []string{
		"rg --glob=*.env pat /tmp",
		"rg -g *.env pat /tmp",
		"grep --include=*.env pat /tmp",
		"grep --exclude=*.env pat /tmp",
		"jq --arg dir=/app/src .",
		"ls --color=always",
		"git commit -m=x",
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() == "reject" {
			t.Errorf("SKIPPED OPERAND RESURRECTED: %q is %s — a pattern/glob/literal is not a file the command opens, and gating it is the pg2-ia640.2 false positive\n  reason: %s",
				cmd, got.Decision, got.Reason)
		}
		if !strings.Contains(cmd, "=") && !strings.Contains(cmd, " -g ") {
			t.Fatalf("test bug: %q exercises neither a glued nor a space value spelling", cmd)
		}
	}
}
