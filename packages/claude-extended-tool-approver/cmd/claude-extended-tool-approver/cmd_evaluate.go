package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/claudecodeadapter"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/settingseval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
	"github.com/spf13/cobra"
)

type evalResult struct {
	ID           int    `json:"id"`
	ToolName     string `json:"tool_name"`
	ToolSummary  string `json:"tool_summary"`
	CommandClass string `json:"command_class"`
	HookDecision string `json:"hook_decision"`
	ReplayResult string `json:"replay_result"`
	// replay_module / replay_reason are the DECIDING rule's Module and Reason,
	// taken from the RuleResult the CURRENT engine returned for this row. They are
	// per-site attribution for the replay verdict — for asks and for `{}` alike —
	// and are what makes ADR 0043's decision-3 demonstration executable from
	// `evaluate` alone (its Consequences name their absence a blocking
	// prerequisite).
	//
	// NOT the stored hook_reason column: that records what an OLDER binary said,
	// so joining on it would attribute today's verdict to yesterday's rule. (Nor
	// is it reachable here — asklog.QueryRows does not select it.)
	//
	// Empty is meaningful, not missing: engine.Evaluate manufactures its terminal
	// NoOpinion with no Module, so an empty pair on an `abstain` row IS the
	// attribution "the chain was exhausted and no rule had an opinion". Empty on a
	// stale-cwd row means no replay ran at all, exactly like replay_result.
	ReplayModule   string `json:"replay_module"`
	ReplayReason   string `json:"replay_reason"`
	SettingsResult string `json:"settings_result,omitempty"`
	Category       string `json:"category"`
	Outcome        string `json:"outcome"`
	SandboxEnabled *int   `json:"sandbox_enabled"`
	// approval_source is the derived approval-MECHANISM axis
	// {unknown,bypass,auto,settings,hook,user}; the four raw fields below back
	// it and let the skill segment orthogonally (e.g. agent_type IS NOT NULL).
	ApprovalSource string          `json:"approval_source"`
	PermissionMode *string         `json:"permission_mode"`
	AgentType      *string         `json:"agent_type"`
	OutcomeNotes   *string         `json:"outcome_notes"`
	ToolResponse   json.RawMessage `json:"tool_response"`
}

func newEvaluateCmd() *cobra.Command {
	var days int
	var since, settingsPath, format, approvalSource, baseline string
	var missesOnly bool
	var corpusPaths []string
	cmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Replay logged decisions and categorize them as correct or miss",
		Long: `Replay every logged decision through the current rule engine and
categorize each as correct, miss-caught-by-settings, miss-uncaught,
needs-review, unresolved, or stale-cwd.

A row whose outcome is "unresolved" (never resolved — interrupted, abandoned,
or swept at SessionEnd) carries no ground truth, so it is categorized
"unresolved" and is never counted as correct or as a miss.

Use --settings to additionally evaluate each decision against a
Claude Code settings file so misses can be attributed to settings
coverage.

Use --approval-source to restrict evaluation to a single approval-mechanism
bucket (unknown|bypass|auto|settings|hook|user).

--format json also emits replay_module and replay_reason: the rule that PRODUCED
the replay verdict, and its reason. That is per-site attribution for the replay
itself, so an ask breakdown needs no second pass through the binary in hook mode:

  claude-extended-tool-approver evaluate --days 3 --format json |
    jq -r '.[] | select(.replay_result == "ask")
           | .replay_module + " | " + (.replay_reason | split("\n")[0] | .[0:33])' |
    sort | uniq -c | sort -rn

A reason is a rule's CONSTANT text with the offending detail interpolated into
it, so module plus a fixed-width prefix of the reason's first LINE is the site
key. Both trims are load-bearing: the detail can be a path, a whole heredoc or a
multi-line bd comment, and without them one site fragments across dozens of
lines. The width is a knob — widen it if two sites collapse together, narrow it
if one site splits.

--baseline <file> folds a decision-delta report into this same command instead
of a separate diff step. The FIRST invocation captures (the file does not
exist yet); a LATER invocation against the same path compares and reports
every row whose replay verdict moved, classified more-restrictive vs
less-restrictive and attributed to the deciding rule:

  claude-extended-tool-approver evaluate --format json --baseline base.json   # capture
  # ... edit rules, rebuild, run unit tests ...
  claude-extended-tool-approver evaluate --format json --baseline base.json   # compare

A baseline captured under different filters (--days/--since/--approval-source/
--misses-only/--settings) is refused rather than silently diffed. The default
bare-array --format json shape (and the jq recipe above) is unchanged when
--baseline is not given.

--corpus <path> switches evaluate to an entirely different grading source: the
labeled internal/goldencorpus corpus (e.g.
internal/goldencorpus/testdata/corpus.json and/or
internal/goldencorpus/testdata/corpus_legacy.json — repeat the flag to grade
more than one file together) instead of the ask log. Each corpus row's own
tool_input is replayed through the new effect engine
(claudecodeadapter.Evaluate) and graded against the row's own expected_verdict
under the corpus's three-way vocabulary (approve/reject/not-approve — see
internal/goldencorpus's package doc comment for the acceptance rule). --corpus
cannot be combined with the ask-log-only flags (--days/--since/--settings/
--approval-source/--baseline/--misses-only).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(corpusPaths) > 0 {
				if days > 0 || since != "" || settingsPath != "" || approvalSource != "" || baseline != "" || missesOnly {
					fmt.Fprintln(os.Stderr, "error: --corpus cannot be combined with --days/--since/--settings/--approval-source/--baseline/--misses-only (it grades a fixed corpus file, not the ask log)")
					os.Exit(1)
				}
				runEvaluateCorpus(corpusPaths, format)
				return nil
			}
			runEvaluate(days, since, settingsPath, format, approvalSource, baseline, missesOnly)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 0, "Only evaluate rows from the last N days")
	cmd.Flags().StringVar(&since, "since", "", "Only evaluate rows after this date (ISO8601)")
	cmd.Flags().StringVar(&settingsPath, "settings", "", "Path to settings file for settings evaluation")
	cmd.Flags().StringVar(&format, "format", "summary", "Output format: json|summary")
	cmd.Flags().StringVar(&approvalSource, "approval-source", "", "Only evaluate rows with this approval_source (unknown|bypass|auto|settings|hook|user)")
	cmd.Flags().StringVar(&baseline, "baseline", "", "Path to a baseline report: captures it if absent, compares against it and reports the decision delta if present")
	cmd.Flags().BoolVar(&missesOnly, "misses-only", false, "Only show rows where hook is wrong")
	cmd.Flags().StringArrayVar(&corpusPaths, "corpus", nil, "Path to a goldencorpus JSON file (repeatable); grades the corpus's own expected_verdict instead of replaying the ask log")
	return cmd
}

func runEvaluate(daysVal int, sinceVal, settingsPathVal, formatVal, approvalSourceVal, baselineVal string, missesOnlyVal bool) {
	days := &daysVal
	since := &sinceVal
	settingsPath := &settingsPathVal
	format := &formatVal
	approvalSourceFilter := &approvalSourceVal
	missesOnly := &missesOnlyVal

	sinceDate := *since
	if *days > 0 && sinceDate == "" {
		sinceDate = time.Now().AddDate(0, 0, -*days).UTC().Format(time.RFC3339)
	}

	// evaluate only ever replays rows through QueryRows — it never writes to
	// the ask log, so it opens read-only (pg2-cbihz).
	store, err := asklog.NewReadOnlyStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	rows, err := store.QueryRows(sinceDate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error querying rows: %v\n", err)
		os.Exit(1)
	}

	var se *settingseval.SettingsEvaluator
	if *settingsPath != "" {
		se, err = settingseval.NewSettingsEvaluator(*settingsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error loading settings: %v\n", err)
			os.Exit(1)
		}
	}

	counts := map[string]int{
		"correct":                 0,
		"miss-caught-by-settings": 0,
		"miss-uncaught":           0,
		"needs-review":            0,
		"stale-cwd":               0,
		"unresolved":              0,
	}

	// sandboxCounts tallies rows by sandbox state ("on"/"off"/"unknown").
	// It includes every row, not just misses, and mirrors the totals line.
	sandboxCounts := map[string]int{"on": 0, "off": 0, "unknown": 0}

	var results []evalResult

	// Engines are memoized by CWD (and rules.json parsed at most once) for the
	// duration of this replay — see setup.EngineCache. Measured: 351,719 rows
	// against 1,240 distinct CWDs (pg2-rszk3).
	engines := setup.NewEngineCache()

	for _, row := range rows {
		// approval_source classifies CONTEXT, not outcome, so it is derived and
		// filtered before anything else (including the stale-cwd short-circuit).
		approvalSource := asklog.ApprovalSource(row.PermissionMode, row.PromptID, row.HookDecision)
		if *approvalSourceFilter != "" && approvalSource != *approvalSourceFilter {
			continue
		}

		sandboxCounts[sandboxEnabledKey(row.SandboxEnabled)]++
		r := evalResult{
			ID:             row.ID,
			ToolName:       row.ToolName,
			ToolSummary:    row.ToolSummary,
			CommandClass:   asklog.CommandClass(row.ToolName, json.RawMessage(row.ToolInputJSON), row.CWD),
			Outcome:        row.Outcome,
			SandboxEnabled: sandboxEnabledPtr(row.SandboxEnabled),
			ApprovalSource: approvalSource,
			PermissionMode: row.PermissionMode,
			AgentType:      row.AgentType,
			OutcomeNotes:   row.OutcomeNotes,
		}
		if row.ToolResponse != nil && *row.ToolResponse != "" {
			r.ToolResponse = json.RawMessage(*row.ToolResponse)
		}
		if row.HookDecision != nil {
			r.HookDecision = *row.HookDecision
		}

		// Replay through the CWD-cached engine; stale mirrors the previous
		// per-row os.Stat(row.CWD) check, now amortized per distinct CWD too.
		eng, stale := engines.EngineForCWD(row.CWD)
		if stale {
			r.Category = "stale-cwd"
			counts["stale-cwd"]++
			if !*missesOnly {
				results = append(results, r)
			}
			continue
		}

		input := &hookio.HookInput{
			ToolName:  row.ToolName,
			ToolInput: json.RawMessage(row.ToolInputJSON),
			CWD:       row.CWD,
		}
		result := eng.EvaluateHook(input)
		r.ReplayResult = decisionToDBString(result.Decision)
		// The returned RuleResult IS the deciding one, which is why attribution is
		// read off it rather than off result.Trace. Trace is chronological, so its
		// FIRST entry is whichever rule ran first — routinely an abstaining one —
		// and for a Bash compound the verdict comes from EvaluateExpression's
		// most-restrictive fold, where the winning leaf can be any leaf. Both make
		// trace[0] the wrong answer; MostRestrictive carries the winner's Module and
		// Reason through the fold, so the returned struct is already the final word.
		r.ReplayModule = result.Module
		r.ReplayReason = result.Reason

		// Settings evaluation
		if se != nil {
			r.SettingsResult = se.Evaluate(row.ToolName, json.RawMessage(row.ToolInputJSON), row.CWD)
		}

		// Categorize
		r.Category = categorize(r, row)

		counts[r.Category]++
		// "unresolved" is excluded from --misses-only for the same reason
		// "stale-cwd" is: it carries no ground truth, so it is not a miss and
		// must not inflate the miss dataset downstream analysis ranks.
		if *missesOnly && (r.Category == "correct" || r.Category == "unresolved") {
			continue
		}
		results = append(results, r)
	}

	if baselineVal != "" {
		runEvaluateBaseline(baselineVal, *format, evaluateFilters{
			Days:           *days,
			Since:          *since,
			ApprovalSource: *approvalSourceFilter,
			MissesOnly:     *missesOnly,
			Settings:       *settingsPath,
		}, counts, results)
		return
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
	default:
		total := 0
		for _, c := range counts {
			total += c
		}
		fmt.Printf("Total rows:          %5d\n", total)
		fmt.Printf("Stale CWD:           %5d\n", counts["stale-cwd"])
		fmt.Printf("Correct:             %5d\n", counts["correct"])
		fmt.Printf("Misses (settings):   %5d\n", counts["miss-caught-by-settings"])
		fmt.Printf("Misses (uncaught):   %5d\n", counts["miss-uncaught"])
		fmt.Printf("Needs review:        %5d\n", counts["needs-review"])
		fmt.Printf("Unresolved:          %5d\n", counts["unresolved"])
		fmt.Printf("By sandbox:          on=%d off=%d unknown=%d\n",
			sandboxCounts["on"], sandboxCounts["off"], sandboxCounts["unknown"])
	}
}

func categorize(r evalResult, row asklog.DecisionRow) string {
	// If correct_hook_decision is set, compare against that. An explicit human
	// annotation is real ground truth, so it outranks even a never-resolved
	// outcome.
	if row.CorrectDec != nil {
		if r.ReplayResult == *row.CorrectDec {
			return "correct"
		}
		if r.SettingsResult != "" {
			return "miss-caught-by-settings"
		}
		return "miss-uncaught"
	}

	// Nobody ever decided this call, so there is no ground truth to grade
	// against. 'unresolved' gets its own terminal category — never "correct",
	// never a "miss-*" — so a SessionEnd sweep can no longer masquerade as a
	// user denial (and can no longer be credited as a correct deny either).
	if !asklog.OutcomeIsDecision(row.Outcome) {
		if row.Outcome == asklog.OutcomeUnresolved {
			return "unresolved"
		}
		return "needs-review"
	}

	expectedDecision := outcomeToExpectedDecision(row.Outcome)

	if r.ReplayResult == expectedDecision {
		return "correct"
	}

	// Hook allows but the call was DECLINED — ambiguous. The user may have
	// redirected (provided text feedback) rather than truly rejecting the tool.
	// Since we can't distinguish denial from correction, classify as
	// needs-review. This carve-out is deliberately scoped to OutcomeDenied: a
	// hook Reject (OutcomeRejected) involved no user, so there is no redirection
	// to confuse it with — a Reject that now replays to allow is a real engine
	// change and MUST stay visible as a miss.
	if r.ReplayResult == "allow" && row.Outcome == asklog.OutcomeDenied {
		return "needs-review"
	}

	// Hook got it wrong — check if settings would catch it
	if r.SettingsResult != "" {
		return "miss-caught-by-settings"
	}
	return "miss-uncaught"
}

// outcomeToExpectedDecision maps a recorded outcome to the hook decision that
// would have been RIGHT for it. It returns "" for the outcomes that record no
// decision at all (pending, unresolved) — those have no expected decision, and
// asklog.OutcomeIsDecision MUST be consulted before grading against them.
func outcomeToExpectedDecision(outcome string) string {
	switch outcome {
	case asklog.OutcomeApproved:
		return "allow"
	case asklog.OutcomeDenied, asklog.OutcomeRejected:
		// Both are a refusal of the call, so a replayed "deny" is correct for
		// either: OutcomeDenied is somebody declining it, OutcomeRejected is the
		// hook refusing it itself (a self-consistency check on the engine).
		return "deny"
	default:
		return ""
	}
}

func decisionToDBString(d hookio.Decision) string {
	switch d {
	case hookio.Approve:
		return "allow"
	case hookio.Reject:
		return "deny"
	case hookio.Ask:
		return "ask"
	case hookio.NoOpinion:
		return "abstain"
	default:
		return "unknown"
	}
}

// --- --corpus: grade the labeled goldencorpus instead of the ask log ---
//
// Track X.5 (docket tc-o14i5.8): evaluate's OTHER grading path. The rows
// above replay real historical ask-log calls and grade them against what a
// human (or the hook itself) decided at the time (row.Outcome /
// row.CorrectDec) -- a moving, production-shaped ground truth. A corpus row
// carries no such history: it is a hand-authored fixture whose own
// expected_verdict IS the ground truth (internal/goldencorpus's package doc
// comment, the P2 corpus contract). The two ground truths do not share a
// join key (an ask-log row has no "case" name and a corpus row has no
// database id), so this is a SEPARATE loop over a separate row source, not a
// new column on the existing one -- hence the new --corpus flag rather than
// a change to the default asklog-replay behavior (see this packet's own
// Freedom section).
//
// Replay here goes through claudecodeadapter.Evaluate (the new effect
// engine), not setup.EngineCache's old internal/engine+internal/rules chain
// that the asklog path above still uses. Three reasons, all from prior
// work this packet only reads: (1) internal/goldencorpus's own doc comment
// says its RulesConfigFixture mirrors internal/evalcontract's Request shape
// "field-for-field" -- it is data modeled for the NEW engine's config, and
// has no equivalent shape the old configrules.Config accepts; (2) a corpus
// row's CWDPathState.CWD is a synthetic fixture path ("/repo", etc.), not a
// real directory on this machine, so EngineCache.EngineForCWD's os.Stat
// staleness check would mark every corpus row stale-cwd -- claudecodeadapter
// does no such check; (3) internal/legacyextract.CompareAgainstNewEngine
// already established this exact pattern (goldencorpus row ->
// claudecodeadapter.Evaluate) for grading corpus rows against the new
// engine, so this reuses prior art rather than inventing a second one.

// corpusEvalResult is one graded goldencorpus row: the corpus's own
// identifying fields plus the replay verdict and the category it earned.
type corpusEvalResult struct {
	Case            string               `json:"case"`
	Tags            []string             `json:"tags,omitempty"`
	ToolName        string               `json:"tool_name"`
	ExpectedVerdict goldencorpus.Verdict `json:"expected_verdict"`
	ReplayDecision  string               `json:"replay_decision"`
	ReplayReason    string               `json:"replay_reason,omitempty"`
	Category        string               `json:"category"`
}

// gradeCorpusRow reports whether decision satisfies expected under the P2
// corpus contract's three-way acceptability rule (internal/goldencorpus's
// package doc comment, verbatim): "not-approve = abstain or reject
// acceptable ... must-reject rows MUST be deny." Approve and Reject each
// require decision to match EXACTLY; NotApprove accepts EITHER Abstain or
// Reject -- Approve is the only decision a NotApprove row disqualifies (an
// Ask decision is reserved/unemitted today -- see evalcontract.Decision's
// own doc comment -- and is graded as disqualifying for NotApprove too,
// since the contract names only "abstain or reject" as acceptable).
func gradeCorpusRow(expected goldencorpus.Verdict, decision evalcontract.Decision) bool {
	switch expected {
	case goldencorpus.Approve:
		return decision == evalcontract.Approve
	case goldencorpus.Reject:
		return decision == evalcontract.Reject
	case goldencorpus.NotApprove:
		return decision == evalcontract.Reject || decision == evalcontract.Abstain
	default:
		return false
	}
}

// convertKubeContexts/convertRemotePaths/convertBuildToolVerbs adapt a
// corpus row's RulesConfigFixture sub-shapes to the new engine's own
// evalcontract-typed equivalents. goldencorpus's package doc comment
// explains why these are separate, mirrored declarations rather than a
// shared import (goldencorpus does not depend on evalcontract), so a small
// field-for-field conversion is required at this one consuming boundary.
func convertKubeContexts(m map[string]goldencorpus.KubeContextRule) map[string]evalcontract.KubeContextRule {
	if m == nil {
		return nil
	}
	out := make(map[string]evalcontract.KubeContextRule, len(m))
	for k, v := range m {
		out[k] = evalcontract.KubeContextRule{Allow: v.Allow}
	}
	return out
}

func convertRemotePaths(m map[string][]goldencorpus.RemotePathRule) map[string][]evalcontract.RemotePathRule {
	if m == nil {
		return nil
	}
	out := make(map[string][]evalcontract.RemotePathRule, len(m))
	for k, rules := range m {
		converted := make([]evalcontract.RemotePathRule, len(rules))
		for i, r := range rules {
			converted[i] = evalcontract.RemotePathRule{Prefix: r.Prefix, Category: r.Category}
		}
		out[k] = converted
	}
	return out
}

func convertBuildToolVerbs(in []goldencorpus.VerbScopedApproval) []evalcontract.VerbScopedApproval {
	if in == nil {
		return nil
	}
	out := make([]evalcontract.VerbScopedApproval, len(in))
	for i, v := range in {
		out[i] = evalcontract.VerbScopedApproval{Tool: v.Tool, Verb: v.Verb, Class: v.Class}
	}
	return out
}

// loadCorpusRows reads and concatenates every corpus JSON file named by
// paths. internal/goldencorpus exposes no loader of its own outside its
// _test.go files (unexported, package-private), so this reads the
// checked-in JSON array shape directly against the exported goldencorpus.Row
// type -- read-only consumption of the corpus format, not a change to the
// goldencorpus package itself.
func loadCorpusRows(paths []string) ([]goldencorpus.Row, error) {
	var rows []goldencorpus.Row
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("reading corpus %s: %w", p, err)
		}
		var fileRows []goldencorpus.Row
		if err := json.Unmarshal(data, &fileRows); err != nil {
			return nil, fmt.Errorf("parsing corpus %s: %w", p, err)
		}
		rows = append(rows, fileRows...)
	}
	return rows, nil
}

// runEvaluateCorpus is --corpus's own entry point: load every named corpus
// file, replay each row's tool_input through the new effect engine, and
// grade it against the row's own expected_verdict (gradeCorpusRow). A row
// naming a tool the new engine has no opinion on (anything but Bash/Write/
// Edit/MultiEdit) is reported "not-comparable", mirroring
// internal/legacyextract.CompareAgainstNewEngine's own established
// Comparable/NotComparable split for the same underlying reason.
func runEvaluateCorpus(corpusPaths []string, format string) {
	rows, err := loadCorpusRows(corpusPaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	reg := cmddesc.DefaultRegistry()
	policies := effectpolicy.DefaultPolicies()
	graphPolicies := effectpolicy.DefaultGraphPolicies()

	counts := map[string]int{"correct": 0, "miss": 0, "not-comparable": 0}
	var results []corpusEvalResult

	for _, row := range rows {
		r := corpusEvalResult{
			Case:            row.Case,
			Tags:            row.Tags,
			ToolName:        row.ToolInput.ToolName,
			ExpectedVerdict: row.ExpectedVerdict,
		}

		if row.ToolInput.ToolName != "Bash" && !claudecodeadapter.IsFileEditTool(row.ToolInput.ToolName) {
			r.Category = "not-comparable"
			counts["not-comparable"]++
			results = append(results, r)
			continue
		}

		raw, err := json.Marshal(row.ToolInput.ToolInput)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: marshalling tool_input for case %s: %v\n", row.Case, err)
			os.Exit(1)
		}
		input := &hookio.HookInput{
			ToolName:  row.ToolInput.ToolName,
			ToolInput: raw,
			CWD:       row.CWDPathState.CWD,
		}
		cfg := claudecodeadapter.RequestConfig{
			ProjectRoot:     row.CWDPathState.ProjectRoot,
			VettedHosts:     row.RulesConfig.VettedHosts,
			RemoteLifecycle: row.RulesConfig.RemoteLifecycle,
			KubeContexts:    convertKubeContexts(row.RulesConfig.KubeContexts),
			RemotePaths:     convertRemotePaths(row.RulesConfig.RemotePaths),
			BuildToolVerbs:  convertBuildToolVerbs(row.RulesConfig.BuildToolVerbs),
		}

		resp, evalErr := claudecodeadapter.Evaluate(input, cfg, reg, policies, graphPolicies)
		if evalErr != nil {
			r.Category = "not-comparable"
			r.ReplayReason = evalErr.Error()
			counts["not-comparable"]++
			results = append(results, r)
			continue
		}

		r.ReplayDecision = resp.Decision.String()
		r.ReplayReason = resp.Reason
		if gradeCorpusRow(row.ExpectedVerdict, resp.Decision) {
			r.Category = "correct"
		} else {
			r.Category = "miss"
		}
		counts[r.Category]++
		results = append(results, r)
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
	default:
		fmt.Printf("Total rows:          %5d\n", len(results))
		fmt.Printf("Correct:             %5d\n", counts["correct"])
		fmt.Printf("Miss:                %5d\n", counts["miss"])
		fmt.Printf("Not comparable:      %5d\n", counts["not-comparable"])
	}
}
