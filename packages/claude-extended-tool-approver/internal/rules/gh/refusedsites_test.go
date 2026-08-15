package gh

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE LAST FOUR SITES OF THE ADR 0044 REFUSAL CENSUS (pg2-qxe85). Twelve of the sixteen
// landed in wave 2; these four were left because gh's are the ones where the reading is
// least mechanical, and one of them was actively disputed.
//
// THE DISTINCTION BEING PINNED, in the terms ADR 0044 uses:
//
//   - EXHAUSTION (ErrNotApplicable) means NO rule owned this leaf. It is the half a
//     consumer may CLEAR, and a later rule may still APPROVE it.
//   - REFUSAL (ErrRefused) means a rule KNEW this command family, EXAMINED this
//     invocation, and would not clear it. It is a NoOpinion FLOOR: the chain continues and
//     a later Ask or Reject still wins, but nothing can approve it.
//
// So the conversion is not cosmetic. Reported as an exhaustion, `gh run rerun` on a
// foreign branch was indistinguishable from a command no rule ever looked at — after this
// rule had spent TWO subprocess resolutions establishing exactly why it would not clear
// it.
//
// THE `gh pr merge --auto` SITE WAS THE DISPUTED ONE, and it is included deliberately.
// A prior agent argued it documents a genuine Abstain because the branch reads as
// "allowed", and that reading is not unreasonable — but it misses what the surrounding
// comment asserts: `--auto` is safe ONLY because `gh pr ready` Asks, and "the two together
// ARE the gate". An exhaustion leaves a later rule free to APPROVE the merge, which would
// delete the gh-side half of that gate while the comment went on claiming it. A refusal is
// the floor that cannot happen to. The invariant is now enforced rather than merely
// described.
//
// WHAT MUST *NOT* BECOME A REFUSAL is asserted here too, because that is the half a
// blanket conversion gets wrong: gh's three remaining not-applicables are genuine
// exhaustions (a non-Bash tool, a `gh` subcommand this rule has no opinion on, and a
// command that is not `gh` at all), and each must stay clearable.

func TestADR0044_GH_RefusedSites(t *testing.T) {
	sameBranch := &stubResolver{currentBranch: "feat", runBranch: "feat"}
	otherBranch := &stubResolver{currentBranch: "feat", runBranch: "main"}

	for _, tt := range []struct {
		site        string
		cmd         string
		rule        *Rule
		wantRefused bool
	}{
		// THE FOUR CONVERTED SITES.
		{
			site:        "pr merge --auto defers to the gh pr ready Ask",
			cmd:         "gh pr merge --auto 123",
			rule:        New(nil),
			wantRefused: true,
		},
		{
			site:        "run rerun with no run ID to resolve",
			cmd:         "gh run rerun",
			rule:        New(sameBranch),
			wantRefused: true,
		},
		{
			site:        "run rerun with no resolver configured",
			cmd:         "gh run rerun 456",
			rule:        New(nil),
			wantRefused: true,
		},
		{
			site:        "run rerun for a run on a different branch",
			cmd:         "gh run rerun 456",
			rule:        New(otherBranch),
			wantRefused: true,
		},
		// THE GENUINE EXHAUSTIONS, which must stay clearable. Nothing was examined in any
		// of them, so a refusal would be a false claim of provenance — and would set a
		// floor on leaves this rule has no business floor-setting.
		{
			site:        "exhaustion — not a gh command at all",
			cmd:         "ls -la",
			rule:        New(nil),
			wantRefused: false,
		},
		{
			site:        "exhaustion — a gh subcommand this rule has no opinion on",
			cmd:         "gh gist create /tmp/x",
			rule:        New(nil),
			wantRefused: false,
		},
	} {
		t.Run(tt.site, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.cmd})}
			res, err := tt.rule.Evaluate(input)
			gotRefused := errors.Is(err, hookio.ErrRefused)
			if gotRefused != tt.wantRefused {
				t.Fatalf("%q: refused=%v, want %v (err=%v, res=%+v)", tt.cmd, gotRefused, tt.wantRefused, err, res)
			}
			if !tt.wantRefused {
				// An exhaustion still has to BE one, or the leaf is a failure rather than a
				// pass-through.
				if !errors.Is(err, hookio.ErrNotApplicable) {
					t.Errorf("%q: expected an exhaustion but err=%v", tt.cmd, err)
				}
				return
			}
			if res.Decision < hookio.NoOpinion {
				t.Errorf("%q: floor is %s, weaker than NoOpinion — a refusal must never be less restrictive than the abstain it replaced", tt.cmd, res.Decision)
			}
			if res.Reason == "" {
				t.Errorf("%q: refusal carries no Reason; the restored text is the only record of WHY (ADR 0044)", tt.cmd)
			}
			if res.Module != tt.rule.Name() {
				t.Errorf("%q: refusal Module = %q, want %q — provenance needs the refusing rule's identity", tt.cmd, res.Module, tt.rule.Name())
			}
			// A refusal MUST still read as not-applicable to an un-upgraded consumer, which
			// is what makes the conversion test-compatible (ADR 0044's subtype claim).
			if !errors.Is(err, hookio.ErrNotApplicable) {
				t.Errorf("%q: refusal does not match ErrNotApplicable; the chain would treat it as a FAILURE", tt.cmd)
			}
		})
	}
}

// TestADR0044_GH_RefusalNeverWeakensADecisiveVerdict is the direction a census conversion
// is most likely to break: a refusal is a FLOOR, so it must not displace an Approve the
// rule genuinely makes, and it must not soften a Reject.
//
// `gh pr merge` WITHOUT `--auto` is the sharp case — it sits in the same branch as the
// converted site and must still REJECT — and the read-only verbs are the sharp case in the
// other direction.
func TestADR0044_GH_RefusalNeverWeakensADecisiveVerdict(t *testing.T) {
	sameBranch := &stubResolver{currentBranch: "feat", runBranch: "feat"}

	for _, tt := range []struct {
		cmd  string
		rule *Rule
		want hookio.Decision
		why  string
	}{
		{"gh pr merge 123", New(nil), hookio.Reject, "an immediate merge bypasses the draft-first landing flow"},
		{"gh pr view 123", New(nil), hookio.Approve, "a read-only verb keeps its approval"},
		{"gh run list", New(nil), hookio.Approve, "a read-only verb keeps its approval"},
		{"gh run rerun 456", New(sameBranch), hookio.Approve, "a rerun of the CURRENT branch is still approved"},
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.cmd})}
		res, err := tt.rule.Evaluate(input)
		if err != nil {
			t.Errorf("%q: unexpected err=%v — this leaf must be a decisive verdict, not a refusal or an exhaustion (%s)", tt.cmd, err, tt.why)
			continue
		}
		if res.Decision != tt.want {
			t.Errorf("%q: got %s (%s), want %s — %s", tt.cmd, res.Decision, res.Reason, tt.want, tt.why)
		}
	}
}

// TestADR0044_GH_CensusIsComplete is the STRUCTURAL guard, and it is what makes the census
// finished rather than merely advanced.
//
// Every converted site kept its restored text as the live Reason, so the marker the census
// used — a comment reading "Former Reason, kept because it is the only record of WHY:" —
// must no longer appear anywhere beside a return in this rule. Asserting it through the
// BEHAVIOUR rather than by reading the source keeps the check honest: each refusal must
// carry a non-empty Reason naming this module, which is precisely what the marker was a
// placeholder for.
func TestADR0044_GH_CensusIsComplete(t *testing.T) {
	// Every site that the census converted, exercised through the rule, must produce a
	// refusal whose Reason survived — not an empty floor.
	for _, tc := range []struct {
		cmd  string
		rule *Rule
	}{
		{"gh pr merge --auto 123", New(nil)},
		{"gh run rerun", New(&stubResolver{currentBranch: "feat", runBranch: "feat"})},
		{"gh run rerun 456", New(nil)},
		{"gh run rerun 456", New(&stubResolver{currentBranch: "feat", runBranch: "main"})},
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tc.cmd})}
		res, err := tc.rule.Evaluate(input)
		if !errors.Is(err, hookio.ErrRefused) {
			t.Errorf("%q: not a refusal (err=%v) — the census is incomplete for this site", tc.cmd, err)
			continue
		}
		if len(res.Reason) < 20 {
			t.Errorf("%q: Reason %q is too short to be the restored text — a placeholder floor loses exactly what the census set out to preserve", tc.cmd, res.Reason)
		}
	}
}
