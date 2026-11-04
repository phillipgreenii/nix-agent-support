package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/shapereplay"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/shapes"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
	"github.com/spf13/cobra"
)

// Phase 4 vocabulary (docket tc-o14i5.5): a DB decision is allow, deny, ask
// or abstain; every Phase 4 artifact says approve, reject, abstain, ask. A NULL
// recorded decision is "unrecorded".
const decisionUnrecorded = "unrecorded"

// decisionRank is the transition-table order of internal/setup/replay_test.go:
// Approve < Abstain < Ask < Reject. Less restrictive means a lower rank.
var decisionRank = map[string]int{"approve": 0, "abstain": 1, "ask": 2, "reject": 3}

// dbDecisionName maps a DB hook_decision string to the Phase 4 vocabulary.
func dbDecisionName(s string) string {
	switch s {
	case "allow":
		return "approve"
	case "deny":
		return "reject"
	case "ask":
		return "ask"
	default:
		return "abstain"
	}
}

// replayEntry is one line of ART/replay.jsonl: every shapes.jsonl field plus
// the replay verdict.
type replayEntry struct {
	shapeEntry
	OldDecision     string   `json:"old_decision"`
	NewDecision     string   `json:"new_decision"`
	NewReason       string   `json:"new_reason"`
	Transition      string   `json:"transition"`
	LessRestrictive bool     `json:"less_restrictive"`
	StateDependent  bool     `json:"state_dependent"`
	StateConsulted  []string `json:"state_consulted"`

	unmapped   bool
	unreadable bool
}

// oldDecisions returns, per shape key, the most common NON-NULL recorded
// decision among the shape's rows (ties go to the more restrictive decision).
// A shape whose rows are all NULL is absent from the result.
func oldDecisions(rows []asklog.DecisionRow) map[string]string {
	counts := map[string]map[string]int{}
	for _, r := range rows {
		if r.HookDecision == nil {
			continue
		}
		k := shapes.Normalize(r).Key()
		if counts[k] == nil {
			counts[k] = map[string]int{}
		}
		counts[k][dbDecisionName(*r.HookDecision)]++
	}
	out := make(map[string]string, len(counts))
	for k, c := range counts {
		best, bestN := "", -1
		for d, n := range c {
			if n > bestN || (n == bestN && decisionRank[d] > decisionRank[best]) {
				best, bestN = d, n
			}
		}
		out[k] = best
	}
	return out
}

// readShapes parses ART/shapes.jsonl.
func readShapes(path string) ([]shapeEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open shapes file (run the shapes subcommand first): %w", err)
	}
	defer func() { _ = f.Close() }()
	var out []shapeEntry
	br := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var e shapeEntry
			if jerr := json.Unmarshal(line, &e); jerr != nil {
				return nil, fmt.Errorf("parse %s line %d: %w", path, len(out)+1, jerr)
			}
			out = append(out, e)
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// repRow is the part of a representative row replay needs.
type repRow struct {
	input shapereplay.Input
}

// lookupRepRows fetches the representative rows by id with the pinned
// read-only recipe: SQL through the read-only store for ceta, RowsByIDs for
// the tracker source.
func lookupRepRows(o replayOpts, ids []int) (map[int]repRow, error) {
	out := map[int]repRow{}
	if len(ids) == 0 {
		return out, nil
	}
	if o.src.isTracker() {
		a, err := trackeradapter.Open(o.src.trackerPath())
		if err != nil {
			return nil, err
		}
		defer func() { _ = a.Close() }()
		labels, err := shapesTrackerLabels(o.db)
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(ids); i += 500 {
			end := min(i+500, len(ids))
			rows, err := a.RowsByIDs(ids[i:end], labels)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				out[r.ID] = toRepRow(r.DecisionRow)
			}
		}
		return out, nil
	}
	path := o.db
	if path == "" {
		path = asklog.DefaultDBPath()
	}
	store, err := asklog.NewReadOnlyStore(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	for i := 0; i < len(ids); i += 500 {
		end := min(i+500, len(ids))
		marks := make([]string, 0, end-i)
		args := make([]any, 0, end-i)
		for _, id := range ids[i:end] {
			marks = append(marks, "?")
			args = append(args, id)
		}
		q := `SELECT id, tool_name, tool_input_json, cwd, permission_mode FROM tool_decisions WHERE id IN (` +
			strings.Join(marks, ",") + `)`
		rs, err := store.DB().Query(q, args...)
		if err != nil {
			return nil, fmt.Errorf("representative-row lookup: %w", err)
		}
		for rs.Next() {
			var r asklog.DecisionRow
			if err := rs.Scan(&r.ID, &r.ToolName, &r.ToolInputJSON, &r.CWD, &r.PermissionMode); err != nil {
				_ = rs.Close()
				return nil, err
			}
			out[r.ID] = toRepRow(r)
		}
		if err := rs.Err(); err != nil {
			_ = rs.Close()
			return nil, err
		}
		_ = rs.Close()
	}
	return out, nil
}

func toRepRow(r asklog.DecisionRow) repRow {
	mode := ""
	if r.PermissionMode != nil {
		mode = *r.PermissionMode
	}
	return repRow{input: shapereplay.Input{
		ToolName: r.ToolName, ToolInputJSON: r.ToolInputJSON, CWD: r.CWD, PermissionMode: mode,
	}}
}

// minID is the lowest id of a shape's example_row_ids (the representative).
func minID(ids []int) (int, bool) {
	if len(ids) == 0 {
		return 0, false
	}
	m := ids[0]
	for _, id := range ids[1:] {
		if id < m {
			m = id
		}
	}
	return m, true
}

// buildReplay replays every shape. rep maps row id to the representative row;
// a shape whose representative is absent is written as unreadable.
func buildReplay(shs []shapeEntry, old map[string]string, rep map[int]repRow, rp *shapereplay.Replayer, stderr io.Writer) []replayEntry {
	out := make([]replayEntry, 0, len(shs))
	for _, s := range shs {
		e := replayEntry{shapeEntry: s, StateConsulted: []string{}}
		if e.Flags == nil {
			e.Flags = []string{}
		}
		if e.PathClasses == nil {
			e.PathClasses = []string{}
		}
		if e.EnvNames == nil {
			e.EnvNames = []string{}
		}
		if e.Programs == nil {
			e.Programs = []string{}
		}
		if e.ExampleRowIDs == nil {
			e.ExampleRowIDs = []int{}
		}
		e.OldDecision = decisionUnrecorded
		if d, ok := old[s.ShapeKey]; ok {
			e.OldDecision = d
		}

		id, haveID := minID(s.ExampleRowIDs)
		row, ok := rep[id]
		if !haveID || !ok {
			e.unreadable = true
			e.NewDecision, e.NewReason = "abstain", "representative-row-unreadable"
			_, _ = fmt.Fprintf(stderr, "unreadable: shape %s: representative row %d not found\n", s.ShapeKey, id)
		} else {
			res := rp.Replay(row.input)
			e.NewDecision, e.NewReason = res.Decision, res.Reason
			e.unmapped = res.Unmapped
			e.StateConsulted = res.Consulted
			e.StateDependent = len(res.Consulted) > 0
		}
		e.Transition = e.OldDecision + "->" + e.NewDecision
		if r, known := decisionRank[e.OldDecision]; known {
			e.LessRestrictive = decisionRank[e.NewDecision] < r
		}
		out = append(out, e)
	}
	return out
}

// writeReplaySummary prints the stdout summary in the pinned order.
func writeReplaySummary(w io.Writer, entries []replayEntry) {
	type agg struct{ shapes, rows int }
	var total, lr, unm, sd agg
	unreadable := 0
	trans := map[string]*agg{}
	for _, e := range entries {
		total.shapes++
		total.rows += e.Rows
		a := trans[e.Transition]
		if a == nil {
			a = &agg{}
			trans[e.Transition] = a
		}
		a.shapes++
		a.rows += e.Rows
		if e.LessRestrictive {
			lr.shapes++
			lr.rows += e.Rows
		}
		if e.unmapped {
			unm.shapes++
			unm.rows += e.Rows
		}
		if e.unreadable {
			unreadable++
		}
		if e.StateDependent {
			sd.shapes++
			sd.rows += e.Rows
		}
	}
	_, _ = fmt.Fprintf(w, "shapes=%d rows=%d\n", total.shapes, total.rows)
	keys := make([]string, 0, len(trans))
	for k := range trans {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "transition %s shapes=%d rows=%d\n", k, trans[k].shapes, trans[k].rows)
	}
	_, _ = fmt.Fprintf(w, "less_restrictive shapes=%d rows=%d\n", lr.shapes, lr.rows)
	_, _ = fmt.Fprintf(w, "abstain-unmapped shapes=%d rows=%d\n", unm.shapes, unm.rows)
	_, _ = fmt.Fprintf(w, "unreadable shapes=%d\n", unreadable)
	_, _ = fmt.Fprintln(w, "## State-dependent (excluded from gates)")
	_, _ = fmt.Fprintf(w, "state-dependent shapes=%d rows=%d\n", sd.shapes, sd.rows)
	for _, e := range entries {
		if e.StateDependent {
			_, _ = fmt.Fprintf(w, "state-dependent %s rows=%d consulted=%s\n", e.ShapeKey, e.Rows, strings.Join(e.StateConsulted, ","))
		}
	}
}

func writeReplayJSONL(w io.Writer, entries []replayEntry) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return bw.Flush()
}

type replayOpts struct {
	since  string
	db     string
	shapes string
	out    string
	src    sourceFlags
}

func newReplayCmd() *cobra.Command {
	var o replayOpts
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Replay each shape's representative row through the NEW effect engine (read-only)",
		Long: `replay reads ART/shapes.jsonl (written by the shapes subcommand), replays the
lowest-id representative row of every shape through the new effect engine
(claudecodeadapter.Evaluate), compares the verdict with the shape's recorded
old-engine decision and writes ART/replay.jsonl, where ART is
${XDG_STATE_HOME:-$HOME/.local/state}/claude-extended-tool-approver/phase4/.

A transition toward less restriction under approve < abstain < ask < reject is
flagged less_restrictive. A verdict whose decision changes when the same input
is replayed under a hermetic environment, config or filesystem twin is tagged
state_dependent (state_consulted names which) and is reported separately: it
is excluded from the gates. A tool the adapter does not map (and a shape whose
representative row cannot be read) is reported as abstain and counted.

The ask log (or, under --source tracker, the tracker database) is opened
read-only and is never written. Replay is offline and single-threaded: the
state twins temporarily change the process environment.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReplay(cmd.OutOrStdout(), cmd.ErrOrStderr(), o)
		},
	}
	cmd.Flags().StringVar(&o.since, "since", "", "Only rows on or after this date, YYYY-MM-DD (default: 30 days before today)")
	cmd.Flags().StringVar(&o.db, "db", "", "Ask-log database path for --source ceta (default: the ask log's own default path)")
	cmd.Flags().StringVar(&o.shapes, "shapes", "", "Shapes file (default: ART/shapes.jsonl)")
	cmd.Flags().StringVar(&o.out, "out", "", "Output file (default: ART/replay.jsonl)")
	addSourceFlags(cmd, &o.src)
	return cmd
}

func runReplay(stdout, stderr io.Writer, o replayOpts) error {
	if err := o.src.validate(); err != nil {
		return err
	}
	since := o.since
	if since == "" {
		since = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}
	dir, err := artDir()
	if err != nil {
		return err
	}
	shapesPath := o.shapes
	if shapesPath == "" {
		shapesPath = filepath.Join(dir, "shapes.jsonl")
	}
	out := o.out
	if out == "" {
		out = filepath.Join(dir, "replay.jsonl")
	}

	shs, err := readShapes(shapesPath)
	if err != nil {
		return err
	}
	rows, err := loadShapeRows(shapesOpts{since: since, db: o.db, src: o.src}, since)
	if err != nil {
		return err
	}
	old := oldDecisions(rows)

	var ids []int
	for _, s := range shs {
		if id, ok := minID(s.ExampleRowIDs); ok {
			ids = append(ids, id)
		}
	}
	rep, err := lookupRepRows(o, ids)
	if err != nil {
		return err
	}

	entries := buildReplay(shs, old, rep, shapereplay.New(), stderr)

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("create %s: %w", out, err)
	}
	if err := writeReplayJSONL(f, entries); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", out, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", out, err)
	}
	_, _ = fmt.Fprintf(stderr, "wrote %s\n", out)
	writeReplaySummary(stdout, entries)
	return nil
}
