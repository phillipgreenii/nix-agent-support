package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/spf13/cobra"
)

// archiveFixtureRow is the JSON shape one archived row takes in a fixtures
// file: the full asklog.ArchiveRow (so nothing — least of all
// correct_hook_decision — is lost) plus its decision_trace_entries, which
// asklog.ArchiveRow itself does not carry (trace entries live in a
// separate table, joined in here the same way `show` joins them).
type archiveFixtureRow struct {
	ID                             int              `json:"id"`
	SessionID                      string           `json:"session_id"`
	CWD                            string           `json:"cwd"`
	AgentID                        *string          `json:"agent_id,omitempty"`
	AgentType                      *string          `json:"agent_type,omitempty"`
	ToolName                       string           `json:"tool_name"`
	ToolUseID                      *string          `json:"tool_use_id,omitempty"`
	ToolInputHash                  string           `json:"tool_input_hash"`
	ToolInputJSON                  json.RawMessage  `json:"tool_input_json"`
	ToolSummary                    *string          `json:"tool_summary,omitempty"`
	HookDecision                   *string          `json:"hook_decision,omitempty"`
	HookReason                     *string          `json:"hook_reason,omitempty"`
	PermissionSuggestions          *string          `json:"permission_suggestions,omitempty"`
	Outcome                        string           `json:"outcome"`
	OutcomeNotes                   *string          `json:"outcome_notes,omitempty"`
	CreatedAt                      string           `json:"created_at"`
	ResolvedAt                     *string          `json:"resolved_at,omitempty"`
	Excluded                       int              `json:"excluded"`
	ExcludedReason                 *string          `json:"excluded_reason,omitempty"`
	CorrectHookDecision            *string          `json:"correct_hook_decision,omitempty"`
	CorrectHookDecisionExplanation *string          `json:"correct_hook_decision_explanation,omitempty"`
	SandboxEnabled                 *int             `json:"sandbox_enabled"`
	PermissionMode                 *string          `json:"permission_mode,omitempty"`
	PromptID                       *string          `json:"prompt_id,omitempty"`
	ToolResponse                   json.RawMessage  `json:"tool_response,omitempty"`
	TranscriptPath                 *string          `json:"transcript_path,omitempty"`
	Trace                          []showTraceEntry `json:"trace,omitempty"`
}

// archiveFixtureSet is the top-level shape written to --fixtures-out: a
// self-describing envelope (where it came from, what threshold was used,
// how many rows were candidates vs. actually sampled) around the sampled
// rows themselves, so the file is useful as a regression-test corpus on its
// own without cross-referencing the archive run that produced it.
type archiveFixtureSet struct {
	CapturedAt         string              `json:"captured_at"`
	SourceDB           string              `json:"source_db"`
	Before             string              `json:"before"`
	TotalCandidateRows int                 `json:"total_candidate_rows"`
	SampledRows        int                 `json:"sampled_rows"`
	Fixtures           []archiveFixtureRow `json:"fixtures"`
}

func newArchiveCmd() *cobra.Command {
	var dbPath, before, fixturesOut string
	var days, samplePerBucket int
	var dryRun, yes bool

	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Archive old decision rows: sample fixtures, delete, then VACUUM",
		Long: `Archive implements the periodic archiving ceremony decided for
bd pg2-riwdh. In order:

  1. Query every tool_decisions row older than the threshold.
  2. BEFORE anything is removed, sample a representative set of
     "interesting" rows from those candidates (bucketed by tool_name and by
     whether/how each row was ground-truth annotated) and write them,
     UNSUMMARIZED — every column, including correct_hook_decision and its
     explanation, plus each row's decision_trace_entries — to
     --fixtures-out.
  3. Delete the candidate rows.
  4. Run VACUUM, but ONLY if step 3 actually deleted at least one row —
     VACUUM against a database with nothing to reclaim is a wasted,
     lock-taking no-op.

The threshold is given as --before (an ISO8601 date: rows strictly older
are archived) or --days (an age in days, converted the same way --days
works for 'evaluate'/'report' — now, minus N days, in UTC). Exactly one is
required.

Without --yes, this is always a dry run: candidates are counted and the
sample is computed and reported, but nothing is written to disk and no row
is deleted. This is the safer default for a command whose entire job is
deleting rows from a corpus someone may actually rely on — pass --yes
(alongside --fixtures-out) to perform the real ceremony.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runArchive(archiveOptions{
				DBPath:          dbPath,
				Before:          before,
				Days:            days,
				FixturesOut:     fixturesOut,
				SamplePerBucket: samplePerBucket,
				DryRun:          dryRun,
				Yes:             yes,
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", asklog.DefaultDBPath(), "Path to the target database")
	cmd.Flags().StringVar(&before, "before", "", "Archive rows created before this date (ISO8601)")
	cmd.Flags().IntVar(&days, "days", 0, "Archive rows older than N days (alternative to --before)")
	cmd.Flags().StringVar(&fixturesOut, "fixtures-out", "", "Path to write the sampled fixture JSON (required for a real run)")
	cmd.Flags().IntVar(&samplePerBucket, "sample-per-bucket", 5, "Max rows sampled per (tool_name, classification) bucket")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report candidates/sample only; write nothing, delete nothing, vacuum nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Actually delete and vacuum (required for a real run; omitting it behaves like --dry-run)")
	return cmd
}

type archiveOptions struct {
	DBPath          string
	Before          string
	Days            int
	FixturesOut     string
	SamplePerBucket int
	DryRun          bool
	Yes             bool
}

// resolveArchiveThreshold turns --before/--days into the single ISO8601
// threshold QueryRowsBefore/DeleteRowsBefore both key on, refusing rather
// than silently picking one when both or neither were given.
func resolveArchiveThreshold(opt archiveOptions) (string, error) {
	if opt.Before != "" && opt.Days > 0 {
		return "", fmt.Errorf("--before and --days are mutually exclusive")
	}
	if opt.Before != "" {
		return opt.Before, nil
	}
	if opt.Days > 0 {
		return time.Now().AddDate(0, 0, -opt.Days).UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("one of --before or --days is required")
}

// archiveBucketKey classifies a candidate row into the bucket the sampling
// step draws representatives from. Two axes:
//
//   - WHAT tool it is, so the sample stays diverse across tool types rather
//     than being dominated by whichever tool happens to be most frequent.
//   - WHY it is "interesting": an explicit human ground-truth annotation
//     (set via `set-correct-decision`) that DISAGREES with the hook's own
//     decision is the single most valuable row to keep — it is a
//     CONFIRMED miss, exactly the kind of regression case the ceremony
//     must not silently lose when the row it lives on is deleted. One that
//     AGREES is still worth a token presence (a confirmed correct case).
//     Everything else — the vast majority of rows, which were never
//     annotated at all — buckets by outcome, the closest available proxy
//     for "edge case" among ungraded rows: a denial or a hook rejection is
//     far more informative to keep than a routine approval.
func archiveBucketKey(r asklog.ArchiveRow) string {
	classification := "unannotated:" + r.Outcome
	if r.CorrectHookDecision != nil {
		hookDecision := ""
		if r.HookDecision != nil {
			hookDecision = *r.HookDecision
		}
		if *r.CorrectHookDecision != hookDecision {
			classification = "annotated-miss"
		} else {
			classification = "annotated-match"
		}
	}
	return r.ToolName + "|" + classification
}

// pickSample selects up to n rows from rows, spread evenly across the slice
// (first, last, and evenly spaced between) rather than just the first n —
// candidates are ordered by id (i.e. roughly by time), so an even spread is
// what makes the sample "representative" across the whole archived window
// instead of only its earliest rows. If n <= 0 or there are already at
// most n rows, every row is returned.
func pickSample(rows []asklog.ArchiveRow, n int) []asklog.ArchiveRow {
	if n <= 0 || len(rows) <= n {
		out := make([]asklog.ArchiveRow, len(rows))
		copy(out, rows)
		return out
	}
	if n == 1 {
		return []asklog.ArchiveRow{rows[0]}
	}
	out := make([]asklog.ArchiveRow, 0, n)
	seen := make(map[int]bool, n)
	for i := 0; i < n; i++ {
		idx := i * (len(rows) - 1) / (n - 1)
		if seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, rows[idx])
	}
	return out
}

// sampleFixtures buckets candidates by archiveBucketKey and takes up to
// perBucket rows from each bucket, in the order buckets were first
// encountered (candidates arrive ordered by id, so this is deterministic
// for a given input).
func sampleFixtures(candidates []asklog.ArchiveRow, perBucket int) []asklog.ArchiveRow {
	buckets := map[string][]asklog.ArchiveRow{}
	var order []string
	for _, r := range candidates {
		key := archiveBucketKey(r)
		if _, ok := buckets[key]; !ok {
			order = append(order, key)
		}
		buckets[key] = append(buckets[key], r)
	}
	var out []asklog.ArchiveRow
	for _, key := range order {
		out = append(out, pickSample(buckets[key], perBucket)...)
	}
	return out
}

// toolResponseRaw converts the nullable tool_response column (raw JSON text)
// into json.RawMessage so it re-encodes as a nested object, the same
// treatment evalResult.ToolResponse gets in cmd_evaluate.go.
func toolResponseRaw(p *string) json.RawMessage {
	if p == nil || *p == "" {
		return nil
	}
	return json.RawMessage(*p)
}

// buildFixtureSet fetches the decision_trace_entries for every sampled row
// and assembles the full JSON envelope. It is a read-only step (QueryTraceByDecisionID
// is a SELECT), run against the SAME store the delete step below uses, so it
// executes strictly before any row disappears.
func buildFixtureSet(store *asklog.Store, dbPath, before string, totalCandidates int, sampled []asklog.ArchiveRow) (archiveFixtureSet, error) {
	set := archiveFixtureSet{
		CapturedAt:         time.Now().UTC().Format(time.RFC3339),
		SourceDB:           dbPath,
		Before:             before,
		TotalCandidateRows: totalCandidates,
		SampledRows:        len(sampled),
	}
	for _, r := range sampled {
		traceRows, err := store.QueryTraceByDecisionID(r.ID)
		if err != nil {
			return archiveFixtureSet{}, fmt.Errorf("query trace for id=%d: %w", r.ID, err)
		}
		var trace []showTraceEntry
		for _, tr := range traceRows {
			trace = append(trace, showTraceEntry{
				RuleOrder: tr.RuleOrder,
				RuleName:  tr.RuleName,
				Decision:  tr.Decision,
				Reason:    tr.Reason,
			})
		}
		set.Fixtures = append(set.Fixtures, archiveFixtureRow{
			ID:                             r.ID,
			SessionID:                      r.SessionID,
			CWD:                            r.CWD,
			AgentID:                        r.AgentID,
			AgentType:                      r.AgentType,
			ToolName:                       r.ToolName,
			ToolUseID:                      r.ToolUseID,
			ToolInputHash:                  r.ToolInputHash,
			ToolInputJSON:                  json.RawMessage(r.ToolInputJSON),
			ToolSummary:                    r.ToolSummary,
			HookDecision:                   r.HookDecision,
			HookReason:                     r.HookReason,
			PermissionSuggestions:          r.PermissionSuggestions,
			Outcome:                        r.Outcome,
			OutcomeNotes:                   r.OutcomeNotes,
			CreatedAt:                      r.CreatedAt,
			ResolvedAt:                     r.ResolvedAt,
			Excluded:                       r.Excluded,
			ExcludedReason:                 r.ExcludedReason,
			CorrectHookDecision:            r.CorrectHookDecision,
			CorrectHookDecisionExplanation: r.CorrectHookDecisionExplanation,
			SandboxEnabled:                 sandboxEnabledPtr(r.SandboxEnabled),
			PermissionMode:                 r.PermissionMode,
			PromptID:                       r.PromptID,
			ToolResponse:                   toolResponseRaw(r.ToolResponse),
			TranscriptPath:                 r.TranscriptPath,
			Trace:                          trace,
		})
	}
	return set, nil
}

func writeArchiveFixtures(path string, set archiveFixtureSet) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(set); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func runArchive(opt archiveOptions) {
	threshold, err := resolveArchiveThreshold(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if !opt.DryRun {
		if opt.FixturesOut == "" {
			fmt.Fprintf(os.Stderr, "error: --fixtures-out is required for a real (non-dry-run) archive\n")
			os.Exit(1)
		}
		if !opt.Yes {
			fmt.Fprintf(os.Stderr, "error: --yes is required to actually delete and vacuum; without it, archive only reports what it would do (equivalent to --dry-run)\n")
			os.Exit(1)
		}
	}

	// archive genuinely WRITES (DELETE + VACUUM), so — like mark-excluded and
	// set-correct-decision — it MUST use the read-write store, never
	// NewReadOnlyStore (pg2-cbihz's read/write split).
	store, err := asklog.NewStore(opt.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	candidates, err := store.QueryRowsBefore(threshold)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error querying candidates: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("db:                  %s\n", opt.DBPath)
	fmt.Printf("threshold:           %s\n", threshold)
	fmt.Printf("candidate rows:      %d\n", len(candidates))

	if len(candidates) == 0 {
		fmt.Println("nothing older than the threshold; no fixtures written, no delete, no VACUUM")
		return
	}

	sampled := sampleFixtures(candidates, opt.SamplePerBucket)
	fmt.Printf("sampled for fixture: %d\n", len(sampled))

	if opt.DryRun {
		fmt.Println("dry run: no fixtures written, no rows deleted, no VACUUM run (pass --fixtures-out and --yes to actually archive)")
		return
	}

	// Step 2: sample and write fixtures — BEFORE any row is removed.
	fixtureSet, err := buildFixtureSet(store, opt.DBPath, threshold, len(candidates), sampled)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building fixture set: %v\n", err)
		os.Exit(1)
	}
	if err := writeArchiveFixtures(opt.FixturesOut, fixtureSet); err != nil {
		fmt.Fprintf(os.Stderr, "error writing fixtures: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d sampled row(s), with ground-truth annotations preserved, to %s\n", len(sampled), opt.FixturesOut)

	// Step 3: delete.
	deleted, err := store.DeleteRowsBefore(threshold)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error deleting rows: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("deleted %d row(s) older than %s\n", deleted, threshold)

	// Step 4: VACUUM, only now that rows were actually removed.
	if deleted > 0 {
		if err := store.Vacuum(); err != nil {
			fmt.Fprintf(os.Stderr, "error running VACUUM: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("VACUUM complete")
	} else {
		fmt.Println("no rows were actually deleted; skipping VACUUM (nothing to reclaim)")
	}
}
