// Package pull implements one work-report pull (design 7.3): exec
// `pg-connector activity list` for a resolved range, validate and map every
// item to a store entry, append them idempotently, and record one pull row and
// one log line per source.
//
// A pull never fails as a whole for one source (INV-DEGRADE-1): every problem
// is expressed as a degraded outcome row, and the process exit code is
// computed from work-report's own rows (Result.ExitCode), never propagated
// from pg-connector's exit code.
package pull

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/telemetry"
)

// Outcome statuses (the pull row's status values).
const (
	StatusSucceeded = "succeeded"
	StatusDegraded  = "degraded"
	StatusDisabled  = "disabled"
)

// Reasons the pull itself produces.
const (
	ReasonDisabledByConfig = "disabled by configuration"
	ReasonNotApplicable    = "not applicable"
	ReasonTruncated        = "truncated: re-pull a narrower range"
)

// unknownSource names the single row used when pg-connector could not run and
// no source is configured or pinned.
const unknownSource = pgconn.Binary

// Deps are a pull's collaborators. Log may be nil.
type Deps struct {
	Cfg   config.Config
	Store *store.Store
	Conn  pgconn.Runner
	Log   *telemetry.Logger
}

// Opts selects what to pull. Sources, when non-empty, pins one --backend call
// per source; otherwise one fan-out call is made. Now stamps the pull.
type Opts struct {
	Range   rangespec.Range
	Sources []string
	Now     time.Time
}

// OutcomeRow is one source's outcome; its JSON form is the --output json
// document's sources[] element.
type OutcomeRow struct {
	Source    string `json:"source"`
	Status    string `json:"status"`
	Count     int    `json:"count"`
	Unchanged int    `json:"unchanged"`
	Rejected  int    `json:"rejected"`
	Truncated bool   `json:"truncated"`
	Reason    string `json:"reason,omitempty"`
}

// Result is a pull's outcome. CouldNotRun is true when pg-connector could not
// be executed, or exited non-zero with no decodable sources[] document, or the
// store was locked.
type Result struct {
	Rows        []OutcomeRow
	CouldNotRun bool
}

// ExitCode is the fan-out exit code computed from the rows: 0 when every
// attempted (non-disabled) source succeeded, 2 when any degraded, 3 when all
// attempted sources degraded.
func (r Result) ExitCode() int {
	attempted, degraded := 0, 0
	for _, row := range r.Rows {
		if row.Status == StatusDisabled {
			continue
		}
		attempted++
		if row.Status == StatusDegraded {
			degraded++
		}
	}
	switch {
	case degraded == 0:
		return 0
	case degraded == attempted:
		return 3
	default:
		return 2
	}
}

// unit is one planned step: a disabled source (reported, never invoked), a
// pinned --backend call, or the single fan-out call (backend "").
type unit struct {
	backend  string
	disabled bool
}

func plan(cfg config.Config, pinned []string) []unit {
	if len(pinned) == 0 {
		return []unit{{}}
	}
	seen := map[string]bool{}
	var units []unit
	for _, s := range pinned {
		if seen[s] {
			continue
		}
		seen[s] = true
		units = append(units, unit{backend: s, disabled: !cfg.SourceEnabled(s)})
	}
	return units
}

// failRows are the rows for a unit that produced no document: every source it
// would have covered, degraded with reason. A fan-out covers every configured
// source (disabled ones stay disabled), or a single "pg-connector" row when
// no source is enabled or known.
func failRows(cfg config.Config, u unit, reason string) []OutcomeRow {
	if u.backend != "" {
		return []OutcomeRow{{Source: u.backend, Status: StatusDegraded, Reason: reason}}
	}
	keys := make([]string, 0, len(cfg.Sources))
	for k := range cfg.Sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows []OutcomeRow
	enabled := 0
	for _, k := range keys {
		if cfg.SourceEnabled(k) {
			enabled++
			rows = append(rows, OutcomeRow{Source: k, Status: StatusDegraded, Reason: reason})
		} else {
			rows = append(rows, OutcomeRow{Source: k, Status: StatusDisabled, Reason: ReasonDisabledByConfig})
		}
	}
	if enabled == 0 {
		rows = append(rows, OutcomeRow{Source: unknownSource, Status: StatusDegraded, Reason: reason})
	}
	return rows
}

// Unavailable builds the CouldNotRun Result for a pull that cannot start at
// all (for example the store is locked at open time): every source the pull
// would have covered is degraded with reason, nothing is stored, and one log
// line per source is still written.
func Unavailable(d Deps, o Opts, reason string) Result {
	res := Result{CouldNotRun: true}
	for _, u := range plan(d.Cfg, o.Sources) {
		if u.disabled {
			res.Rows = append(res.Rows, disabledRow(u.backend))
			continue
		}
		res.Rows = append(res.Rows, failRows(d.Cfg, u, reason)...)
	}
	for _, row := range res.Rows {
		logRow(d, o, row, o.Now, 0)
	}
	return res
}

func disabledRow(source string) OutcomeRow {
	return OutcomeRow{Source: source, Status: StatusDisabled, Reason: ReasonDisabledByConfig}
}

// run carries one Run's mutable state.
type run struct {
	d    Deps
	o    Opts
	wall time.Time // wall-clock start, so row times advance from o.Now
	res  Result
	// lockReason is non-empty once the store reported ErrLocked: every row from
	// then on is degraded with it.
	lockReason string
}

// Run performs one pull. It returns a non-nil error only for failures that are
// not a degraded outcome (an unexpected store error, cancellation); everything
// pg-connector or a locked store can do is reported in the Result.
func Run(ctx context.Context, d Deps, o Opts) (Result, error) {
	r := &run{d: d, o: o, wall: time.Now()}
	for _, u := range plan(d.Cfg, o.Sources) {
		if err := ctx.Err(); err != nil {
			return r.res, err
		}
		unitStart := time.Now()
		var err error
		if u.disabled {
			err = r.emit(ctx, disabledRow(u.backend), unitStart)
		} else {
			err = r.runUnit(ctx, u, unitStart)
		}
		if err != nil {
			return r.res, err
		}
	}
	return r.res, nil
}

// connectorArgs builds the activity list argv: --before is always an RFC3339
// instant, --since is omitted for an open-ended range.
func connectorArgs(rng rangespec.Range, backend string) []string {
	args := []string{"activity", "list"}
	if !rng.OpenStart {
		args = append(args, "--since", rng.Since.Format(time.RFC3339))
	}
	args = append(args, "--before", rng.Before.Format(time.RFC3339))
	if backend != "" {
		args = append(args, "--backend", backend)
	}
	return append(args, "--output", "json")
}

// invoke runs pg-connector, returning stdout and a failure reason ("" when a
// document may follow). stderr is used only when the runner can report it.
func (r *run) invoke(ctx context.Context, args []string) (stdout []byte, exit int, stderr []byte, err error) {
	if dr, ok := r.d.Conn.(pgconn.DetailedRunner); ok {
		out, err := dr.RunDetailed(ctx, args...)
		return out.Stdout, out.ExitCode, out.Stderr, err
	}
	stdout, exit, err = r.d.Conn.Run(ctx, args...)
	return stdout, exit, nil, err
}

func (r *run) runUnit(ctx context.Context, u unit, unitStart time.Time) error {
	stdout, exit, stderr, err := r.invoke(ctx, connectorArgs(r.o.Range, u.backend))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return r.failUnit(ctx, u, unitStart, fmt.Sprintf("%s could not be run: %v", pgconn.Binary, err))
	}
	doc, ok := decodeDoc(stdout)
	if !ok {
		reason := fmt.Sprintf("%s exited %d without a decodable sources document", pgconn.Binary, exit)
		if ex := excerpt(stderr); ex != "" {
			reason += ": " + ex
		}
		return r.failUnit(ctx, u, unitStart, reason)
	}
	return r.handleDoc(ctx, u, doc, unitStart)
}

// failUnit degrades every source the unit would have covered and persists the
// rows (only pull rows are written).
func (r *run) failUnit(ctx context.Context, u unit, unitStart time.Time, reason string) error {
	r.res.CouldNotRun = true
	for _, row := range failRows(r.d.Cfg, u, reason) {
		if err := r.emit(ctx, row, unitStart); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) handleDoc(ctx context.Context, u unit, doc wireDoc, unitStart time.Time) error {
	bySource := map[string][]wireItem{}
	for _, it := range doc.Items {
		bySource[it.Source] = append(bySource[it.Source], it)
	}
	seen := map[string]bool{}
	for _, wr := range doc.Sources {
		seen[wr.Source] = true
		row := r.ingestRow(ctx, wr, bySource[wr.Source])
		if err := r.emit(ctx, row, unitStart); err != nil {
			return err
		}
	}
	if u.backend != "" && !seen[u.backend] {
		return r.emit(ctx, OutcomeRow{
			Source: u.backend, Status: StatusDegraded,
			Reason: pgconn.Binary + " returned no row for this source",
		}, unitStart)
	}
	return nil
}

// ingestRow turns one sources[] row and its items into an outcome row,
// appending the valid items. Items of a disabled source are discarded.
func (r *run) ingestRow(ctx context.Context, wr wireRow, items []wireItem) OutcomeRow {
	switch {
	case !r.d.Cfg.SourceEnabled(wr.Source):
		return disabledRow(wr.Source)
	case wr.Status == StatusDisabled:
		reason := wr.Reason
		if reason == "" {
			reason = ReasonNotApplicable
		}
		return OutcomeRow{Source: wr.Source, Status: StatusDisabled, Reason: reason}
	}

	row := OutcomeRow{Source: wr.Source, Truncated: wr.Truncated}
	labels := r.d.Cfg.SourceLabels(wr.Source)
	for _, it := range items {
		if r.lockReason != "" {
			break
		}
		e, err := toEntry(wr.Source, labels, it.Item)
		if err != nil {
			row.Rejected++
			continue
		}
		res, err := r.d.Store.Append(ctx, e, r.o.Now)
		switch {
		case errors.Is(err, store.ErrLocked):
			r.lock(err)
		case err != nil:
			// an unexpected store failure on one entry rejects that entry only.
			row.Rejected++
		case res == store.Unchanged:
			row.Unchanged++
		default:
			row.Count++
		}
	}

	switch {
	case r.lockReason != "":
		row.Status, row.Reason = StatusDegraded, r.lockReason
	case wr.Status != StatusSucceeded:
		row.Status = StatusDegraded
		row.Reason = wr.Reason
		if row.Reason == "" {
			row.Reason = fmt.Sprintf("source reported status %q", wr.Status)
		}
	case wr.Truncated:
		row.Status, row.Reason = StatusDegraded, ReasonTruncated
	default:
		row.Status = StatusSucceeded
	}
	return row
}

func (r *run) lock(err error) {
	r.res.CouldNotRun = true
	r.lockReason = fmt.Sprintf("store locked: %v", err)
}

// emit persists the row (one pull row) and logs it (one line), then adds it to
// the result. Once the store has reported a lock every non-disabled row is
// degraded with the lock reason instead, and no further store write is tried
// (the store already waited out its busy timeout once).
func (r *run) emit(ctx context.Context, row OutcomeRow, unitStart time.Time) error {
	if r.lockReason != "" && row.Status != StatusDisabled {
		row.Status, row.Reason = StatusDegraded, r.lockReason
	}
	started := r.o.Now.Add(unitStart.Sub(r.wall))
	dur := time.Since(unitStart)
	if r.lockReason == "" {
		err := r.d.Store.RecordPull(ctx, store.PullRow{
			Source: row.Source, Since: r.sinceBound(), Before: r.o.Range.Before,
			StartedAt: started, EndedAt: started.Add(dur),
			Status: row.Status, Count: row.Count, Unchanged: row.Unchanged, Rejected: row.Rejected,
			Truncated: row.Truncated, Reason: row.Reason,
		})
		switch {
		case errors.Is(err, store.ErrLocked):
			r.lock(err)
			if row.Status != StatusDisabled {
				row.Status, row.Reason = StatusDegraded, r.lockReason
			}
		case err != nil:
			return fmt.Errorf("record pull row for %s: %w", row.Source, err)
		}
	}
	logRow(r.d, r.o, row, started, dur)
	r.res.Rows = append(r.res.Rows, row)
	return nil
}

func (r *run) sinceBound() time.Time {
	if r.o.Range.OpenStart {
		return time.Time{}
	}
	return r.o.Range.Since
}

// logRow writes the per-source telemetry line; a logging failure never fails
// the pull.
func logRow(d Deps, o Opts, row OutcomeRow, ts time.Time, dur time.Duration) {
	if d.Log == nil {
		return
	}
	since := ""
	if !o.Range.OpenStart {
		since = o.Range.Since.Format(time.RFC3339)
	}
	_ = d.Log.LogPull(telemetry.PullLog{
		TS: ts.UTC(), Source: row.Source, Since: since, Before: o.Range.Before.Format(time.RFC3339),
		Status: row.Status, Count: row.Count, Unchanged: row.Unchanged, Rejected: row.Rejected,
		Truncated: row.Truncated, DurationMS: dur.Milliseconds(), Reason: row.Reason,
	})
}

// excerpt returns a short single-line excerpt of stderr.
func excerpt(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	const max = 200
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}
