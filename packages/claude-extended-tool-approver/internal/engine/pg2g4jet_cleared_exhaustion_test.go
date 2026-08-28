// SUBSTITUTIONCLEARED+PROVENANCEEXHAUSTION ABSTAIN SUITE (pg2-g4jet, operator ruling
// 2026-08-28): for a command-substitution body cmdparse's static allowlist CLEARS
// (`seq`, `mktemp`, and any other body no rule models standalone), COMMAND position
// (`echo $(BODY)`) no longer floors to Ask via engine.commandSubstitutionFloor —
// engine.foldSubstitutionScan's `case cmdparse.SubstitutionCleared:` branch that did
// that (pg2-whumr, ADR 0048) is removed. The operator's ruling was that recursion has
// genuinely no opinion — positive or negative — on this cohort in EITHER the use-site
// (`echo $(mktemp -d)`) or the assignment (`x=$(mktemp -d)`) form, and at execution
// time the two forms are identical (run the command once, capture its output), so
// there is no basis for them to diverge: command position must now abstain too,
// matching the bare assignment-only leaf's existing abstain (which, for a
// SubstitutionCleared body specifically, arrives via engine.go's separate
// pg2-mtnmb "nothing but env assignments no rule owns" floor overriding
// cmdparse's ExpansionSafeCmd fast-path Approve — not via envvars.go's
// exhaustionOnly/pg2-et8ns relief, which only ever applies to an
// ExpansionUnknown value such as `x=$(mount)`; measured directly below rather
// than assumed) instead of being raised above it.
//
// This file is deliberately narrow and pairs the ONE relation the ruling changed with
// the ONE relation it explicitly did NOT — a SubstitutionRefused body (off the curated
// per-command allowlist entirely, e.g. `git show HEAD`'s textconv/external-diff RCE
// surface, or `paste`, which this bead's own "Correction" measured structurally
// identical to `git show HEAD` here and explicitly out of scope) still floors
// UNCONDITIONALLY, regardless of what recursion concludes about the bare form. Without
// this second half sitting right beside the first, a future retune of
// commandSubstitutionFloor's call site could widen this relief past
// SubstitutionCleared with nothing here to catch it.
package engine_test

import (
	"fmt"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// assignmentOnlyCmd builds the OTHER half of the relation this bead reconciles: a bare
// assignment-only leaf — `x=$(BODY)`, nothing else on the leaf, so nothing is executed
// besides BODY itself. This is deliberately NOT whumr_position_relation_test.go's
// envValuePositionCmd (`X=$(BODY) echo hi`, a PREFIX assignment in front of a REAL
// trailing command): that shape is a different, pre-existing mechanism entirely —
// cmdparse's classifyExpansion/ExpansionSafeCmd static fast path treats a value that
// IS a single SubstitutionCleared body as positively cleared and skips recursion
// altogether, independent of whether any rule actually approves the bare form — and it
// is unrelated to and unwidened by this bead (measured: `X=$(mktemp -d) echo hi`
// reaches Approve via safe-commands' own approval of the trailing `echo hi`, same as
// before). The bead's own illustrative "assignment" form is the bare leaf, and that is
// what this helper builds.
func assignmentOnlyCmd(body string) string { return fmt.Sprintf("x=$(%s)", body) }

// TestPg2G4jet_SubstitutionClearedExhaustionAbstainsInCommandPosition pins the relief
// itself: a SubstitutionCleared body recursion has no opinion on (ProvenanceExhaustion)
// must reach the SAME abstain in command position as it already does in the bare
// assignment-only position — the asymmetry pg2-g4jet's operator ruling closed.
// commandPositionCmd and provenanceInput are shared helpers from
// whumr_position_relation_test.go and verdict_provenance_test.go (same package).
func TestPg2G4jet_SubstitutionClearedExhaustionAbstainsInCommandPosition(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	for _, body := range []string{"mktemp -d", "seq 1 3"} {
		t.Run(body, func(t *testing.T) {
			cmdV := eng.EvaluateHook(provenanceInput(projectRoot, commandPositionCmd(body)))
			assignV := eng.EvaluateHook(provenanceInput(projectRoot, assignmentOnlyCmd(body)))
			if cmdV.Decision != hookio.NoOpinion {
				t.Errorf("command position %q = %s (%s: %s); want abstain (NoOpinion) — pg2-g4jet removed the Ask floor for this cohort",
					commandPositionCmd(body), cmdV.Decision, cmdV.Module, cmdV.Reason)
			}
			if assignV.Decision != hookio.NoOpinion {
				t.Errorf("assignment-only position %q = %s (%s: %s); want abstain (NoOpinion) — unaffected by this bead either way",
					assignmentOnlyCmd(body), assignV.Decision, assignV.Module, assignV.Reason)
			}
			if cmdV.Decision != assignV.Decision {
				t.Errorf("body %q: command position %s != assignment-only position %s; pg2-g4jet's whole point was to align the two, not just each individually",
					body, cmdV.Decision, assignV.Decision)
			}
		})
	}
}

// TestPg2G4jet_SubstitutionRefusedStillFloorsUnconditionally is the regression guard
// against this relief accidentally widening past SubstitutionCleared: a body the seam
// REFUSES outright must stay floored UNCONDITIONALLY in command position, exactly as
// before this bead, however confidently recursion judges the bare form. The floor's
// own decisive value moved from Ask to Reject under pg2-kxmpe (2026-08-28, unrelated
// to and unaffected by pg2-g4jet) — this test asserts against that current value.
func TestPg2G4jet_SubstitutionRefusedStillFloorsUnconditionally(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	bodies := []string{
		// textconv/external-diff RCE surface (ADR 0048) — deliberately excluded from
		// the curated per-command substitution allowlist; recursion's bare Approve for
		// `git show HEAD` must never leak through the substitution.
		"git show HEAD",
		// `paste` is Approved standalone (safe-commands' allowlist, given an in-zone
		// file) but is NOT on cmdparse's curated per-command SUBSTITUTION allowlist —
		// this bead's own "Correction" measured it structurally identical to
		// `git show HEAD` here (falls through classifySubstitutionCommand's final
		// `return SubstitutionRefused`), and ruled it explicitly out of THIS bead's
		// scope (tracked separately, pg2-iuapn).
		"paste go.mod",
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			got := eng.EvaluateHook(provenanceInput(projectRoot, commandPositionCmd(body)))
			// pg2-kxmpe (2026-08-28, landed after this test) raises the SubstitutionRefused
			// floor itself from Ask to Reject — unrelated to and unaffected by pg2-g4jet,
			// which this test guards. The floor being UNCONDITIONAL is what this test pins;
			// its exact decisive value moved out from under it via a separate ruling.
			if got.Decision != hookio.Reject {
				t.Errorf("%q = %s (%s: %s); want Reject — pg2-g4jet must NOT touch the SubstitutionRefused floor",
					commandPositionCmd(body), got.Decision, got.Module, got.Reason)
			}
		})
	}
}
