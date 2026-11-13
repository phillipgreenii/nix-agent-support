package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/shapes"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
	"github.com/spf13/cobra"
)

// Label explanation prefixes (docket tc-o14i5.5, "Label strings"). A proposed
// label's explanation begins llmProposedPrefix; an operator-confirmed one
// begins operatorReviewedPrefix and is never overwritten by this command.
const (
	llmProposedPrefix      = "LLM-proposed:"
	operatorReviewedPrefix = "operator-reviewed:"
)

// proposalVerdicts maps the Phase 4 vocabulary of a proposal to the DB
// vocabulary of correct_hook_decision. "ask" is deliberately absent (rule R5: a
// human prompt should essentially never be emitted) and "exclude" is handled
// separately because it is not a verdict.
var proposalVerdicts = map[string]string{"approve": "allow", "reject": "deny", "abstain": "abstain"}

const proposalExclude = "exclude"

// proposalInput is one line of ART/proposals-input.jsonl. Pointers detect a
// missing field.
type proposalInput struct {
	ShapeKey *string `json:"shape_key"`
	Proposed *string `json:"proposed"`
	Reason   *string `json:"reason"`
}

// proposalEntry is one line of ART/proposals.jsonl.
type proposalEntry struct {
	ShapeKey     string `json:"shape_key"`
	Proposed     string `json:"proposed"`
	Reason       string `json:"reason"`
	RowIDsLabled []int  `json:"row_ids_labeled"`
	Source       string `json:"source"`
}

type labelProposeOpts struct {
	since  string
	db     string
	in     string
	out    string
	dryRun bool
	force  bool
	src    sourceFlags
}

func newLabelProposeCmd() *cobra.Command {
	var o labelProposeOpts
	cmd := &cobra.Command{
		Use:   "label-propose",
		Short: "Apply LLM-proposed verdict labels, one proposal per shape",
		Long: `label-propose reads ART/proposals-input.jsonl (objects shape_key, proposed,
reason; proposed is approve, reject, abstain or exclude), recomputes the shapes
of the window, and records each proposal on EVERY row of its shape through the
existing labelling tooling: approve, reject and abstain call SetCorrectDecision
(as set-correct-decision does) with the explanation "LLM-proposed: <reason>";
exclude calls MarkExcluded (as mark-excluded does). ART is
${XDG_STATE_HOME:-$HOME/.local/state}/claude-extended-tool-approver/phase4/.

ART/proposals.jsonl is rewritten atomically as the union of its previous
contents and this run's lines, keyed by shape_key, with the row ids labelled and
the source.

A shape is skipped when any of its rows already carries an "LLM-proposed:" or
"operator-reviewed:" explanation, unless --force is given; --force relabels only
rows that carry "LLM-proposed:" (or no label at all), never an
"operator-reviewed:" row nor a row labelled by hand. A line proposing "ask" (or
anything outside approve, reject, abstain, exclude), a malformed line, and a
shape that matches nothing in the window are reported on stderr and make the
exit status non-zero; the other lines are still processed.

--dry-run writes only the manifest and touches no database. This command is
the only reader of the ask log that opens it read-write, and only to label.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLabelPropose(cmd.OutOrStdout(), cmd.ErrOrStderr(), o)
		},
	}
	cmd.Flags().StringVar(&o.since, "since", "", "Only rows on or after this date, YYYY-MM-DD (default: 30 days before today)")
	cmd.Flags().StringVar(&o.db, "db", "", "Ask-log database path (default: the ask log's own default path)")
	cmd.Flags().StringVar(&o.in, "in", "", "Proposals input file (default: ART/proposals-input.jsonl)")
	cmd.Flags().StringVar(&o.out, "out", "", "Proposals manifest file (default: ART/proposals.jsonl)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Write only the manifest; label nothing")
	cmd.Flags().BoolVar(&o.force, "force", false, "Relabel rows that carry an LLM-proposed: label (never an operator-reviewed: one)")
	addSourceFlags(cmd, &o.src)
	return cmd
}

// rowLabel is what is already recorded on a row.
type rowLabel struct {
	decision    string
	explanation string
}

// labelable reports whether label-propose may write to a row carrying l.
// A row with no label at all is labelable; a row carrying LLM-proposed: is
// labelable only with force; anything else (operator-reviewed:, or a label set
// by hand with some other explanation) is never overwritten.
func (l rowLabel) labelable(force bool) bool {
	switch {
	case strings.HasPrefix(l.explanation, llmProposedPrefix):
		return force
	case strings.HasPrefix(l.explanation, operatorReviewedPrefix):
		return false
	}
	return l.decision == "" && l.explanation == ""
}

func (l rowLabel) managed() bool {
	return strings.HasPrefix(l.explanation, llmProposedPrefix) ||
		strings.HasPrefix(l.explanation, operatorReviewedPrefix)
}

// labelTarget is the writer/reader pair of one source.
type labelTarget struct {
	src sourceFlags
	db  string

	tracker *trackeradapter.Adapter
	keys    map[int]asklog.CallKey
	labels  map[asklog.CallKey]asklog.TrackerLabel
}

func (t *labelTarget) close() {
	if t.tracker != nil {
		_ = t.tracker.Close()
	}
}

func (t *labelTarget) dbPath() string {
	if t.db != "" {
		return t.db
	}
	return asklog.DefaultDBPath()
}

// loadRows reads the window's rows (the same rows the shapes command saw) and
// prepares the existing-label lookup.
func (t *labelTarget) loadRows(since string) ([]asklog.DecisionRow, error) {
	rows, err := loadShapeRows(shapesOpts{since: since, db: t.db, src: t.src}, since)
	if err != nil {
		return nil, err
	}
	if t.src.isTracker() {
		a, err := trackeradapter.Open(t.src.trackerPath())
		if err != nil {
			return nil, err
		}
		t.tracker = a
		t.keys = map[int]asklog.CallKey{}
		if t.labels, err = shapesTrackerLabels(t.db); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// existing returns the labels already recorded on ids.
func (t *labelTarget) existing(ids []int) (map[int]rowLabel, error) {
	out := make(map[int]rowLabel, len(ids))
	if t.src.isTracker() {
		for _, id := range ids {
			k, ok := t.keys[id]
			if !ok {
				var err error
				k, ok, err = t.tracker.Key(id)
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, fmt.Errorf("call_id %d is not in the tracker database", id)
				}
				t.keys[id] = k
			}
			var l rowLabel
			if tl, has := t.labels[k]; has {
				l.explanation = tl.CorrectDecExplanation
				if tl.CorrectDec != nil {
					l.decision = *tl.CorrectDec
				}
			}
			out[id] = l
		}
		return out, nil
	}
	store, err := asklog.NewReadOnlyStore(t.dbPath())
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
		rs, err := store.DB().QueryContext(context.Background(), `SELECT id, COALESCE(correct_hook_decision,''),
			COALESCE(correct_hook_decision_explanation,'') FROM tool_decisions WHERE id IN (`+
			strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("reading existing labels: %w", err)
		}
		for rs.Next() {
			var id int
			var l rowLabel
			if err := rs.Scan(&id, &l.decision, &l.explanation); err != nil {
				_ = rs.Close()
				return nil, err
			}
			out[id] = l
		}
		if err := rs.Err(); err != nil {
			_ = rs.Close()
			return nil, err
		}
		_ = rs.Close()
	}
	return out, nil
}

// labelWrite is one planned database write.
type labelWrite struct {
	ids         []int
	exclude     bool
	decision    string
	explanation string
}

// apply performs the writes through the existing store methods.
func (t *labelTarget) apply(writes []labelWrite) error {
	if len(writes) == 0 {
		return nil
	}
	store, err := asklog.NewStore(t.dbPath())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	for _, w := range writes {
		switch {
		case t.src.isTracker():
			keys := make([]asklog.CallKey, 0, len(w.ids))
			for _, id := range w.ids {
				keys = append(keys, t.keys[id])
			}
			if w.exclude {
				err = store.MarkTrackerExcluded(keys, w.explanation)
			} else {
				err = store.SetTrackerCorrectDecision(keys, w.decision, w.explanation)
			}
		case w.exclude:
			err = store.MarkExcluded(w.ids, w.explanation)
		default:
			err = store.SetCorrectDecision(w.ids, w.decision, w.explanation)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// readManifest parses an existing proposals manifest. A missing file is empty.
func readManifest(path string) ([]proposalEntry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []proposalEntry
	br := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var e proposalEntry
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

// writeManifest rewrites the manifest atomically (temp file in the same
// directory, then rename).
func writeManifest(path string, entries []proposalEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".proposals-*.tmp")
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(tmp)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	for _, e := range entries {
		if e.RowIDsLabled == nil {
			e.RowIDsLabled = []int{}
		}
		if err := enc.Encode(e); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// parsedProposal is a validated input line.
type parsedProposal struct {
	line     int
	shapeKey string
	proposed string
	reason   string
}

func unionIDs(a, b []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, s := range [][]int{a, b} {
		for _, id := range s {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Ints(out)
	if out == nil {
		out = []int{}
	}
	return out
}

func runLabelPropose(stdout, stderr io.Writer, o labelProposeOpts) (retErr error) {
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
	in, out := o.in, o.out
	if in == "" {
		in = filepath.Join(dir, "proposals-input.jsonl")
	}
	if out == "" {
		out = filepath.Join(dir, "proposals.jsonl")
	}

	inFile, err := os.Open(in)
	if err != nil {
		return fmt.Errorf("open proposals input: %w", err)
	}
	defer func() { _ = inFile.Close() }()

	bad := 0
	reject := func(line int, format string, args ...any) {
		bad++
		_, _ = fmt.Fprintf(stderr, "line %d: %s\n", line, fmt.Sprintf(format, args...))
	}

	// Parse and validate every line first; the last valid line per shape wins.
	var parsed []parsedProposal
	br := bufio.NewReaderSize(inFile, 1<<20)
	for n := 1; ; n++ {
		raw, rerr := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(raw))) > 0 {
			var pi proposalInput
			switch {
			case json.Unmarshal(raw, &pi) != nil:
				reject(n, "malformed JSON")
			case pi.ShapeKey == nil || *pi.ShapeKey == "":
				reject(n, "missing shape_key")
			case pi.Proposed == nil || *pi.Proposed == "":
				reject(n, "missing proposed")
			case pi.Reason == nil || strings.TrimSpace(*pi.Reason) == "":
				reject(n, "missing reason")
			default:
				if _, ok := proposalVerdicts[*pi.Proposed]; !ok && *pi.Proposed != proposalExclude {
					reject(n, "proposed %q is not one of approve, reject, abstain, exclude; nothing written", *pi.Proposed)
				} else {
					parsed = append(parsed, parsedProposal{line: n, shapeKey: *pi.ShapeKey, proposed: *pi.Proposed, reason: *pi.Reason})
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	lastFor := map[string]int{}
	for i, p := range parsed {
		lastFor[p.shapeKey] = i
	}

	tgt := &labelTarget{src: o.src, db: o.db}
	defer tgt.close()
	rows, err := tgt.loadRows(since)
	if err != nil {
		return err
	}
	idsByShape := map[string][]int{}
	for _, r := range rows {
		k := shapes.Normalize(r).Key()
		idsByShape[k] = append(idsByShape[k], r.ID)
	}

	manifest, err := readManifest(out)
	if err != nil {
		return err
	}
	pos := map[string]int{}
	for i, e := range manifest {
		pos[e.ShapeKey] = i
	}
	upsert := func(e proposalEntry) {
		if i, ok := pos[e.ShapeKey]; ok {
			manifest[i] = e
			return
		}
		pos[e.ShapeKey] = len(manifest)
		manifest = append(manifest, e)
	}

	var writes []labelWrite
	labeledShapes, labeledRows, skipped := 0, 0, 0
	for i, p := range parsed {
		if lastFor[p.shapeKey] != i {
			continue // superseded by a later line for the same shape
		}
		ids := idsByShape[p.shapeKey]
		if len(ids) == 0 {
			if prev, ok := pos[p.shapeKey]; ok && p.proposed == proposalExclude && manifest[prev].Proposed == proposalExclude {
				skipped++ // already excluded on an earlier run
				continue
			}
			reject(p.line, "shape_key %q matches no shape in the window", p.shapeKey)
			continue
		}
		sort.Ints(ids)
		existing, err := tgt.existing(ids)
		if err != nil {
			return err
		}
		managed := false
		var target, protected []int
		for _, id := range ids {
			l := existing[id]
			if l.managed() {
				managed = true
			}
			if l.labelable(o.force) {
				target = append(target, id)
			} else {
				protected = append(protected, id)
			}
		}
		prev, hasPrev := pos[p.shapeKey]
		if (managed && !o.force) || len(target) == 0 {
			skipped++
			if !hasPrev && o.dryRun {
				// A dry run reports the already-labelled ids of a skipped shape.
				upsert(proposalEntry{ShapeKey: p.shapeKey, Proposed: p.proposed, Reason: p.reason, RowIDsLabled: protected, Source: o.src.source})
			}
			continue
		}
		decision, explanation := proposalVerdicts[p.proposed], llmProposedPrefix+" "+p.reason
		isExclude := p.proposed == proposalExclude
		if isExclude {
			decision, explanation = "", p.reason
		}
		if !o.dryRun {
			writes = append(writes, labelWrite{ids: target, exclude: isExclude, decision: decision, explanation: explanation})
		}
		var before []int
		if hasPrev {
			before = manifest[prev].RowIDsLabled
		}
		upsert(proposalEntry{
			ShapeKey: p.shapeKey, Proposed: p.proposed, Reason: p.reason,
			RowIDsLabled: unionIDs(before, target), Source: o.src.source,
		})
		labeledShapes++
		labeledRows += len(target)
	}

	// Labels first, then the manifest, so the manifest never claims a row that
	// was not written; a failed apply leaves the manifest untouched.
	if err := tgt.apply(writes); err != nil {
		return err
	}
	if err := writeManifest(out, manifest); err != nil {
		return err
	}

	verb := "labeled"
	if o.dryRun {
		verb = "would label"
	}
	_, _ = fmt.Fprintf(stdout, "%s shapes=%d rows=%d skipped=%d rejected_lines=%d manifest=%s\n",
		verb, labeledShapes, labeledRows, skipped, bad, out)
	if bad > 0 {
		return fmt.Errorf("%d input line(s) rejected", bad)
	}
	return nil
}
