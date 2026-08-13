package engine

// I1b — the fail-safe PARSE floor — and I10's reason-string contract.
//
// Both first become LIVE in ADR 0039 step 2 (pg2-fez3d). Step 1 (pg2-jxmk9) ran the
// seam in SHADOW with the outgoing front end still authoritative, so a parse failure
// could not reach a verdict and NEITHER invariant was testable: the outgoing byte
// scanners had no notion of "this command does not parse", they always produced some
// leaf set, and `LogShadowDisagreement` returned nothing.

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// alwaysRejectRule stands for any rule that would have earned a Reject on a leaf.
// It exists to make the FORFEITURE observable: under I1b no leaf is examined, so this
// rule is never consulted and its Reject is given up.
type alwaysRejectRule struct{ consulted int }

func (r *alwaysRejectRule) Name() string { return "always-reject" }

func (r *alwaysRejectRule) Evaluate(*hookio.HookInput) (hookio.RuleResult, error) {
	r.consulted++
	return hookio.RuleResult{Decision: hookio.Reject, Reason: "would have rejected", Module: r.Name()}, nil
}

// alwaysApproveRule stands for a rule that would have approved every leaf. It is what
// makes the floor's DIRECTION assertable: with this chain, a parseable command
// approves, so an unparseable one demoting to NoOpinion is the floor firing and not
// an artifact of nobody having an opinion.
type alwaysApproveRule struct{}

func (r *alwaysApproveRule) Name() string { return "always-approve" }

func (r *alwaysApproveRule) Evaluate(*hookio.HookInput) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "approved", Module: r.Name()}, nil
}

// TestEngine_UnparseableCommandFoldsToAbstain is I1b and I10 together.
//
// I1b: a WHOLE-COMMAND parse failure MUST yield Abstain. I10: CETA MUST NOT Approve
// a command the bash parser could not parse.
//
// The chain here APPROVES everything, so a leaf reaching it is an `allow`. Every row
// below must still come back NoOpinion, which is only possible if the floor fires.
func TestEngine_UnparseableCommandFoldsToAbstain(t *testing.T) {
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}
	for _, src := range []string{
		`echo "unclosed`,                   // unbalanced double quote
		"echo 'unclosed",                   // unbalanced single quote
		"cat <<EOF\nbody never terminated", // unterminated here-document
		"echo $(oops",                      // unterminated command substitution
		"for f in a b; do echo hi",         // loop with no `done`
		"if true; then echo hi",            // conditional with no `fi`
		"FOO=(a b) cmd",                    // "inline variables cannot be arrays"
		"echo $((cd /x && ls) | jq .)",     // bash's `$( (list) )` fallback, unmodelled
		"echo ${(f)var}",                   // a zsh-only construct
	} {
		t.Run(src, func(t *testing.T) {
			e := New(&alwaysApproveRule{})
			got := e.EvaluateExpression(src, nil, origin)
			if got.Decision == hookio.Approve {
				t.Fatalf("I10 violated: an unparseable command was APPROVED (%q)", got.Reason)
			}
			if got.Decision != hookio.NoOpinion {
				t.Errorf("Decision = %v, want NoOpinion (I1b's floor)", got.Decision)
			}
			if !strings.Contains(got.Reason, "unparseable command") {
				t.Errorf("Reason = %q, want it to name the parse failure", got.Reason)
			}
			if got.Module != "engine" {
				t.Errorf("Module = %q, want engine", got.Module)
			}
		})
	}

	// CONTROL. The same chain on a PARSEABLE command approves, so the rows above are
	// the floor firing rather than the chain being silent.
	t.Run("control: a parseable command still approves", func(t *testing.T) {
		e := New(&alwaysApproveRule{})
		if got := e.EvaluateExpression("echo hi && ls", nil, origin); got.Decision != hookio.Approve {
			t.Errorf("Decision = %v, want Approve", got.Decision)
		}
	})
}

// TestEngine_UnparseableFloorIsAFoldNotAnEarlyReturn pins the MECHANISM ADR 0039's
// I1b names, not merely the outcome: the floor is applied as a `MostRestrictive`
// FOLD, never as an early return.
//
// The distinction is order-independence, and it is testable because a fold is
// DOMINATED by a more restrictive sibling while an early return is not. The engine
// cannot produce a sibling leaf for an unparseable command — that is the forfeiture —
// so the property is asserted the other way round: the floor value is folded into the
// running result, so a fold that ALREADY held Reject would keep it. Here the seed is
// Approve, so the observable form is that the floor lands ON the fold and the loop is
// still entered (over zero leaves) rather than the function returning from the parse
// check.
func TestEngine_UnparseableFloorIsAFoldNotAnEarlyReturn(t *testing.T) {
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	// A parse failure and NO leaves: the result is exactly the floor, and it is NOT
	// the bare `NoOpinion` with an empty reason that the "parsed but empty" branch
	// returns. Those two must stay distinguishable — "there is nothing here" and "I
	// could not read this" are different answers and only the second is a floor.
	e := New(&alwaysApproveRule{})
	unparseable := e.EvaluateExpression(`echo "unclosed`, nil, origin)
	empty := e.EvaluateExpression("   ", nil, origin)
	if unparseable.Reason == "" {
		t.Error("the unparseable floor must carry a reason a user can read")
	}
	if empty.Reason != "" {
		t.Errorf("the empty-command branch must stay reasonless, got %q", empty.Reason)
	}
	if unparseable.Decision != empty.Decision {
		t.Errorf("both are NoOpinion; got %v and %v", unparseable.Decision, empty.Decision)
	}

	// ORDER INDEPENDENCE, asserted where it is observable: the same failing text
	// yields the same verdict whichever position it occupies, because nothing about
	// the floor depends on how far the walk got.
	for _, src := range []string{
		`echo "unclosed ; echo hi`,
		`echo hi ; echo "unclosed`,
	} {
		if got := e.EvaluateExpression(src, nil, origin); got.Decision != hookio.NoOpinion {
			t.Errorf("EvaluateExpression(%q) = %v, want NoOpinion", src, got.Decision)
		}
	}
}

// TestEngine_UnparseableForfeitsALeafReject is the COST of I1b, asserted rather than
// only documented.
//
// I1b is STRONGER than I1a's scan floor: I1a fires with the sibling leaves still
// evaluated, so a Reject one of them earned survives the fold. I1b fires with NO LEAF
// EXAMINED, so any Reject a leaf would have earned is FORFEITED — a movement in the
// MORE PERMISSIVE direction on `Approve < NoOpinion < Ask < Reject`, even though it
// can never reach Approve. ADR 0039's replay gate is worded as "no transition in the
// LESS-RESTRICTIVE direction" precisely so this is caught rather than passing
// silently, and every such corpus row is reported individually as a forfeiture.
//
// The rule below rejects everything, so the parseable control Rejects; the
// unparseable row demotes to NoOpinion and the rule is never consulted at all.
func TestEngine_UnparseableForfeitsALeafReject(t *testing.T) {
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	control := &alwaysRejectRule{}
	if got := New(control).EvaluateExpression("rm -rf /etc", nil, origin); got.Decision != hookio.Reject {
		t.Fatalf("control Decision = %v, want Reject", got.Decision)
	}
	if control.consulted == 0 {
		t.Fatal("control: the rule was never consulted, so the forfeiture below proves nothing")
	}

	forfeited := &alwaysRejectRule{}
	got := New(forfeited).EvaluateExpression(`rm -rf /etc "unclosed`, nil, origin)
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Decision = %v, want NoOpinion — the Reject is forfeited, not kept", got.Decision)
	}
	if forfeited.consulted != 0 {
		t.Errorf("the rule was consulted %d times; under I1b NO leaf is examined", forfeited.consulted)
	}
}

// TestEngine_UnparseableReasonHonoursI10 pins I10's two-sided reason contract.
//
// "Where the parser attributes the failure to zsh, the reason SHOULD name the
// dialect; where it does not, the reason MUST report the failure WITHOUT guessing at
// a cause."
//
// The second half is the one that needs a test, because guessing is the tempting
// behaviour: CETA receives NO shell field in its hook input and can never establish
// which dialect will run, so naming one on an unattributed failure would put
// fabricated provenance on a user-facing prompt.
func TestEngine_UnparseableReasonHonoursI10(t *testing.T) {
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}
	e := New(&alwaysApproveRule{})

	t.Run("attributed: the reason names the dialect", func(t *testing.T) {
		// `${(f)var}` — parameter expansion FLAGS — is zsh-only, and the parser returns
		// a LangError naming the variants that DO support it.
		got := e.EvaluateExpression("echo ${(f)var}", nil, origin)
		if got.Decision != hookio.NoOpinion {
			t.Fatalf("Decision = %v, want NoOpinion", got.Decision)
		}
		if !strings.Contains(got.Reason, "zsh") {
			t.Errorf("Reason = %q, want it to name zsh (the parser attributed the failure)", got.Reason)
		}
		if !strings.Contains(got.Reason, "valid in") {
			t.Errorf("Reason = %q, want it to say where the construct IS valid", got.Reason)
		}
	})

	t.Run("unattributed: the reason names no dialect at all", func(t *testing.T) {
		for _, src := range []string{
			`echo "unclosed`,
			"cat <<EOF\nbody never terminated",
			"for f in a b; do echo hi",
		} {
			got := e.EvaluateExpression(src, nil, origin)
			if got.Decision != hookio.NoOpinion {
				t.Fatalf("EvaluateExpression(%q) = %v, want NoOpinion", src, got.Decision)
			}
			for _, guess := range []string{"zsh", "bash", "mksh", "bats", "posix", "dialect"} {
				if strings.Contains(strings.ToLower(got.Reason), guess) {
					t.Errorf("EvaluateExpression(%q) reason %q names %q; the parser attributed NO cause, so none may be guessed",
						src, got.Reason, guess)
				}
			}
			if !strings.Contains(got.Reason, "unparseable command") {
				t.Errorf("EvaluateExpression(%q) reason %q does not report the failure at all", src, got.Reason)
			}
		}
	})
}
