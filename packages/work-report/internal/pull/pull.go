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
	"regexp"
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

// DefaultRetryWait is how long a pull waits before it re-invokes the sources
// that degraded on a deadline. Host stalls under load last from tens of
// seconds to minutes, so an immediate retry would mostly hit the same stall;
// half a minute is long enough to let a burst of agent work drain and short
// enough that the hourly pull stays well inside its period (one retry, one
// wait, so the worst case adds this wait plus one backend deadline).
const DefaultRetryWait = 30 * time.Second

// Deps are a pull's collaborators. Log may be nil. RetryWait (zero means
// DefaultRetryWait) and Sleep (nil means a real, context-aware timer) are the
// seam through which tests wait without sleeping.
type Deps struct {
	Cfg       config.Config
	Store     *store.Store
	Conn      pgconn.Runner
	Log       *telemetry.Logger
	RetryWait time.Duration
	Sleep     func(ctx context.Context, d time.Duration) error
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
		logRow(d, o, row, o.Now, 0, false)
	}
	return res
}

func disabledRow(source string) OutcomeRow {
	return OutcomeRow{Source: source, Status: StatusDisabled, Reason: ReasonDisabledByConfig}
}

// pending is one outcome row waiting to be persisted: it carries the timing of
// the attempt that produced it, so holding it back (see Run) never skews the
// logged duration. retry marks a degraded row whose failure was a deadline; it
// is the only kind of row Run holds back for a second attempt. retried is set
// on the row that replaced such a row after the retry.
type pending struct {
	row       OutcomeRow
	unitStart time.Time
	dur       time.Duration
	retry     bool
	retried   bool
}

func newPending(row OutcomeRow, unitStart time.Time) pending {
	return pending{row: row, unitStart: unitStart, dur: time.Since(unitStart)}
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
//
// Every unit is attempted first and its rows are held, in order, before any is
// persisted. A source that degraded on a deadline is then re-invoked once, after
// a wait, and its row is replaced by the retry's, so a source that recovers
// never produces a degraded row (and no alert); a source that fails the retry
// too ends degraded as before. Only deadline-kind failures are retried.
func Run(ctx context.Context, d Deps, o Opts) (Result, error) {
	r := &run{d: d, o: o, wall: time.Now()}
	var held []pending
	for _, u := range plan(d.Cfg, o.Sources) {
		if err := ctx.Err(); err != nil {
			return r.res, r.flush(ctx, held, err)
		}
		unitStart := time.Now()
		if u.disabled {
			held = append(held, newPending(disabledRow(u.backend), unitStart))
			continue
		}
		ps, err := r.runUnit(ctx, u, unitStart)
		held = append(held, ps...)
		if err != nil {
			return r.res, r.flush(ctx, held, err)
		}
	}
	held, err := r.retryDeadlines(ctx, held)
	return r.res, r.flush(ctx, held, err)
}

// flush persists and logs every held row in order. The error it returns is
// cause when set (a flush failure must not mask the reason the pull stopped),
// otherwise the first flush error.
func (r *run) flush(ctx context.Context, held []pending, cause error) error {
	for _, p := range held {
		if err := r.emit(ctx, p); err != nil {
			if cause != nil {
				return cause
			}
			return err
		}
	}
	return cause
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

func (r *run) runUnit(ctx context.Context, u unit, unitStart time.Time) ([]pending, error) {
	stdout, exit, stderr, err := r.invoke(ctx, connectorArgs(r.o.Range, u.backend))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return r.failUnit(u, unitStart, fmt.Sprintf("%s could not be run: %v", pgconn.Binary, err)), nil
	}
	doc, ok := decodeDoc(stdout)
	if !ok {
		reason := fmt.Sprintf("%s exited %d without a decodable sources document", pgconn.Binary, exit)
		if ex := excerpt(stderr); ex != "" {
			reason += ": " + ex
		}
		return r.failUnit(u, unitStart, reason), nil
	}
	return r.handleDoc(ctx, u, doc, unitStart), nil
}

// failUnit degrades every source the unit would have covered (only pull rows
// are written).
func (r *run) failUnit(u unit, unitStart time.Time, reason string) []pending {
	r.res.CouldNotRun = true
	var out []pending
	for _, row := range failRows(r.d.Cfg, u, reason) {
		out = append(out, newPending(row, unitStart))
	}
	return out
}

func (r *run) handleDoc(ctx context.Context, u unit, doc wireDoc, unitStart time.Time) []pending {
	bySource := map[string][]wireItem{}
	for _, it := range doc.Items {
		bySource[it.Source] = append(bySource[it.Source], it)
	}
	seen := map[string]bool{}
	var out []pending
	for _, wr := range doc.Sources {
		seen[wr.Source] = true
		row := r.ingestRow(ctx, wr, bySource[wr.Source])
		p := newPending(row, unitStart)
		p.retry = retryable(wr, row)
		out = append(out, p)
	}
	if u.backend != "" && !seen[u.backend] {
		out = append(out, newPending(OutcomeRow{
			Source: u.backend, Status: StatusDegraded,
			Reason: pgconn.Binary + " returned no row for this source",
		}, unitStart))
	}
	return out
}

// retryable reports whether row, built from the decoded sources[] row wr, is
// a deadline-kind degradation worth one more attempt: the source itself
// reported degraded (not a store lock, not a truncation) with a deadline
// reason.
func retryable(wr wireRow, row OutcomeRow) bool {
	return row.Status == StatusDegraded && wr.Status == StatusDegraded && isDeadlineReason(row.Reason)
}

// The source rows carry no structured failure code (pg-connector's sources[]
// row is just source, status, count and a free-text reason), so a deadline is
// recognized from the two texts pg-connector writes when a backend's time
// budget runs out:
//
//   - the backend's own structured error: "deadline exceeded after <elapsed>
//     (limit <limit>) in op=<op> ..." (pkg/scriptout deadlineError and the
//     router's equivalent);
//   - the umbrella's backstop kill of a backend that could not answer:
//     "(umbrella deadline <limit> exceeded; backend killed)" (pkg/scriptout
//     callDetail).
//
// Both are matched in full shape, not by a bare "deadline", so "exit status 1",
// a source's own "upstream timed out", or the pull's own "context deadline
// exceeded" are never retried. If pg-connector rewords either text the retry
// silently stops (every deadline degrades as before); the tests in
// pull_test.go pin today's wording.
var deadlineReason = regexp.MustCompile(
	`deadline exceeded after \S+ \(limit \S+\) in op=\S+|umbrella deadline \S+ exceeded; backend killed`,
)

func isDeadlineReason(reason string) bool { return deadlineReason.MatchString(reason) }

// retryDeadlines waits once, then re-invokes each held deadline-degraded
// source once, pinned to that source, replacing its held row with the retry's
// row. A retry that yields no row for the source (cannot run, no decodable
// document, no row for it) leaves the original row in place. Healthy and
// non-deadline rows are never re-run.
func (r *run) retryDeadlines(ctx context.Context, held []pending) ([]pending, error) {
	var sources []string
	for _, p := range held {
		if p.retry {
			sources = append(sources, p.row.Source)
		}
	}
	if len(sources) == 0 {
		return held, nil
	}
	if err := r.wait(ctx); err != nil {
		return held, err
	}
	for _, u := range plan(r.d.Cfg, sources) {
		if u.disabled {
			continue
		}
		np, ok, err := r.retryUnit(ctx, u)
		if err != nil {
			return held, err
		}
		if !ok {
			continue
		}
		for i := range held {
			if held[i].retry && held[i].row.Source == u.backend {
				held[i] = np
				break
			}
		}
	}
	return held, nil
}

// wait sleeps RetryWait (DefaultRetryWait when zero) through the injectable
// Sleep, or a real timer that ends early with ctx.
func (r *run) wait(ctx context.Context) error {
	d := r.d.RetryWait
	if d <= 0 {
		d = DefaultRetryWait
	}
	if r.d.Sleep != nil {
		return r.d.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryUnit runs one pinned retry. ok is false when it produced no usable row
// for the source; it never touches Result.CouldNotRun, because the first
// attempt already ran.
func (r *run) retryUnit(ctx context.Context, u unit) (p pending, ok bool, err error) {
	unitStart := time.Now()
	stdout, _, _, ierr := r.invoke(ctx, connectorArgs(r.o.Range, u.backend))
	if ierr != nil {
		return pending{}, false, ctx.Err()
	}
	doc, decoded := decodeDoc(stdout)
	if !decoded {
		return pending{}, false, nil
	}
	var items []wireItem
	for _, it := range doc.Items {
		if it.Source == u.backend {
			items = append(items, it)
		}
	}
	for _, wr := range doc.Sources {
		if wr.Source != u.backend {
			continue
		}
		p = newPending(r.ingestRow(ctx, wr, items), unitStart)
		p.retried = true
		return p, true, nil
	}
	return pending{}, false, nil
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
func (r *run) emit(ctx context.Context, p pending) error {
	row := p.row
	if r.lockReason != "" && row.Status != StatusDisabled {
		row.Status, row.Reason = StatusDegraded, r.lockReason
	}
	started := r.o.Now.Add(p.unitStart.Sub(r.wall))
	if r.lockReason == "" {
		err := r.d.Store.RecordPull(ctx, store.PullRow{
			Source: row.Source, Since: r.sinceBound(), Before: r.o.Range.Before,
			StartedAt: started, EndedAt: started.Add(p.dur),
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
	logRow(r.d, r.o, row, started, p.dur, p.retried)
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
func logRow(d Deps, o Opts, row OutcomeRow, ts time.Time, dur time.Duration, retried bool) {
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
		Truncated: row.Truncated, DurationMS: dur.Milliseconds(), Reason: row.Reason, Retried: retried,
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
