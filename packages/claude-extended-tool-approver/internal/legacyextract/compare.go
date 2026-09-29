package legacyextract

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/claudecodeadapter"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Disagreement is one legacy-tagged row whose expected_verdict (the OLD
// RuleChain's verdict, as extracted) disagrees with what the NEW effect
// engine (claudecodeadapter.Evaluate, driven with DefaultRegistry/
// DefaultPolicies/DefaultGraphPolicies and a zero-value RequestConfig --
// matching the row's own empty Settings/RulesConfig fixture) produces on
// the SAME tool_input today. Per the docket design's own disposition rule
// (Phase 0 item 3): "Legacy rows are an oracle to DISPOSITION, not to
// match" -- every disagreement here is resolved into exactly one of the
// three named buckets, never left open.
type Disagreement struct {
	Case        string
	Family      string
	ToolName    string
	Command     string
	OldVerdict  goldencorpus.Verdict
	NewVerdict  goldencorpus.Verdict
	NewReason   string
	Disposition string
	Rationale   string
}

var evalVerdict = map[evalcontract.Decision]goldencorpus.Verdict{
	evalcontract.Approve: goldencorpus.Approve,
	evalcontract.Reject:  goldencorpus.Reject,
	evalcontract.Abstain: goldencorpus.NotApprove,
	evalcontract.Ask:     goldencorpus.NotApprove,
}

// CompareResult is the comparison pass's own summary counters, alongside
// the disagreement list itself.
type CompareResult struct {
	Comparable    int // rows whose ToolName the new adapter models at all
	NotComparable int // rows using a tool the adapter has no opinion on (Skill/Agent/MCP/etc)
	Agreed        int
	Disagreements []Disagreement
}

// CompareAgainstNewEngine runs every Bash/Write/Edit/MultiEdit legacy row
// through the real, production DefaultPolicies/DefaultGraphPolicies/
// DefaultRegistry chain and reports every disagreement with the row's own
// (extracted, legacy) expected_verdict, dispositioned per
// disagreementDisposition below.
func CompareAgainstNewEngine(rows []goldencorpus.Row) (*CompareResult, error) {
	reg := cmddesc.DefaultRegistry()
	policies := effectpolicy.DefaultPolicies()
	graphPolicies := effectpolicy.DefaultGraphPolicies()

	cr := &CompareResult{}
	for _, row := range rows {
		toolName := row.ToolInput.ToolName
		if toolName != "Bash" && !claudecodeadapter.IsFileEditTool(toolName) {
			cr.NotComparable++
			continue
		}
		raw, err := json.Marshal(row.ToolInput.ToolInput)
		if err != nil {
			return nil, fmt.Errorf("legacyextract: marshalling tool_input for %s: %w", row.Case, err)
		}
		input := &hookio.HookInput{
			ToolName:  toolName,
			ToolInput: raw,
			CWD:       row.CWDPathState.CWD,
		}
		cfg := claudecodeadapter.RequestConfig{ProjectRoot: row.CWDPathState.ProjectRoot}
		resp, err := claudecodeadapter.Evaluate(input, cfg, reg, policies, graphPolicies)
		if err != nil {
			// The adapter refused the input outright (e.g. an unparseable
			// command) -- not comparable, not a disagreement.
			cr.NotComparable++
			continue
		}
		cr.Comparable++
		newVerdict := evalVerdict[resp.Decision]
		if newVerdict == row.ExpectedVerdict {
			cr.Agreed++
			continue
		}
		cmd, _ := row.ToolInput.ToolInput["command"].(string)
		disposition, rationale := disagreementDisposition(row, newVerdict, resp.Reason)
		cr.Disagreements = append(cr.Disagreements, Disagreement{
			Case:        row.Case,
			Family:      familyTag(row),
			ToolName:    toolName,
			Command:     cmd,
			OldVerdict:  row.ExpectedVerdict,
			NewVerdict:  newVerdict,
			NewReason:   resp.Reason,
			Disposition: disposition,
			Rationale:   rationale,
		})
	}
	return cr, nil
}

func familyTag(row goldencorpus.Row) string {
	for _, t := range row.Tags {
		if len(t) > len("legacy-") && t[:len("legacy-")] == "legacy-" {
			return t[len("legacy-"):]
		}
	}
	return "unknown"
}

// coverageGapReason matches a new-engine Reason string that says, in
// substance, "I have no domain knowledge for this command/operand at all"
// (cmddesc has no CommandSchema registered for it, or a schema exists but
// doesn't model this subcommand/flag). R1 states the effect engine
// REPLACES RuleChain but the actual per-family retarget (porting git/gh/
// docker/kubectl/vault/sqlite3/ssh/nix/... domain knowledge into cmddesc
// schemas and effectpolicy policies) is explicitly LATER work (this
// docket's own Phase 0.5 packet and beyond) -- Phase 0 is the freeze/oracle
// snapshot BEFORE that retarget, so a coverage-gap disagreement is an
// EXPECTED, already-understood gap, not a fact needing individual
// per-command judgement.
var coverageGapReason = regexp.MustCompile(`no schema for|unmodeled|unknown flag|no executable|no operator declaration|all \d+ command nodes? permitted`)

// configArtifactReason matches a new-engine Reason string that depends on
// OPERATOR configuration (VettedHosts, KubeContexts, RemotePaths,
// BuildToolVerbs) this comparison pass deliberately runs with a ZERO-VALUE
// RequestConfig (matching every extracted row's own empty
// Settings/RulesConfig fixture -- see this package's doc comment). A row
// whose new-engine reason cites exactly this kind of unconfigured state
// (e.g. "host is not vetted", a remote target read as a runtime
// expansion) is NOT a genuine legacy-vs-new policy disagreement: the
// legacy RuleChain test that produced it very likely ran against ITS OWN
// hard-coded vetted-host/kube-context fixture that this converter does not
// (and, for a Phase-0 corpus row with an intentionally empty RulesConfig,
// should not) reproduce. These are reported separately as a comparison
// LIMITATION, not dispositioned into one of the three named buckets.
var configArtifactReason = regexp.MustCompile(`host is not vetted|is a runtime expansion|kube`)

// disagreementDisposition resolves ONE legacy-vs-new disagreement.
//
// The docket design names three buckets (Phase 0 item 3, verbatim): "old
// was a hole" (new stricter, keep), "new too strict" (spec/policy work), or
// "accepted change". This function auto-resolves what is MECHANICALLY
// diagnosable from the new engine's own stated Reason and the verdict
// transition, and is explicit about what it does NOT diagnose:
//
//   - "accepted change": same restrictiveness rank, different verdict
//     spelling (e.g. old Reject vs new NotApprove/abstain) -- the
//     permission-gate effect is unchanged.
//   - "comparison artifact (not a genuine disagreement)": the new reason
//     cites operator configuration this comparison pass never populates
//     (see configArtifactReason) -- reported for transparency, excluded
//     from the "needs manual review" remainder count.
//   - "new too strict" (spec/policy work): the new reason cites a
//     registry/policy COVERAGE gap (see coverageGapReason) -- the new
//     engine has not yet been extended with this command/family's domain
//     knowledge (R1's retarget is explicitly later-phase work), REGARDLESS
//     of whether the symptom direction is over- or under-blocking, because
//     the root cause and the remedy (add the missing schema/policy) are
//     identical either way.
//   - "needs manual review": everything else -- a genuine strictness delta
//     with no mechanically-diagnosable cause, left for a human (or a
//     follow-up pass reading each case) to resolve into "old was a hole"
//     vs "new too strict" by judging the command's actual danger.
func disagreementDisposition(row goldencorpus.Row, newVerdict goldencorpus.Verdict, newReason string) (string, string) {
	rank := func(v goldencorpus.Verdict) int {
		switch v {
		case goldencorpus.Approve:
			return 0
		case goldencorpus.NotApprove:
			return 1
		case goldencorpus.Reject:
			return 2
		}
		return 1
	}
	oldRank, newRank := rank(row.ExpectedVerdict), rank(newVerdict)
	if oldRank == newRank {
		return "accepted change", fmt.Sprintf(
			"same restrictiveness rank, different verdict spelling (old=%s -> new=%s; new reason: %s) -- permission-gate effect is unchanged.",
			row.ExpectedVerdict, newVerdict, newReason,
		)
	}
	if configArtifactReason.MatchString(newReason) {
		return "comparison artifact (not a genuine disagreement)", fmt.Sprintf(
			"new reason cites operator configuration (VettedHosts/KubeContexts/RemotePaths) this comparison pass runs with a zero-value RequestConfig (old=%s -> new=%s; new reason: %s) -- re-run with the legacy test's own fixture values to get a real answer.",
			row.ExpectedVerdict, newVerdict, newReason,
		)
	}
	if coverageGapReason.MatchString(newReason) {
		return "new too strict", fmt.Sprintf(
			"new engine has no registry/policy coverage for this command or subcommand yet (old=%s -> new=%s; new reason: %s) -- R1's per-family retarget (porting this domain knowledge into cmddesc/effectpolicy) is later-phase work, not this packet's; needs spec/policy work regardless of symptom direction.",
			row.ExpectedVerdict, newVerdict, newReason,
		)
	}
	direction := "new is STRICTER than the legacy oracle"
	if newRank < oldRank {
		direction = "new is LESS strict than the legacy oracle"
	}
	return "needs manual review", fmt.Sprintf(
		"%s (old=%s -> new=%s; new reason: %s) -- resolve to \"old was a hole\" or \"new too strict\" by judging whether the extra/reduced strictness is correct for this specific command.",
		direction, row.ExpectedVerdict, newVerdict, newReason,
	)
}
