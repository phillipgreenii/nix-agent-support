// PATH-MODEL RELATION SUITE (pg2-zpct4): the two path models' reconciliation asserted
// END TO END, through the production rule chain, as a RELATION between two spellings of
// one read.
//
// THE RELATION: for any path, capturing a read into an env-var value is never LESS gated
// than writing the same read as a bare command.
//
// It is a separate file from the chain-level suites on purpose: this is not a row in a
// verdict table, it is the invariant that makes such a table safe to retune. The hole it
// closes was found by pg2-d0ja3's provenance fuzz target and was PRE-EXISTING — measured
// identical on the base commit — so nothing here is a regression test for a change; it is
// the guard that stops the two path models drifting apart again.
//
// WHY IT NEEDS THE WHOLE CHAIN. The disagreement lived BETWEEN two components, so neither
// unit test can see it. cmdparse's static substitution seam screened argv through
// `secretpath.IsSecret` and, on a clearance, classified the value ExpansionSafeCmd — which
// skips the substitution recursion, hence skips `internal/rules/safecmds`' readPathIssue,
// hence skips `patheval`'s zone model entirely. Both components were self-consistent; the
// COMPOSITION was the defect, and only an assembled chain evaluates the composition.
// cmdparse's own TestClassifySubstitutionBody_NoContentReaderIsClearedHoldingAPath and
// safecmds' TestReadPathIssue_IsNeverLooserThanTheStaticSubstitutionSeam pin the two
// halves; this pins that they compose.
package engine_test

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestZpct4_CapturedReadIsNeverLooserThanTheBareRead asserts the relation over a matrix of
// readers x paths rather than over hand-picked rows, because the defect's shape is "some
// path nobody wrote down". Every cell is the SAME read in two spellings, so any inequality
// is a spelling-dependent gate — which is the bug class, whatever direction the models are
// later tuned in.
func TestZpct4_CapturedReadIsNeverLooserThanTheBareRead(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	// Content readers: the commands that can emit another file's bytes, i.e. the ones
	// for which path readability decides anything. Name-resolution commands
	// (readlink/realpath/basename) are excluded because their bare spellings clear any
	// path too — there is no relation for a capture to break.
	readers := []string{"cat", "head -1", "tail -1", "wc -l", "grep -c x", "jq -r .x", "yq .a", "tq .a"}
	paths := []string{
		// The class the two models disagreed about: real paths in no readable zone.
		"/etc/shadow", "/etc/passwd", "/etc/sudoers", "/", "/var/log/system.log",
		"/Users/otheruser/.aws/credentials", "~someuser/notes.txt",
		// Deny-listed paths, where the two models already agreed. Included so a future
		// change cannot fix the disagreement by weakening the half that worked.
		".env", "/Users/testuser/.ssh/id_rsa", "~/.ssh/config", "secrets/db.yaml",
		// In-zone paths. The relation must hold here too, and it holds by the seam
		// DELEGATING rather than by it guessing "readable" — so these stay approved on
		// both spellings and the reconciliation costs them nothing.
		projectRoot + "/go.mod", "./go.mod", "go.mod",
	}

	for _, r := range readers {
		for _, p := range paths {
			for _, bare := range []string{r + " " + p, r + " < " + p} {
				captured := "X=$(" + bare + ") echo hi"
				bareV := eng.EvaluateHook(provenanceInput(projectRoot, bare))
				capturedV := eng.EvaluateHook(provenanceInput(projectRoot, captured))
				if capturedV.Decision < bareV.Decision {
					t.Errorf("PATH-MODEL DISAGREEMENT: %q is %s but %q is %s — capturing the read into an env value made it LESS gated\n  captured reason: %s\n  bare reason:     %s",
						captured, capturedV.Decision, bare, bareV.Decision,
						capturedV.Reason, bareV.Reason)
				}
			}
		}
	}
}

// TestZpct4_UnclassifiablePathCannotReachApproveThroughASubstitution is the FAIL-CLOSED
// half of the acceptance criteria, and it is a different claim from the relation above.
//
// The relation says the two spellings agree. This says WHERE they agree when NEITHER model
// can place the path: at a non-Approve. It matters because "reconciled" could in principle
// have been achieved by making the bare spelling looser, and that is the one resolution the
// bead forbids. `patheval.PathUnknown.CanRead()` is false and cmdparse's seam DELEGATES
// rather than clears, so an unplaceable path is refused by the authority and there is no
// route to Approve for either spelling — which is what these rows check.
func TestZpct4_UnclassifiablePathCannotReachApproveThroughASubstitution(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	// Paths in no zone this session can read, spelled several ways so the check is not
	// about one prefix: absolute system files, a foreign home, an unknown user's home,
	// and an escape out of the project root.
	unplaceable := []string{
		"/etc/shadow", "/etc/master.passwd", "/private/var/db/x",
		"/Users/otheruser/notes.txt", "~someuser/notes.txt",
		projectRoot + "/../../../etc/shadow",
	}
	for _, p := range unplaceable {
		for _, cmd := range []string{
			"X=$(cat " + p + ") echo hi",
			"X=$(wc -l < " + p + ") echo hi",
			"echo $(cat " + p + ")",
			"cat " + p,
		} {
			got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
			if got.Decision == hookio.Approve {
				t.Errorf("FAIL-CLOSED VIOLATION: %q is APPROVED, but %q is in no readable zone — no substitution spelling may clear a path neither model can classify (reason: %s)",
					cmd, p, got.Reason)
			}
		}
	}
}
