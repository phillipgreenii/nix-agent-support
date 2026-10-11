package focus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The lock of `focus select --apply` (spec section 7.2 step 5): reconcile
// focus_selection for one period to the final set, in ONE store transaction
// together with the stored-draft deletion, the focus_period row (get or
// create, persisted cap) and the focus_run row; THEN write the focus_selected
// annotation of every affected entity under pg-desk's per-entity lock (value
// computed from the committed table); the caller then completes the run
// record's per-entity {outcome, seq} and finalizes exit_code LAST. A crash
// between the steps leaves rows without annotations, which Repair mends.
//
// pg-desk MUST NOT contain the focus-item work kind or dedup-key logic and
// executes no tracker write verb: nothing is minted here; the annotation is
// what the decider reacts to.
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing. The durable record is the focus_run row (written in the lock
// transaction, finalized by the verb) and the annotation's change_log record.

// Exit codes Lock reports (the focus scheme of the spec's section 7).
const (
	ExitOK           = 0
	ExitUsage        = 1
	ExitPartial      = 2
	ExitTotal        = 3
	ExitPeriodClosed = 6
)

// Outcome words of LockResult.Outcome (the vocabulary of the run statistics).
const (
	OutcomePreview      = "preview"
	OutcomeOK           = "ok"
	OutcomeEmpty        = "empty"
	OutcomePartial      = "partial"
	OutcomeTotal        = "total"
	OutcomeUsage        = "usage"
	OutcomePeriodClosed = "period_closed"
)

// Annotation outcomes (counts_json.annotations[].outcome).
const (
	AnnotationOK     = "ok"
	AnnotationFailed = "failed"
	// AnnotationWouldWrite is the outcome a dry-run repair reports.
	AnnotationWouldWrite = "would-write"
)

// annotationOrigin is the origin of the change_log record an annotation
// write appends.
const annotationOrigin = "pg-desk"

var (
	// ErrPeriodClosed reports a lock or repair-free write on a closed period
	// (exit 6).
	ErrPeriodClosed = errors.New("focus: period is closed")
	// ErrPlanChanged reports that the plan or the stored draft changed
	// between the caller's read and the lock transaction (a concurrent lock,
	// pull or replan). Nothing was written; the caller re-reads and
	// recomputes, which is how a second concurrent lock computes fresh.
	ErrPlanChanged = errors.New("focus: plan or draft changed since it was read")
)

// Period addresses one focus period.
type Period struct{ Type, Key string }

// LockDeps are the seams of the lock. Hydrate hydrates one key new to the
// store (a read of the tracker that writes the store's own entity row);
// Stderr receives the one-line warnings; Actor is the actor recorded on the
// annotations.
type LockDeps struct {
	Store     *store.Store
	Annotator Annotator
	Config    *config.Config
	Now       func() time.Time
	Hydrate   func(ctx context.Context, k Key) error
	Stderr    io.Writer
	Actor     string
}

func (d LockDeps) repo() string {
	if d.Config != nil && len(d.Config.Repos) > 0 {
		return d.Config.Repos[0].Remote
	}
	return ""
}

func (d LockDeps) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d LockDeps) todayKey() string {
	loc := time.Local
	if d.Config != nil {
		loc = d.Config.FocusTimeZone()
	}
	return d.now().In(loc).Format("2006-01-02")
}

// Seen is what the caller computed the final set against. The lock re-reads
// both inside its transaction and returns ErrPlanChanged when they differ.
type Seen struct {
	// Plan is the period's plan rows as read.
	Plan []PlanRow
	// Draft is the stored draft the final set was computed from, nil when
	// the draft was computed in process.
	Draft *store.FocusDraft
}

// LockInput is everything one lock needs.
//
//   - Final is the result of ComputeFinalSet; Draft is the draft it was
//     computed from (nil: computed in process, FreshOrder is then set).
//   - RunID, when not empty, is the focus_run row the transaction inserts
//     (exit_code NULL until the caller finalizes it).
//   - ReplyCap is the reply's cap=N, if any.
type LockInput struct {
	Period     Period
	Actor      string
	Apply      bool
	DryRun     bool
	Final      FinalSet
	Draft      *DraftDoc
	FreshOrder bool
	Merges     []Merge
	ReplyCap   *int
	RunID      string
	Seen       Seen
}

// AnnotationOutcome is one entity's annotation result.
type AnnotationOutcome struct {
	Key     Key
	Outcome string // ok|failed
	Seq     int64
}

// LockResult is what a lock did (or, for a preview, would do).
type LockResult struct {
	Inserted    []Insertion
	Deleted     []Deletion
	Annotations []AnnotationOutcome
	// Merged and Unmerged are the merge links recorded and removed.
	Merged, Unmerged []Merge
	Outcome          string
	ExitCode         int
	RunID            string
	Preview          PreviewResult
}

// hookAfterCommit is called between the table commit and the annotation
// writes; tests use it to observe the committed-but-unannotated state.
var hookAfterCommit = func() {}

// NextRankPosition is the rank_position a row added now takes: the maximum
// over the period's rows plus one (1 for an empty period).
func NextRankPosition(tx *store.FocusTx, periodID int64) (int, error) {
	m, err := tx.MaxRankPosition(periodID)
	if err != nil {
		return 0, err
	}
	return m + 1, nil
}

// decideFirstLock applies the first-lock rule of RV-B. firstLock is whether
// the period holds no row; nonEmpty whether the final set inserts anything;
// otherHolds and maxOther describe the other periods that hold rows. replace
// is true when the lock deletes every row of every other period. A non-empty
// first lock for a future period, or for a period not later than the current
// plan, is a usage error.
func decideFirstLock(firstLock, nonEmpty bool, key, today string, otherHolds bool, maxOther string) (replace bool, err error) {
	if !firstLock || !nonEmpty {
		return false, nil
	}
	if key > today {
		return false, &UsageError{
			Msg:    fmt.Sprintf("period %s is in the future", key),
			Remedy: "plan it on the day",
		}
	}
	if !otherHolds {
		return false, nil
	}
	if key <= maxOther {
		return false, &UsageError{
			Msg:    fmt.Sprintf("period %s is earlier than the current plan (%s); history is not rewritten", key, maxOther),
			Remedy: "address the current plan's period, or a later one",
		}
	}
	return true, nil
}

// Lock is `select --apply` over final set in.Final (see the file comment). A
// preview (no Apply, or DryRun) computes everything and writes nothing.
//
// Errors carry the exit code through LockResult.ExitCode: a *UsageError is 1
// (a first lock for an earlier or a future period; no selection state
// changed), ErrPeriodClosed is 6, ErrPlanChanged is returned with nothing
// written and ExitCode 0 for the caller to retry, any other error is 3 (the
// transaction rolled back). A nil error with ExitCode 2 is partial: the rows
// committed but some annotation writes failed.
func Lock(ctx context.Context, d LockDeps, in LockInput) (LockResult, error) {
	res := LockResult{RunID: in.RunID}
	if !in.Apply || in.DryRun {
		pv, err := BuildPreview(ctx, d, in, in.Final)
		res.Preview, res.Outcome = pv, OutcomePreview
		if err != nil {
			res.Outcome, res.ExitCode = outcomeOfErr(err)
		}
		return res, err
	}
	repo := d.repo()
	at := d.now().Format(time.RFC3339)
	today := d.todayKey()
	fs := in.Final

	var affected []Key
	seenKey := map[Key]bool{}
	touch := func(k Key) {
		if !seenKey[k] {
			seenKey[k] = true
			affected = append(affected, k)
		}
	}
	err := d.Store.FocusLockTx(func(tx *store.FocusTx) error {
		res.Inserted, res.Deleted, affected, seenKey = nil, nil, nil, map[Key]bool{}
		p, err := tx.GetOrCreatePeriod(in.Period.Type, in.Period.Key)
		if err != nil {
			return err
		}
		if p.ClosedAt != "" {
			return ErrPeriodClosed
		}
		rows, err := tx.PlanRows(p.ID)
		if err != nil {
			return err
		}
		if !samePlan(rows, in.Seen.Plan) {
			return ErrPlanChanged
		}
		if in.Seen.Draft != nil {
			cur, found, err := tx.ReadDraft()
			if err != nil {
				return err
			}
			if !found || cur.MadeAt != in.Seen.Draft.MadeAt || cur.BodyJSON != in.Seen.Draft.BodyJSON {
				return ErrPlanChanged
			}
		}
		first := len(rows) == 0
		otherHolds, err := tx.OtherPeriodsHoldRows(p.ID)
		if err != nil {
			return err
		}
		maxOther := ""
		if otherHolds {
			if maxOther, err = tx.MaxOtherPeriodKey(p.ID); err != nil {
				return err
			}
		}
		replace, err := decideFirstLock(first, len(fs.Insert) > 0, p.PeriodKey, today, otherHolds, maxOther)
		if err != nil {
			return err
		}
		next, err := NextRankPosition(tx, p.ID)
		if err != nil {
			return err
		}

		// Strikes and removals first, so an item struck by -key is counted
		// as operator even when an older period also holds a row of it.
		for _, del := range fs.Delete {
			touch(del.Key)
			if del.Cause == CauseOperator {
				erows, err := tx.RowsOfEntity(repo, del.Key.Type, del.Key.ID)
				if err != nil {
					return err
				}
				if _, err := tx.DeleteEntityRows(repo, del.Key.Type, del.Key.ID); err != nil {
					return err
				}
				for range erows {
					res.Deleted = append(res.Deleted, del)
				}
				continue
			}
			ok, err := tx.DeleteSelection(p.ID, repo, del.Key.Type, del.Key.ID)
			if err != nil {
				return err
			}
			if ok {
				res.Deleted = append(res.Deleted, del)
			}
		}
		if replace {
			old, err := tx.RowsOfOtherPeriods(p.ID)
			if err != nil {
				return err
			}
			if _, err := tx.DeleteRowsOfOtherPeriods(p.ID); err != nil {
				return err
			}
			for _, r := range old {
				k := Key{Type: r.EntityType, ID: r.EntityID}
				touch(k)
				res.Deleted = append(res.Deleted, Deletion{Key: k, Cause: CauseNewPeriod})
			}
		}
		for i, ins := range fs.Insert {
			touch(ins.Key)
			if _, err := tx.InsertSelection(store.FocusSelection{
				FocusPeriodID: p.ID, Repo: repo, EntityType: ins.Key.Type, EntityID: ins.Key.ID,
				SelectedAt: at, RankPosition: next + i, Tier: ins.Tier,
			}); err != nil {
				return err
			}
			res.Inserted = append(res.Inserted, ins)
		}
		if fs.PersistCap != nil {
			if err := tx.SetPeriodCap(p.ID, *fs.PersistCap); err != nil {
				return err
			}
		}
		if _, err := tx.DeleteDraftAtOrBelow(in.Period.Type, in.Period.Key); err != nil {
			return err
		}
		if in.RunID != "" {
			pid := p.ID
			if _, err := tx.InsertRun(store.FocusRun{
				RunID: in.RunID, Verb: "select", FocusPeriodID: &pid,
				StartedAt: at, Actor: in.Actor, CountsJSON: "{}",
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		res.Inserted, res.Deleted = nil, nil
		switch {
		case errors.Is(err, ErrPlanChanged):
			return res, err
		case errors.Is(err, ErrPeriodClosed), errors.Is(err, ErrUsage):
			res.Outcome, res.ExitCode = outcomeOfErr(err)
			return res, err
		}
		res.Outcome, res.ExitCode = OutcomeTotal, ExitTotal
		return res, fmt.Errorf("focus: lock: %w", err)
	}

	hookAfterCommit()
	res.Outcome, res.ExitCode = OutcomeOK, ExitOK
	// The merge links are external references: they are not selection state,
	// so they are written after the commit; a failure is partial.
	mergeActor := in.Actor
	if mergeActor == "" {
		mergeActor = d.Actor
	}
	var mergeErr error
	if len(fs.Absorbed) > 0 {
		if mergeErr = RecordMerges(d.Store, repo, fs.Absorbed, mergeActor, at); mergeErr == nil {
			res.Merged = fs.Absorbed
		}
	}
	if mergeErr == nil && len(fs.Unmerge) > 0 {
		if mergeErr = RemoveMerges(d.Store, repo, fs.Unmerge); mergeErr == nil {
			res.Unmerged = fs.Unmerge
		}
	}
	if mergeErr != nil {
		if d.Stderr != nil {
			_, _ = fmt.Fprintf(d.Stderr, "WARNING: %v\n", mergeErr)
		}
		res.Outcome, res.ExitCode = OutcomePartial, ExitPartial
	}
	res.Annotations = ApplyAnnotations(ctx, d, affected, at)
	for _, a := range res.Annotations {
		if a.Outcome != AnnotationOK {
			res.Outcome, res.ExitCode = OutcomePartial, ExitPartial
			break
		}
	}
	if res.ExitCode == ExitOK && len(res.Inserted) == 0 && len(res.Deleted) == 0 {
		res.Outcome = OutcomeEmpty
	}
	return res, nil
}

// outcomeOfErr maps a lock or preview error to its outcome word and code.
func outcomeOfErr(err error) (string, int) {
	switch {
	case errors.Is(err, ErrPeriodClosed):
		return OutcomePeriodClosed, ExitPeriodClosed
	case errors.Is(err, ErrUsage):
		return OutcomeUsage, ExitUsage
	}
	return OutcomeTotal, ExitTotal
}

// samePlan reports whether the rows read inside the transaction are the rows
// the caller computed against: the same keys.
func samePlan(rows []store.FocusSelection, seen []PlanRow) bool {
	if len(rows) != len(seen) {
		return false
	}
	want := make(map[Key]bool, len(seen))
	for _, p := range seen {
		want[p.Key] = true
	}
	for _, r := range rows {
		if !want[Key{Type: r.EntityType, ID: r.EntityID}] {
			return false
		}
	}
	return true
}

// ApplyAnnotations writes the focus_selected annotation of every key, one
// entity at a time through d.Annotator (which serializes per entity). The
// value is FocusAnnotationValue computed from the committed table; after the
// write it is read again and rewritten if a concurrent lock committed in
// between, so the annotation converges on the table. A write that fails is
// reported as failed with seq 0 and does not stop the others.
func ApplyAnnotations(ctx context.Context, d LockDeps, keys []Key, at string) []AnnotationOutcome {
	repo := d.repo()
	out := make([]AnnotationOutcome, 0, len(keys))
	for _, k := range keys {
		o := AnnotationOutcome{Key: k, Outcome: AnnotationFailed}
		var seq int64
		for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
			v, err := d.Store.FocusAnnotationValue(repo, k.Type, k.ID)
			if err != nil {
				break
			}
			s, changed, err := d.Annotator.SetFocusSelected(repo, k.Type, k.ID, v, annotationOrigin, d.Actor, at)
			if err != nil {
				break
			}
			if changed {
				seq = s
			}
			if again, err := d.Store.FocusAnnotationValue(repo, k.Type, k.ID); err == nil && again == v {
				o.Outcome, o.Seq = AnnotationOK, seq
				break
			}
		}
		if o.Outcome != AnnotationOK {
			o.Seq = 0
		}
		out = append(out, o)
	}
	return out
}
