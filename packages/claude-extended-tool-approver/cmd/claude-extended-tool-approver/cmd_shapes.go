package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/shapes"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
	"github.com/spf13/cobra"
)

// Phase 4 (docket tc-o14i5.5) baseline: 77,114 distinct tool_input_hash values
// over 84,581 rows in the 30 days before 2026-11-01. The collapse ratio the
// shapes command reports is read against it.
const (
	shapesBaselineDistinct = 77114
	shapesBaselineRows     = 84581
)

// artDir is the machine-local Phase 4 artifact directory,
// ${XDG_STATE_HOME:-$HOME/.local/state}/claude-extended-tool-approver/phase4/.
// It is outside the repo and created on demand.
func artDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve state directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "claude-extended-tool-approver", "phase4"), nil
}

// shapeEntry is one line of ART/shapes.jsonl.
type shapeEntry struct {
	ShapeKey      string   `json:"shape_key"`
	Tool          string   `json:"tool"`
	Program       string   `json:"program"`
	Subcommand    string   `json:"subcommand"`
	Flags         []string `json:"flags"`
	PathClasses   []string `json:"path_classes"`
	EnvNames      []string `json:"env_names"`
	Programs      []string `json:"programs"`
	Family        string   `json:"family"`
	Rows          int      `json:"rows"`
	ExampleRowIDs []int    `json:"example_row_ids"`
}

// buildShapeTable groups rows by shape. The result is sorted by rows
// descending, then shape_key ascending.
func buildShapeTable(rows []asklog.DecisionRow) []shapeEntry {
	byShape := map[shapes.Shape]*shapeEntry{}
	ids := map[shapes.Shape][]int{}
	for _, r := range rows {
		s := shapes.Normalize(r)
		e, ok := byShape[s]
		if !ok {
			e = &shapeEntry{
				ShapeKey:    s.Key(),
				Tool:        s.Tool,
				Program:     s.Program,
				Subcommand:  s.Subcommand,
				Flags:       s.FlagList(),
				PathClasses: s.PathClassList(),
				EnvNames:    s.EnvNameList(),
				Programs:    s.Programs(),
				Family:      s.Family(),
			}
			byShape[s] = e
		}
		e.Rows++
		ids[s] = append(ids[s], r.ID)
	}
	out := make([]shapeEntry, 0, len(byShape))
	for s, e := range byShape {
		sort.Ints(ids[s])
		n := len(ids[s])
		if n > 5 {
			n = 5
		}
		e.ExampleRowIDs = append([]int{}, ids[s][:n]...)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rows != out[j].Rows {
			return out[i].Rows > out[j].Rows
		}
		return out[i].ShapeKey < out[j].ShapeKey
	})
	return out
}

// writeShapesJSONL writes one JSON object per line.
func writeShapesJSONL(w io.Writer, entries []shapeEntry) error {
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

// collapseLine is the report's last stdout line. The decimal is shapes/rows
// rounded half up to four places, computed in integers so no float rounding
// can move a boundary.
func collapseLine(rows, nShapes int) string {
	ratio := "n/a"
	if rows > 0 {
		q := (int64(nShapes)*20000 + int64(rows)) / (2 * int64(rows))
		ratio = fmt.Sprintf("%d.%04d", q/10000, q%10000)
	}
	return fmt.Sprintf("rows=%d shapes=%d collapse_ratio=%d/%d=%s baseline=%d/%d",
		rows, nShapes, nShapes, rows, ratio, shapesBaselineDistinct, shapesBaselineRows)
}

type shapesOpts struct {
	since string
	db    string
	out   string
	src   sourceFlags
}

func newShapesCmd() *cobra.Command {
	var o shapesOpts
	cmd := &cobra.Command{
		Use:   "shapes",
		Short: "Reduce decision-DB rows to shapes and report the collapse ratio (read-only)",
		Long: `shapes turns every decision-DB row into a SHAPE (tool, program, subcommand,
flag set, positional path classes, env names), counts the rows of each shape and
writes the table, most-populated shape first, to ART/shapes.jsonl, where ART is
${XDG_STATE_HOME:-$HOME/.local/state}/claude-extended-tool-approver/phase4/.

A shape is derived from the tool input only, never from the recorded outcome.
The last stdout line is

  rows=<N> shapes=<S> collapse_ratio=<S>/<N>=<decimal> baseline=77114/84581

The ask log (or, under --source tracker, the tracker database) is opened
read-only and is never written.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShapes(cmd.OutOrStdout(), o)
		},
	}
	cmd.Flags().StringVar(&o.since, "since", "", "Only rows on or after this date, YYYY-MM-DD (default: 30 days before today)")
	cmd.Flags().StringVar(&o.db, "db", "", "Ask-log database path for --source ceta (default: the ask log's own default path)")
	cmd.Flags().StringVar(&o.out, "out", "", "Output file (default: ART/shapes.jsonl)")
	addSourceFlags(cmd, &o.src)
	return cmd
}

func runShapes(stdout io.Writer, o shapesOpts) error {
	if err := o.src.validate(); err != nil {
		return err
	}
	since := o.since
	if since == "" {
		since = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}
	rows, err := loadShapeRows(o, since)
	if err != nil {
		return err
	}
	entries := buildShapeTable(rows)

	out := o.out
	if out == "" {
		dir, err := artDir()
		if err != nil {
			return err
		}
		out = filepath.Join(dir, "shapes.jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("create %s: %w", out, err)
	}
	if err := writeShapesJSONL(f, entries); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", out, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", out, err)
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s\n", out)
	_, _ = fmt.Fprintln(stdout, collapseLine(len(rows), len(entries)))
	return nil
}

// loadShapeRows reads the window's rows from the chosen history. Both stores
// are opened read-only.
func loadShapeRows(o shapesOpts, since string) ([]asklog.DecisionRow, error) {
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
		trows, err := a.Rows(since, labels)
		if err != nil {
			return nil, fmt.Errorf("querying rows: %w", err)
		}
		rows := make([]asklog.DecisionRow, len(trows))
		for i, r := range trows {
			rows[i] = r.DecisionRow
		}
		return rows, nil
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
	rows, err := store.QueryRows(since)
	if err != nil {
		return nil, fmt.Errorf("querying rows: %w", err)
	}
	return rows, nil
}

// shapesTrackerLabels reads CETA's tracker labels exactly as evaluate
// --source tracker does, so the two commands see the same row set. A missing
// ask log, or one that predates the labels table, has none.
func shapesTrackerLabels(db string) (map[asklog.CallKey]asklog.TrackerLabel, error) {
	none := map[asklog.CallKey]asklog.TrackerLabel{}
	path := db
	if path == "" {
		path = asklog.DefaultDBPath()
	}
	if _, err := os.Stat(path); err != nil {
		return none, nil
	}
	store, err := asklog.NewReadOnlyStore(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	labels, err := store.TrackerLabels()
	if err != nil {
		return nil, fmt.Errorf("reading tracker labels: %w", err)
	}
	return labels, nil
}
