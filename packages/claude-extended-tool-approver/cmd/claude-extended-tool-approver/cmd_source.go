package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
	"github.com/spf13/cobra"
)

// The two histories evaluate, report and show (and the two label commands) can
// read. They are never mixed: --source tracker shows only calls the
// claude-tool-tracker recorded, --source ceta shows CETA's own ask log. The
// default stays ceta until the cut-over flips it.
const (
	sourceCeta    = "ceta"
	sourceTracker = "tracker"
)

// sourceFlags holds the values of --source and --tracker-db.
type sourceFlags struct {
	source    string
	trackerDB string
}

func addSourceFlags(cmd *cobra.Command, f *sourceFlags) {
	cmd.Flags().StringVar(&f.source, "source", sourceCeta, "History to read: ceta (CETA's ask log) or tracker (the claude-tool-tracker database, read-only)")
	cmd.Flags().StringVar(&f.trackerDB, "tracker-db", "", "Path of the tracker database for --source tracker (default: the tracker's own default path)")
}

// validate rejects an unknown --source before anything is opened.
func (f sourceFlags) validate() error {
	switch f.source {
	case sourceCeta, sourceTracker:
		return nil
	}
	return fmt.Errorf("--source must be ceta or tracker, got %q", f.source)
}

func (f sourceFlags) isTracker() bool { return f.source == sourceTracker }

func (f sourceFlags) trackerPath() string {
	if f.trackerDB != "" {
		return f.trackerDB
	}
	return trackeradapter.DefaultPath()
}

// openTrackerOrExit opens the tracker database read-only. Any failure is a
// clear error and exit 1: a tracker problem must never read as an empty
// history.
func openTrackerOrExit(f sourceFlags) *trackeradapter.Adapter {
	a, err := trackeradapter.Open(f.trackerPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	return a
}

// loadTrackerLabels reads CETA's tracker labels (offline validation data that
// stays in CETA's own database). A missing CETA database, or one that predates
// the labels table, simply has none.
func loadTrackerLabels() map[asklog.CallKey]asklog.TrackerLabel {
	none := map[asklog.CallKey]asklog.TrackerLabel{}
	path := asklog.DefaultDBPath()
	if _, err := os.Stat(path); err != nil {
		return none
	}
	store, err := asklog.NewReadOnlyStore(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()
	labels, err := store.TrackerLabels()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading tracker labels: %v\n", err)
		os.Exit(1)
	}
	return labels
}

// loadSourceRows returns the rows evaluate and report grade, plus, under
// --source tracker, the tracker rows by id (for the extra output fields).
func loadSourceRows(f sourceFlags, sinceDate string) ([]asklog.DecisionRow, map[int]trackeradapter.Row) {
	if f.isTracker() {
		a := openTrackerOrExit(f)
		defer func() { _ = a.Close() }()
		rows, err := a.Rows(sinceDate, loadTrackerLabels())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error querying rows: %v\n", err)
			os.Exit(1)
		}
		out := make([]asklog.DecisionRow, len(rows))
		byID := make(map[int]trackeradapter.Row, len(rows))
		for i, r := range rows {
			out[i] = r.DecisionRow
			byID[r.CallID] = r
		}
		return out, byID
	}
	// evaluate and report only ever read rows — they never write to the ask
	// log, so it opens read-only (pg2-cbihz).
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
	return rows, nil
}

// resolveTrackerKeys maps call_ids to the (session_id, tool_use_id) keys the
// labels are stored under; an unknown call_id is an error.
func resolveTrackerKeys(f sourceFlags, ids []int) []asklog.CallKey {
	a := openTrackerOrExit(f)
	defer func() { _ = a.Close() }()
	keys := make([]asklog.CallKey, 0, len(ids))
	for _, id := range ids {
		k, ok, err := a.Key(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "error: call_id %d is not in the tracker database\n", id)
			os.Exit(1)
		}
		keys = append(keys, k)
	}
	return keys
}

// trackerSourceTag is the value evaluateFilters records so a baseline captured
// from one history is never diffed against the other. It is empty for ceta, so
// baselines written before --source existed stay valid.
func trackerSourceTag(f sourceFlags) string {
	if f.isTracker() {
		return sourceTracker
	}
	return ""
}

// applyTracker fills evalResult's tracker-only fields.
func (r *evalResult) applyTracker(t trackeradapter.Row) {
	r.SettledBy = t.SettledBy
	r.Settlement = t.Settlement
	conv := t.ConvertedAsk
	r.ConvertedAsk = &conv
	r.EngineVerdict = t.EngineVerdict
	r.DeliveredVerdict = t.DeliveredVerdict
	r.IsError = t.IsError
	r.DenialReason = t.DenialReason
}
