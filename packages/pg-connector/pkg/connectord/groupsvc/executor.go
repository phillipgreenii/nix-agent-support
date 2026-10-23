package groupsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// executor adapts one group's Fetcher to scheduler.Executor.
type executor struct {
	s *Service
	f Fetcher
}

func (e *executor) MaxBatch() int { return e.f.MaxBatch() }

func (e *executor) Spend(class governor.Class, n int) governor.Request {
	return e.f.Spend(class, n)
}

// job is one task of a batch that goes out.
type job struct {
	idx    int
	id     string
	target Target
	tok    groupkit.Token
	// prev is the stored row the fetch replaces; nil when it had no content.
	prev *store.GroupRow
}

// Execute runs one batch of the group: it drops the tasks that need no work,
// builds each target from the entity's stored primary content, fetches the rest
// in one Fetch call, and stores each outcome through the generation-checked
// Commit.
//
// Class scoping: the keep-alive skip and the already-fresh skip apply to classes
// 3 and above only; a class 1 or 2 task always fetches (a caller is waiting, or
// a write invalidated the group).
func (e *executor) Execute(ctx context.Context, items []scheduler.Item) []scheduler.Result {
	s, group, kind := e.s, e.f.Group(), e.s.cfg.Spec.EntityKind
	res := make([]scheduler.Result, len(items))
	members, err := s.members(ctx)
	if err != nil {
		return failAll(res, err)
	}
	now := s.now()

	var (
		jobs  []job
		class = governor.ClassFill
	)
	for i, it := range items {
		id := it.Task.Entity
		background := it.Task.Class >= governor.ClassQuery
		ent, ok, err := s.cfg.Store.GetEntity(ctx, kind, id)
		if err != nil {
			res[i].Err = err
			continue
		}
		if background {
			in := false
			if ok {
				if in, err = s.inKeepAlive(ctx, ent, members); err != nil {
					res[i].Err = err
					continue
				}
			}
			if !in {
				res[i].Skipped = true
				continue
			}
		}
		prim, _, havePrim, err := s.loadPrimary(ctx, id)
		if err != nil {
			res[i].Err = err
			continue
		}
		if !havePrim {
			// A group fetch needs the stored reference and stamps.
			res[i].Skipped = true
			continue
		}
		stamps := groupkit.Stamps{Primary: prim.Stamp, Secondary: prim.Stamp2}
		cur, curOK, err := s.cfg.Store.GetGroup(ctx, kind, id, group)
		if err != nil {
			res[i].Err = err
			continue
		}
		if background && it.Role == scheduler.RoleDue && curOK && s.cfg.Rules.Fresh(cur, stamps, now).IsFresh() {
			// Another path refreshed it since this task was queued: book the next cycle.
			res[i].Skipped = true
			_ = s.bookAfter(ctx, id, group, members)
			continue
		}
		if prim.Gone || prim.Ref == "" {
			res[i].Err = fmt.Errorf("groupsvc: %s: %w", id, errNoReference)
			continue
		}
		j := job{
			idx: i,
			id:  id,
			target: Target{
				ID: id, Ref: prim.Ref, Stamp: prim.Stamp, Stamp2: prim.Stamp2,
				Terminal: prim.Terminal, Attrs: prim.Attrs,
			},
			// The token carries the generation of the very row read as prev, so the
			// previous content and the generation Commit checks cannot disagree.
			tok: groupkit.Token{Kind: kind, ID: id, Group: group, Generation: cur.Generation},
		}
		if curOK && len(cur.Content) > 0 {
			c := cur
			j.prev = &c
		}
		jobs = append(jobs, j)
		if it.Role == scheduler.RoleDue {
			class = min(class, it.Task.Class)
		}
	}
	if len(jobs) == 0 {
		return res
	}
	if class == governor.ClassFill { // only fill riders left: admit as a keep-alive refresh
		class = governor.ClassKeepAlive
	}

	targets := make([]Target, len(jobs))
	for k, j := range jobs {
		targets[k] = j.target
	}
	info := RefreshInfo{Started: s.now(), Class: class, Purpose: purposeOf(class)}
	outs := e.f.Fetch(ctx, CallMeta{Class: class, Purpose: info.Purpose, Group: group, BatchSize: len(targets)}, targets)
	for k, j := range jobs {
		if k >= len(outs) {
			res[j.idx].Err = e.fail(ctx, j, fmt.Errorf("groupsvc: the %s fetcher gave no answer for %s", group, j.id))
			continue
		}
		res[j.idx].Err = e.apply(ctx, j, outs[k], info, members)
	}
	return res
}

func failAll(res []scheduler.Result, err error) []scheduler.Result {
	for i := range res {
		res[i].Err = err
	}
	return res
}

// apply books one outcome.
func (e *executor) apply(ctx context.Context, j job, o Outcome, info RefreshInfo, members map[string]bool) error {
	s, group := e.s, e.f.Group()
	if o.Err != nil {
		if errors.Is(o.Err, ErrGone) {
			return s.tombstone(ctx, j.id, info.Purpose)
		}
		return e.fail(ctx, j, o.Err)
	}
	if len(o.Content) == 0 {
		return e.fail(ctx, j, fmt.Errorf("groupsvc: the %s fetcher returned no content for %s", group, j.id))
	}
	fetchedAt := s.now()
	freshUntil, maxAgeAt := s.cfg.Rules.Schedule(group, j.target.Terminal, fetchedAt)
	if !o.FreshUntil.IsZero() {
		freshUntil = o.FreshUntil
	}
	stamp, stamp2 := o.Stamp, o.Stamp2
	if stamp == "" && stamp2 == "" {
		stamp, stamp2 = j.target.Stamp, j.target.Stamp2
	}
	fp := o.Fingerprint
	if fp == "" {
		fp = groupkit.Fingerprint(o.Content)
	}
	row := store.GroupRow{
		Content: o.Content, Fingerprint: fp, FetchedAt: fetchedAt, FreshUntil: freshUntil, MaxAgeAt: maxAgeAt,
		LastError: o.LastError,
	}
	row.SetStamps(stamp, stamp2)
	row.Kind, row.ID, row.Group, row.Generation = j.tok.Kind, j.tok.ID, j.tok.Group, j.tok.Generation+1
	var then func(tx *store.Tx) error
	if s.cfg.OnGroupStoring != nil {
		then = func(tx *store.Tx) error { return s.cfg.OnGroupStoring(ctx, tx, j.prev, row, info) }
	}
	stored, err := groupkit.CommitThen(ctx, s.cfg.Store, j.tok, row, then)
	if err != nil {
		return err
	}
	if stored {
		s.countCommit(j.id, group)
		if s.cfg.Identity != nil {
			s.cfg.Identity.Clear()
		}
		if s.cfg.OnGroupRefreshed != nil {
			s.cfg.OnGroupRefreshed(j.prev, row, info)
		}
	}
	// Stored or superseded, the cycle follows the row now in the store: a write
	// that superseded this fetch left it due now, so it is refetched at once.
	return s.bookAfter(ctx, j.id, group, members)
}

// fail books a failed fetch: the error is classified through the connector's
// ErrorPolicy (a rate-limit answer pauses the background classes and is not an
// authentication failure), recorded as the row's last_error (a governor refusal
// is the budget speaking, not the origin failing, and is not), and returned so
// the scheduler requeues the task.
func (e *executor) fail(ctx context.Context, j job, err error) error {
	s := e.s
	err = s.cfg.Errors.Classify(s.cfg.Governor, s.cfg.FallbackPause, err)
	var refusal *governor.Refusal
	if errors.As(err, &refusal) {
		return err
	}
	if s.cfg.Identity != nil && !s.cfg.Errors.IsRateLimit(err) && s.cfg.Errors.IsUnauthenticated(err) {
		s.cfg.Identity.MarkUnauthenticated(err)
	}
	s.recordError(ctx, j.id, e.f.Group(), err)
	return err
}

// recordError sets last_error on the stored group row, keeping its content. It is
// a compare-and-swap on the generation, so it can never overwrite a refresh that
// stored meanwhile; a missing row stays missing.
func (s *Service) recordError(ctx context.Context, id, group string, cause error) {
	row, ok, err := s.cfg.Store.GetGroup(ctx, s.cfg.Spec.EntityKind, id, group)
	if err != nil || !ok {
		return
	}
	row.LastError = cause.Error()
	_, _ = s.cfg.Store.PutGroupIf(ctx, row, row.Generation)
}

// tombstone marks the entity gone: the origin no longer shows it. The row is read
// and tombstoned_at is written in one transaction (so the write cannot rewind a
// version raised meanwhile), and OnGone runs in it, only when the entity was
// visible before; an OnGone error rolls the tombstone back. Its queued tasks are
// cancelled and its stored groups are left for expiry.
func (s *Service) tombstone(ctx context.Context, id string, purpose governor.Purpose) error {
	err := s.cfg.Store.InTx(ctx, func(tx *store.Tx) error {
		row, ok, err := tx.GetEntity(ctx, s.cfg.Spec.EntityKind, id)
		if err != nil || !ok {
			return err
		}
		visible := row.TombstonedAt.IsZero()
		row.TombstonedAt = s.now()
		if err := tx.PutEntity(ctx, row); err != nil {
			return err
		}
		if visible && s.cfg.OnGone != nil {
			return s.cfg.OnGone(ctx, tx, id, purpose)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.cfg.Scheduler.CancelEntity(id)
	return nil
}
