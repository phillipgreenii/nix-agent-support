// ENVVARS DYNAMIC-PATH-READ RELIEF SUITE (pg2-4x2mu), driven through the REAL
// composed rule chain (setup.RuleChain via buildFullEngine).
//
// It lives in `package engine_test` for the same reason engine_integration_test.go's
// header and verdict_provenance_test.go's header both give: the chain must BE
// production's, because the claim under test is about what the WHOLE chain does
// with a leaf, not about envvars or safe-commands in isolation.
//
// WHY A FAKEEVALUATOR-ONLY TEST CANNOT SUBSTITUTE FOR THIS ONE. envvars' own unit
// tests (internal/rules/envvars/envvars_test.go) drive evaluateAssignment
// directly with a fakeEvaluator that returns a fixed hookio.RuleResult per body —
// which proves envvars' OWN switch logic is right, but never exercises the
// engine-level fold that PRODUCES that RuleResult in the first place. A leaf that
// only safe-commands examines, with every other rule answering "not applicable",
// ties its refusal against the engine's OWN manufactured loop-exhaustion seed
// inside hookio.MostRestrictive — and that tie-merge is exactly where this bead's
// development caught a real defect: the merge's first draft ANDed the bare
// RefusalCategory values, which read the manufactured seed's zero-value "no
// opinion" as an affirmative "not dynamic-path-read" and silently defeated the
// relief on precisely this single-refusing-rule shape. Fixed in
// hookio.mergeRefusalCategory (now Provenance-aware) and in hookio.Verdict's
// ErrRefused branch (which had the identical gap for the one-rule-chain
// simulation every other unit test in this tree uses). This suite is the
// regression guard for BOTH fixes acting together, end to end.
package engine_test

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestEnvVars_DynamicPathReadRelief_RealChain is pg2-4x2mu's acceptance case,
// reproduced against the production chain rather than a hand-picked subset.
func TestEnvVars_DynamicPathReadRelief_RealChain(t *testing.T) {
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	tests := []struct {
		name string
		cmd  string
		want hookio.Decision
	}{
		{
			// A BARE assignment-only leaf ("nothing but env assignments, nothing
			// executed") lands at NoOpinion/abstain rather than Approve even when
			// EVERY assignment clears — engine.go's pg2-mtnmb floor ("env
			// assignments only, no rule has an opinion") forces that regardless of
			// this bead, to keep a parser-desync phantom NAME=VALUE from silently
			// becoming `allow`. The bead's own Verification section anticipates
			// exactly this landing spot ("Ask -> Approve/NoOpinion"): the relief's
			// job is only to stop envvars ITSELF escalating to Ask, not to force an
			// unrelated floor into Approve.
			name: "assignment-only capture whose only refusal is a dynamic-path read is relieved to abstain",
			cmd:  `out=$(cat "$dynamic/path")`,
			want: hookio.NoOpinion,
		},
		{
			name: "leading-assignment form beside a real command clears to approve",
			cmd:  `out=$(cat "$dynamic/path") echo hi`,
			want: hookio.Approve,
		},
		{
			name: "the bead's own motivating shape: a different reader, same relief",
			cmd:  `out=$(jq -r .x "$dynamic/path") echo hi`,
			want: hookio.Approve,
		},
		{
			name: "mixing a dynamic-path read with a mutating command WITHIN one substitution body still asks",
			cmd:  `out=$(cat "$p"; rm -rf /etc)`,
			want: hookio.Ask,
		},
		{
			name: "mixing a dynamic-path read with a known-bad literal path ACROSS substitutions still asks",
			cmd:  `out=$(cat "$p")$(cat /etc/shadow)`,
			want: hookio.Ask,
		},
		{
			name: "a capture whose only refusal is an unrelated category still asks",
			cmd:  `out=$(rm -rf /etc)`,
			want: hookio.Ask,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eng.EvaluateHook(provenanceInput(projectRoot, tt.cmd))
			if got.Decision != tt.want {
				t.Errorf("cmd %q: got %s (%s), want %s", tt.cmd, got.Decision, got.Reason, tt.want)
			}
		})
	}
}

// TestEnvVars_DynamicPathReadRelief_CommandPositionUnchanged pins pg2-2ke04's own
// dynamic-path refusal, for the SAME command NOT captured into a variable,
// exactly as before this bead — the relief applies only inside a captured
// substitution body, never to command position.
func TestEnvVars_DynamicPathReadRelief_CommandPositionUnchanged(t *testing.T) {
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	got := eng.EvaluateHook(provenanceInput(projectRoot, `cat "$dynamic/path"`))
	if got.Decision == hookio.Approve {
		t.Errorf(`bare "cat \"$dynamic/path\"" = approve (%s); pg2-2ke04's command-position refusal must stay unchanged`, got.Reason)
	}
}
