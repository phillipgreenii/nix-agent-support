package focus

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The pull of `focus pull` (spec section 7.3): strictly additive and outside
// the draft/lock cycle. It resolves WHICH not-yet-selected candidates to add
// (ResolvePull, a pure function over the current rank and the live facts) and
// adds them in ONE store transaction (Pull), taking rank_position by the lock
// sequence rule (the maximum plus one, in rank order) with the tier of the
// run. It removes nothing, re-caps nothing, re-sorts nothing and leaves the
// stored draft alone. The focus_selected annotation of every row it adds is
// written afterwards through the same helper `select` uses (ApplyAnnotations).
//
// pg-desk MUST NOT contain the focus-item work kind or dedup-key logic and
// executes no tracker write verb: nothing is minted here.
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing. The durable record is the focus_run row (written in the pull's
// transaction, finalized by the verb) and the annotation's change_log record.

// PullNoPlanRemedy is the remedy of a pull into a period that has no rows
// while another period holds some.
const PullNoPlanRemedy = "lock today's plan first: run show, then select --apply"

// PullVerb is the focus_run.verb of a pull.
const PullVerb = "pull"

// PullRequest is what the operator asked to pull: the top Top candidates not
// yet selected (Top > 0), or the named Keys, never both.
type PullRequest struct {
	Top  int
	Keys []Key
}

// PullPlan is the resolution of a PullRequest. Select lists the rows to add in
// rank order (a key the rank does not place, covered by a group or an epic,
// follows the ranked ones in request order); Skipped is every routine skip
// with its reason (SkipAlreadySelected, SkipNoLongerCandidate or
// SkipNotACandidate); AllMissing is true when keys were named and none of
// them has an entity row in the store, the one total failure of a request.
type PullPlan struct {
	Select     []Insertion
	Skipped    []Skip
	AllMissing bool
}

// ResolvePull decides which rows a pull adds. rk is the CURRENT rank; set the
// candidate set it ranked; facts the live facts of every key rk and the
// request name; plan the period's rows now; entities the store's entity rows
// (a named key must have one: pull does not hydrate).
//
// A named key is skipped, never an error, when it is already in the plan
// (already-selected), when it is finished, hidden or inactive (a merged or
// closed item: no-longer-candidate), or when it is no candidate at all, which
// includes a stored entity that was never one (not-a-candidate). --top reads
// the rank in order and takes the first Top rows that are not in the plan and
// not finished, so it never selects an item it would have to skip.
func ResolvePull(rk Ranking, set CandidateSet, facts LiveFacts, plan []PlanRow, entities map[Key]store.Entity, req PullRequest) PullPlan {
	var out PullPlan
	inPlan := make(map[Key]bool, len(plan))
	for _, p := range plan {
		inPlan[p.Key] = true
	}
	cand := make(map[Key]bool, len(set.Candidates))
	for _, c := range set.Candidates {
		cand[c.Key] = true
	}
	ranked := make(map[Key]RankedRow, len(rk.Rows))
	for _, r := range rk.Rows {
		ranked[r.Key] = r
	}
	lost := func(k Key) bool {
		f := facts[k]
		return f.Finished || f.Hidden || f.Inactive
	}
	tierOf := func(k Key) *string {
		if r, ok := ranked[k]; ok {
			t := r.Tier
			return &t
		}
		return nil
	}

	if req.Top > 0 {
		for _, r := range rk.Rows {
			if len(out.Select) >= req.Top {
				break
			}
			if inPlan[r.Key] || r.Finished || lost(r.Key) {
				continue
			}
			t := r.Tier
			out.Select = append(out.Select, Insertion{Key: r.Key, Source: SourcePull, Tier: &t})
		}
		return out
	}

	seen := map[Key]bool{}
	missing := 0
	named := 0
	for _, k := range req.Keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		named++
		switch {
		case !hasEntity(entities, k):
			missing++
			out.Skipped = append(out.Skipped, Skip{Key: k, Reason: SkipNotACandidate})
		case inPlan[k]:
			out.Skipped = append(out.Skipped, Skip{Key: k, Reason: SkipAlreadySelected})
		case lost(k):
			out.Skipped = append(out.Skipped, Skip{Key: k, Reason: SkipNoLongerCandidate})
		case !cand[k]:
			out.Skipped = append(out.Skipped, Skip{Key: k, Reason: SkipNotACandidate})
		default:
			if r, ok := ranked[k]; ok && r.Finished {
				out.Skipped = append(out.Skipped, Skip{Key: k, Reason: SkipNoLongerCandidate})
				continue
			}
			out.Select = append(out.Select, Insertion{Key: k, Source: SourcePull, Tier: tierOf(k)})
		}
	}
	out.AllMissing = named > 0 && missing == named
	// Rank order: ranked keys by position, the rest after them in request
	// order (a stable sort keeps that order).
	pos := func(k Key) int {
		if r, ok := ranked[k]; ok {
			return r.Position
		}
		return int(^uint(0) >> 1)
	}
	sort.SliceStable(out.Select, func(i, j int) bool { return pos(out.Select[i].Key) < pos(out.Select[j].Key) })
	return out
}

func hasEntity(entities map[Key]store.Entity, k Key) bool {
	_, ok := entities[k]
	return ok
}

// checkPull is the period rule of a pull, shared by the preflight (outside a
// transaction) and the transaction itself: a pull into a future period is a
// usage error, and so is a pull into a period that has no rows while ANOTHER
// period holds rows (a pull never replaces a plan, so two live plans can never
// exist). maxOther names the other period for the message.
func checkPull(key, today string, hasRows, otherHolds bool, maxOther string) error {
	if key > today {
		return &UsageError{
			Msg:    fmt.Sprintf("period %s is in the future", key),
			Remedy: "pull into today's plan, or plan it on the day",
		}
	}
	if !hasRows && otherHolds {
		return &UsageError{
			Msg:    fmt.Sprintf("period %s has no plan while period %s holds one; a pull never replaces a plan", key, maxOther),
			Remedy: PullNoPlanRemedy,
		}
	}
	return nil
}

// PullPreflight runs the period checks of a pull that need no transaction:
// the period is open (ErrPeriodClosed), is not in the future and, when it has
// no rows, no other period holds any. A dry run and a preview call it so that
// they report what the pull itself would; Pull repeats the same checks inside
// its transaction.
func PullPreflight(d LockDeps, p Period) error {
	period, found, err := d.Store.FocusPeriodGet(p.Type, p.Key)
	if err != nil {
		return fmt.Errorf("focus: read period: %w", err)
	}
	if found && period.ClosedAt != "" {
		return ErrPeriodClosed
	}
	hasRows := false
	if found {
		rows, err := d.Store.FocusPlanRows(period.ID)
		if err != nil {
			return fmt.Errorf("focus: read plan rows: %w", err)
		}
		hasRows = len(rows) > 0
	}
	all, err := d.Store.FocusRowsAllPeriods()
	if err != nil {
		return fmt.Errorf("focus: read plan rows: %w", err)
	}
	periods, err := d.Store.FocusPeriods()
	if err != nil {
		return fmt.Errorf("focus: read periods: %w", err)
	}
	keyOf := map[int64]string{}
	for _, op := range periods {
		keyOf[op.ID] = op.PeriodKey
	}
	otherHolds, maxOther := false, ""
	for _, r := range all {
		if found && r.FocusPeriodID == period.ID {
			continue
		}
		otherHolds = true
		if k := keyOf[r.FocusPeriodID]; k > maxOther {
			maxOther = k
		}
	}
	return checkPull(p.Key, d.todayKey(), hasRows, otherHolds, maxOther)
}

// PullInput is one pull's write. Select is the rows to add in rank order
// (ResolvePull's result); RunID, when not empty, is the focus_run row the
// transaction inserts (exit_code NULL until the caller finalizes it).
type PullInput struct {
	Period Period
	Actor  string
	Select []Insertion
	RunID  string
}

// PullResult is what a pull did. Inserted are the rows it added; Raced the
// selected keys that a concurrent run added first (reported as
// already-selected, never an error).
type PullResult struct {
	Inserted    []Insertion
	Raced       []Key
	Annotations []AnnotationOutcome
	Outcome     string
	ExitCode    int
	RunID       string
}

// Pull adds in.Select to the period's plan in ONE transaction together with
// the focus_period row (get or create) and the focus_run row, then writes the
// focus_selected annotation of every row it added (ApplyAnnotations, the
// helper select uses). Inside the transaction it re-reads the period: closed
// is ErrPeriodClosed, a future period or a period with no rows while another
// holds rows is a *UsageError, each with nothing written. A key already in the
// plan by then (a concurrent pull) is left out and reported in Raced. Any
// other error is exit 3 (the transaction rolled back); a nil error with
// ExitCode 2 is partial: the rows committed but an annotation write failed.
func Pull(ctx context.Context, d LockDeps, in PullInput) (PullResult, error) {
	res := PullResult{RunID: in.RunID}
	repo := d.repo()
	at := d.now().Format(time.RFC3339)
	today := d.todayKey()
	err := d.Store.FocusLockTx(func(tx *store.FocusTx) error {
		res.Inserted, res.Raced = nil, nil
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
		if err := checkPull(p.PeriodKey, today, len(rows) > 0, otherHolds, maxOther); err != nil {
			return err
		}
		have := make(map[Key]bool, len(rows))
		for _, r := range rows {
			have[Key{Type: r.EntityType, ID: r.EntityID}] = true
		}
		next, err := NextRankPosition(tx, p.ID)
		if err != nil {
			return err
		}
		for _, ins := range in.Select {
			if have[ins.Key] {
				res.Raced = append(res.Raced, ins.Key)
				continue
			}
			if _, err := tx.InsertSelection(store.FocusSelection{
				FocusPeriodID: p.ID, Repo: repo, EntityType: ins.Key.Type, EntityID: ins.Key.ID,
				SelectedAt: at, RankPosition: next, Tier: ins.Tier,
			}); err != nil {
				return err
			}
			next++
			have[ins.Key] = true
			res.Inserted = append(res.Inserted, ins)
		}
		if in.RunID != "" {
			pid := p.ID
			if _, err := tx.InsertRun(store.FocusRun{
				RunID: in.RunID, Verb: PullVerb, FocusPeriodID: &pid,
				StartedAt: at, Actor: in.Actor, CountsJSON: "{}",
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		res.Inserted, res.Raced = nil, nil
		switch {
		case errors.Is(err, ErrPeriodClosed), errors.Is(err, ErrUsage):
			res.Outcome, res.ExitCode = outcomeOfErr(err)
			return res, err
		}
		res.Outcome, res.ExitCode = OutcomeTotal, ExitTotal
		return res, fmt.Errorf("focus: pull: %w", err)
	}

	hookAfterCommit()
	res.Outcome, res.ExitCode = OutcomeOK, ExitOK
	keys := make([]Key, 0, len(res.Inserted))
	for _, ins := range res.Inserted {
		keys = append(keys, ins.Key)
	}
	res.Annotations = ApplyAnnotations(ctx, d, keys, at)
	for _, a := range res.Annotations {
		if a.Outcome != AnnotationOK {
			res.Outcome, res.ExitCode = OutcomePartial, ExitPartial
			break
		}
	}
	if res.ExitCode == ExitOK && len(res.Inserted) == 0 {
		res.Outcome = OutcomeEmpty
	}
	return res, nil
}
