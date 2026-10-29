package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackercompare"
	"github.com/spf13/cobra"
)

// exitCodeError makes main exit with a specific code and no "error:" line (the
// command has already printed its report).
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// compareTrackerExitDifferences is the exit code when the report found
// differences (0 is a clean report, 1 an error).
const compareTrackerExitDifferences = 2

func newCompareTrackerCmd() *cobra.Command {
	var since, until, format, trackerDB string
	cmd := &cobra.Command{
		Use:   "compare-tracker",
		Short: "Compare CETA's ask log with the claude-tool-tracker's records over a time window",
		Long: `Join CETA's ask log and the claude-tool-tracker's database on
session_id + tool_use_id over a time window and report where they differ, so
you can decide whether the tracker can take over. Both databases are opened
read-only. CETA's PreToolUse has no matcher, so every CETA row should have a
tracker record.

The report lists:
  - calls only CETA recorded, and calls only the tracker recorded;
  - calls whose CETA verdict differs from the tracker's ceta.verdict;
  - calls whose outcome differs;
  - calls where permission_mode, agent_type, sandbox_enabled, is_error or the
    presence of a denial reason differs;
  - keys with duplicate rows, CETA rows with no tool_use_id, and sessions with
    CETA rows but no tracker event file;
  - the tracker ingest's skipped-line counts for the window.

Window. --since and --until take an RFC 3339 timestamp, a YYYY-MM-DD date (UTC
midnight) or a duration such as 48h (that long before now). The window is
[since, until); --since defaults to 48h, --until to now-and-later. A call is in
the window by its CETA created_at or its tracker start time (either side's
time suffices, and both sides are then read whole). Skipped-line counts cover
the ingest runs that started in the window.

Definitions. A call is "recorded by the tracker" when it has a tracker event
(tracker.call_start); a call carrying only ceta.* events is not. Outcomes are
compared in CETA's vocabulary (approved, denied, rejected, failed, pending,
unresolved); the tracker's settlement maps as: result error -> failed; refused
and settled by CETA -> rejected; refused otherwise -> denied; approved ->
approved; pending -> pending; anything else -> unresolved. Verdicts compare
CETA's hook_decision (allow, deny, ask, abstain) with the tracker's engine
verdict (approve -> allow, reject -> deny); the delivered verdict is not
compared. is_error compares only when both sides know it. A denial reason is
compared by presence. Several rows for one key are listed as duplicates and
the latest row of each side is compared.

Output (--format): table (default) or json (the full report).

Exit codes: 0 clean report, 2 differences found (anything listed above except
the skipped-line counts), 1 error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCompareTracker(cmd.OutOrStdout(), since, until, format, trackerDB, time.Now())
		},
	}
	cmd.Flags().StringVar(&since, "since", "48h", "Window start: RFC 3339 timestamp, YYYY-MM-DD date, or duration before now (e.g. 48h)")
	cmd.Flags().StringVar(&until, "until", "", "Window end, exclusive (same forms as --since; default: unbounded)")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: table|json")
	cmd.Flags().StringVar(&trackerDB, "tracker-db", "", "Path of the tracker database (default: the tracker's own default path)")
	return cmd
}

func runCompareTracker(out io.Writer, since, until, format, trackerDB string, now time.Time) error {
	if format != "table" && format != "json" {
		return fmt.Errorf("--format must be table or json, got %q", format)
	}
	var win trackercompare.Window
	if since != "" {
		t, err := trackercompare.ParseTime(since, now)
		if err != nil {
			return fmt.Errorf("--since %w", err)
		}
		win.SinceMs = t.UnixMilli()
	}
	if until != "" {
		t, err := trackercompare.ParseTime(until, now)
		if err != nil {
			return fmt.Errorf("--until %w", err)
		}
		win.UntilMs = t.UnixMilli()
		if win.UntilMs <= win.SinceMs {
			return errors.New("--until must be after --since")
		}
	}

	f := sourceFlags{source: sourceTracker, trackerDB: trackerDB}
	adapter, err := trackeradapter.Open(f.trackerPath())
	if err != nil {
		return err
	}
	defer func() { _ = adapter.Close() }()

	store, err := asklog.NewReadOnlyStore(asklog.DefaultDBPath())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	cetaRows, err := store.QueryCompareRows()
	if err != nil {
		return fmt.Errorf("querying CETA rows: %w", err)
	}
	trackerRows, err := adapter.Rows("", nil)
	if err != nil {
		return fmt.Errorf("querying tracker rows: %w", err)
	}
	sources, err := adapter.SessionSources()
	if err != nil {
		return err
	}
	to := int64(math.MaxInt64)
	if win.UntilMs != 0 {
		to = win.UntilMs - 1
	}
	skips, err := adapter.SkippedCounts(win.SinceMs, to)
	if err != nil {
		return err
	}

	rep, err := trackercompare.Compare(trackercompare.Input{
		Window: win, Ceta: cetaRows, Tracker: trackerRows, Sources: sources, Skips: skips,
	})
	if err != nil {
		return err
	}
	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		printCompareTrackerTable(out, rep)
	}
	if !rep.Clean {
		return exitCodeError{code: compareTrackerExitDifferences}
	}
	return nil
}

func printCompareTrackerTable(w io.Writer, r trackercompare.Report) {
	_, _ = fmt.Fprintf(w, "CETA calls: %d  tracker calls: %d  matched: %d\n", r.CetaCalls, r.TrackerCalls, r.Matched)
	_, _ = fmt.Fprintf(w, "\nOnly CETA recorded (%d)\n", len(r.OnlyCeta))
	for _, c := range r.OnlyCeta {
		_, _ = fmt.Fprintf(w, "  %s %s %s (ceta id %d)\n", c.SessionID, c.ToolUseID, c.ToolName, c.CetaID)
	}
	_, _ = fmt.Fprintf(w, "\nOnly the tracker recorded (%d)\n", len(r.OnlyTracker))
	for _, c := range r.OnlyTracker {
		_, _ = fmt.Fprintf(w, "  %s %s %s (call_id %d)\n", c.SessionID, c.ToolUseID, c.ToolName, c.CallID)
	}
	_, _ = fmt.Fprintf(w, "\nDifferences (%d)\n", len(r.Differences))
	cats := make([]string, 0, len(r.DifferenceCounts))
	for c := range r.DifferenceCounts {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	for _, c := range cats {
		_, _ = fmt.Fprintf(w, "  %s: %d\n", c, r.DifferenceCounts[c])
	}
	for _, d := range r.Differences {
		_, _ = fmt.Fprintf(w, "  %s %s %s: ceta=%s tracker=%s (ceta id %d, call_id %d)\n",
			d.SessionID, d.ToolUseID, d.Category, d.Ceta, d.Tracker, d.CetaID, d.CallID)
	}
	_, _ = fmt.Fprintf(w, "\nDuplicate keys (%d)\n", len(r.Duplicates))
	for _, d := range r.Duplicates {
		_, _ = fmt.Fprintf(w, "  %s %s %s x%d\n", d.Side, d.SessionID, d.ToolUseID, d.Count)
	}
	_, _ = fmt.Fprintf(w, "\nCETA rows without a tool_use_id: %d\n", r.UnkeyedCeta)
	_, _ = fmt.Fprintf(w, "\nSessions with CETA rows and no tracker file (%d)\n", len(r.SessionsWithoutTracker))
	for _, s := range r.SessionsWithoutTracker {
		_, _ = fmt.Fprintf(w, "  %s (%d CETA rows)\n", s.SessionID, s.CetaRows)
	}
	k := r.Skips
	_, _ = fmt.Fprintf(w, "\nIngest skipped lines for the window: %d (invalid json %d, missing field %d) of %d read in %d files\n",
		k.SkippedTotal, k.SkippedInvalidJSON, k.SkippedMissingField, k.LinesRead, k.IngestFiles)
	if r.Clean {
		_, _ = fmt.Fprintln(w, "\nResult: clean")
	} else {
		_, _ = fmt.Fprintln(w, "\nResult: differences found")
	}
}
