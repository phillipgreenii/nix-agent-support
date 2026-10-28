package backfill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Write is one write an apply run made, read from its run record line.
type Write struct {
	At    time.Time
	PR    string
	RunID string
	Rule  string
	Kind  string
	// Op is create, update, reopen, close or annotate.
	Op string
}

// WritesOf lists the writes of one apply-mode record: for each rule, the first
// `applied` actions of that rule in plan order (all of them when the record has no
// counters for the rule but says it was applied). A plan-mode record, a failed run
// and an action that was deduped, skipped or failed are writes of nothing.
func WritesOf(r Record) []Write {
	if r.Mode != ModeApply || r.Decide == nil {
		return nil
	}
	var out []Write
	for _, ro := range r.Decide.Rules {
		n := 0
		switch {
		case ro.Counts != nil:
			n = ro.Counts["applied"]
		case ro.Outcome == "applied":
			n = len(r.Decide.Actions)
		}
		for _, a := range r.Decide.Actions {
			if n == 0 {
				break
			}
			if a.Rule != ro.Rule {
				continue
			}
			out = append(out, Write{At: r.Time, PR: r.Entity, RunID: r.RunID, Rule: a.Rule, Kind: a.Kind, Op: a.Op})
			n--
		}
	}
	return out
}

// FlipBound is the time the by-timestamp listing and the create count start from:
// the EARLIER of the latest flip marker's time and the first apply-mode run record
// after the marker before it, so writes made by live `pr-change` apply runs between
// the deployment apply that changed the role's mode and the marker are not missed.
// It is not pg-desk's persisted lane-level flip time (a display value). False when
// there is neither a marker nor an apply-mode record.
func FlipBound(lg *Log) (time.Time, bool) {
	var bound time.Time
	have := false
	after := 0
	if n := len(lg.FlipMarkers); n > 0 {
		last := lg.FlipMarkers[n-1]
		bound, have = last.T, true
		if n > 1 {
			after = lg.FlipMarkers[n-2].Seq
		}
	}
	for _, r := range lg.Records {
		if r.Mode != ModeApply || r.Seq <= after || r.Time.IsZero() {
			continue
		}
		if !have || r.Time.Before(bound) {
			bound, have = r.Time, true
		}
	}
	return bound, have
}

// WritesSinceFlip lists every write since the flip bound, by timestamp (a tie
// keeps file order): creates, closes, reopens and head updates alike.
func WritesSinceFlip(lg *Log) (writes []Write, bound time.Time, ok bool) {
	bound, ok = FlipBound(lg)
	if !ok {
		return nil, bound, false
	}
	for _, r := range lg.Records {
		if r.Time.Before(bound) {
			continue
		}
		writes = append(writes, WritesOf(r)...)
	}
	sort.SliceStable(writes, func(i, j int) bool { return writes[i].At.Before(writes[j].At) })
	return writes, bound, true
}

// CreatesSinceFlip counts, per kind, the creates since the flip bound of the PRs
// in ids (the enumerated set).
func CreatesSinceFlip(lg *Log, ids map[string]bool) map[string]int {
	out := map[string]int{}
	ws, _, ok := WritesSinceFlip(lg)
	if !ok {
		return out
	}
	for _, w := range ws {
		if w.Op == "create" && ids[w.PR] {
			out[w.Kind]++
		}
	}
	return out
}

// OverLimit reports the first kind (in name order) whose creates exceed the
// operator's expected number; a kind with no limit has limit 0.
func OverLimit(creates, limits map[string]int) (kind string, n, limit int, over bool) {
	for _, k := range sortedKeys(creates) {
		if creates[k] > limits[k] {
			return k, creates[k], limits[k], true
		}
	}
	return "", 0, 0, false
}

// FlipMarkerOptions configure WriteFlipMarker.
type FlipMarkerOptions struct {
	RunRecord string
	// Time is the flip's effective time; zero means now. A runbook writes it just
	// before the deployment apply that changes the role's mode.
	Time time.Time
	Now  func() time.Time
	// EpicBead, when set, gets a comment with the marker (a copy: the run record is
	// the authoritative source). Backend and BeadsDir select the tracker the epic
	// lives in, as the connector understands them.
	EpicBead  string
	Backend   string
	BeadsDir  string
	Connector string
	Env       []string
	Runner    Runner
}

// ErrEpicComment means the marker was recorded but the comment on the epic bead
// failed; the run record is authoritative, so the caller reports it and goes on.
var ErrEpicComment = errors.New("the flip marker was recorded in the run record but the comment on the epic bead failed")

// WriteFlipMarker appends the flip marker to the run record, and then comments on
// the epic bead.
func WriteFlipMarker(ctx context.Context, o FlipMarkerOptions) (Marker, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	at := now().UTC().Truncate(time.Second)
	eff := at
	if !o.Time.IsZero() {
		eff = o.Time.UTC()
	}
	m := Marker{
		Contract: FlipMarkerContract, Kind: "flip-marker",
		At: at.Format(time.RFC3339), Time: eff.Format(time.RFC3339), Mode: ModeApply,
	}
	if o.RunRecord == "" {
		return m, fmt.Errorf("%w: no run record file is configured", ErrUsage)
	}
	if err := AppendLine(o.RunRecord, m); err != nil {
		return m, err
	}
	if o.EpicBead == "" {
		return m, nil
	}
	run := o.Runner
	if run == nil {
		run = ExecRunner
	}
	conn := o.Connector
	if conn == "" {
		conn = "pg-connector"
	}
	body := fmt.Sprintf("PR lane flip marker: mode=%s time=%s (written %s). The run record %s is the authoritative copy; every write since this time is listed by `pg-pr-backfill writes`.", m.Mode, m.Time, m.At, o.RunRecord)
	args := []string{"issue", "comment", o.EpicBead, "--body", body}
	if o.Backend != "" {
		args = append(args, "--backend", o.Backend)
	}
	env := append([]string{}, o.Env...)
	if o.BeadsDir != "" {
		env = append(env, "PG_CONNECTOR_ISSUE_BEADS_DIR="+o.BeadsDir)
	}
	res := run(ctx, Call{Name: conn, Args: args, Env: env})
	if res.StartErr != nil || res.Code != 0 {
		return m, fmt.Errorf("%w: %s", ErrEpicComment, firstLine(string(res.Stderr)+string(res.Stdout)+errText(res.StartErr)))
	}
	return m, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// PrintWrites prints the by-timestamp listing: one line per write, the bound first.
func PrintWrites(w io.Writer, lg *Log) (int, bool) {
	ws, bound, ok := WritesSinceFlip(lg)
	if !ok {
		fmt.Fprintln(w, "writes: no flip marker and no apply-mode run record: nothing to list (write the marker with flip-marker)")
		return 0, false
	}
	fmt.Fprintf(w, "writes since: %s (the earlier of the flip marker and the first apply-mode run record)\n", bound.Format(time.RFC3339))
	for _, x := range ws {
		fmt.Fprintf(w, "%s %s %s %s rule=%s run=%s\n", x.At.Format(time.RFC3339), x.PR, x.Op, x.Kind, x.Rule, x.RunID)
	}
	byOp := map[string]int{}
	for _, x := range ws {
		byOp[x.Op]++
	}
	var parts []string
	for _, k := range sortedKeys(byOp) {
		parts = append(parts, fmt.Sprintf("%s=%d", k, byOp[k]))
	}
	fmt.Fprintf(w, "writes total=%d %s\n", len(ws), strings.Join(parts, " "))
	return len(ws), true
}
