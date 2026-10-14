package groupsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// executor adapts one group's Fetcher to scheduler.Executor.
type executor struct {
	s *Service
	f groups.Fetcher
}

func (e *executor) MaxBatch() int { return e.f.MaxBatch() }

func (e *executor) Spend(class governor.Class, n int) governor.Request {
	return e.f.Spend(class, n)
}

// job is one task of a batch that goes out.
type job struct {
	idx    int
	node   string
	target groups.Target
	tok    groups.Token
	// prev is the stored row the fetch replaces; nil when it had no content.
	prev *store.GroupRow
}

// Execute runs one batch of the group: it drops the tasks that need no work,
// builds each target from the entity's stored summary, fetches the rest in one
// Fetch call, and stores each outcome through the generation-checked Commit.
//
// Class scoping: the keep-alive skip and the already-fresh skip apply to classes
// 3 and above only; a class 1 or 2 task always fetches (a caller is waiting, or
// a write invalidated the group).
func (e *executor) Execute(ctx context.Context, items []scheduler.Item) []scheduler.Result {
	s, group := e.s, e.f.Group()
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
		node := it.Task.Entity
		background := it.Task.Class >= governor.ClassQuery
		ent, ok, err := s.cfg.Store.GetEntity(ctx, entity.KindPR, node)
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
		sum, _, haveSum, err := s.loadSummary(ctx, node)
		if err != nil {
			res[i].Err = err
			continue
		}
		if !haveSum {
			// A group fetch needs the stored reference, head, base and state.
			res[i].Skipped = true
			continue
		}
		stamps := groups.StampsOf(sum)
		cur, curOK, err := s.cfg.Store.GetGroup(ctx, entity.KindPR, node, group)
		if err != nil {
			res[i].Err = err
			continue
		}
		if background && it.Role == scheduler.RoleDue && curOK && s.cfg.Rules.Fresh(cur, stamps, now).IsFresh() {
			// Another path refreshed it since this task was queued: book the next cycle.
			res[i].Skipped = true
			_ = s.bookAfter(ctx, node, group, members)
			continue
		}
		ref, err := entity.NewRef(sum.Repo, sum.Number)
		if err != nil {
			res[i].Err = fmt.Errorf("groupsvc: %s: %w", node, err)
			continue
		}
		j := job{
			idx:  i,
			node: node,
			target: groups.Target{
				NodeID: node, Ref: ref, Stamps: stamps, HeadRefName: sum.HeadRefName,
				State: sum.State, Terminal: sum.State == "MERGED" || sum.State == "CLOSED",
			},
			// The token carries the generation of the very row read as prev, so the
			// previous content and the generation Commit checks cannot disagree.
			tok: groups.Token{Kind: entity.KindPR, ID: node, Group: group, Generation: cur.Generation},
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

	targets := make([]groups.Target, len(jobs))
	for k, j := range jobs {
		targets[k] = j.target
	}
	info := RefreshInfo{Started: s.now(), Class: class, Purpose: purposeOf(class)}
	outs := e.f.Fetch(ctx, ratefeed.Meta{Class: class, Purpose: info.Purpose, Group: group, BatchSize: len(targets)}, targets)
	for k, j := range jobs {
		if k >= len(outs) {
			res[j.idx].Err = e.fail(ctx, j, fmt.Errorf("groupsvc: the %s fetcher gave no answer for %s", group, j.node))
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
func (e *executor) apply(ctx context.Context, j job, o groups.Outcome, info RefreshInfo, members map[string]bool) error {
	s, group := e.s, e.f.Group()
	if o.Err != nil {
		if errors.Is(o.Err, groups.ErrGone) {
			return s.tombstone(ctx, j.node, info.Purpose)
		}
		return e.fail(ctx, j, o.Err)
	}
	if len(o.Content) == 0 {
		return e.fail(ctx, j, fmt.Errorf("groupsvc: the %s fetcher returned no content for %s", group, j.node))
	}
	fetchedAt := s.now()
	freshUntil, maxAgeAt := s.cfg.Rules.Schedule(group, j.target.Terminal, fetchedAt)
	if !o.FreshUntil.IsZero() {
		freshUntil = o.FreshUntil
	}
	stamps := o.Stamps
	if stamps == (groups.Stamps{}) {
		stamps = j.target.Stamps
	}
	fp := o.Fingerprint
	if fp == "" {
		fp = groups.Fingerprint(o.Content)
	}
	row := store.GroupRow{
		Content: o.Content, Fingerprint: fp, FetchedAt: fetchedAt, FreshUntil: freshUntil, MaxAgeAt: maxAgeAt,
		LastError: o.LastError, FetchedForHead: stamps.Head, FetchedForBase: stamps.Base,
	}
	row.Kind, row.ID, row.Group, row.Generation = j.tok.Kind, j.tok.ID, j.tok.Group, j.tok.Generation+1
	var then func(tx *store.Tx) error
	if s.cfg.OnGroupStoring != nil {
		then = func(tx *store.Tx) error { return s.cfg.OnGroupStoring(ctx, tx, j.prev, row, info) }
	}
	stored, err := groups.CommitThen(ctx, s.cfg.Store, j.tok, row, then)
	if err != nil {
		return err
	}
	if stored {
		s.countCommit(j.node, group)
		if s.cfg.Identity != nil {
			s.cfg.Identity.Clear()
		}
		if s.cfg.OnGroupRefreshed != nil {
			s.cfg.OnGroupRefreshed(j.prev, row, info)
		}
	}
	// Stored or superseded, the cycle follows the row now in the store: a write
	// that superseded this fetch left it due now, so it is refetched at once.
	return s.bookAfter(ctx, j.node, group, members)
}

// fail books a failed fetch: the error is classified (a rate-limit answer pauses
// the background classes and is not an authentication failure), recorded as the
// row's last_error (a governor refusal is the budget speaking, not the origin
// failing, and is not), and returned so the scheduler requeues the task.
func (e *executor) fail(ctx context.Context, j job, err error) error {
	s := e.s
	err = metered.Classify(s.cfg.Governor, s.cfg.FallbackPause, err)
	var refusal *governor.Refusal
	if errors.As(err, &refusal) {
		return err
	}
	if s.cfg.Identity != nil && !metered.IsRateLimit(err) && errors.Is(err, vcs.ErrAuthInvalid) {
		s.cfg.Identity.MarkUnauthenticated(err)
	}
	s.recordError(ctx, j.node, e.f.Group(), err)
	return err
}

// recordError sets last_error on the stored group row, keeping its content. It is
// a compare-and-swap on the generation, so it can never overwrite a refresh that
// stored meanwhile; a missing row stays missing.
func (s *Service) recordError(ctx context.Context, node, group string, cause error) {
	row, ok, err := s.cfg.Store.GetGroup(ctx, entity.KindPR, node, group)
	if err != nil || !ok {
		return
	}
	row.LastError = cause.Error()
	_, _ = s.cfg.Store.PutGroupIf(ctx, row, row.Generation)
}

// tombstone marks the entity gone: GitHub no longer shows it. The row is read
// and tombstoned_at is written in one transaction (so the write cannot rewind a
// version raised meanwhile), and OnGone runs in it, only when the entity was
// visible before; an OnGone error rolls the tombstone back. Its queued tasks are
// cancelled and its stored groups are left for expiry.
func (s *Service) tombstone(ctx context.Context, node string, purpose governor.Purpose) error {
	err := s.cfg.Store.InTx(ctx, func(tx *store.Tx) error {
		row, ok, err := tx.GetEntity(ctx, entity.KindPR, node)
		if err != nil || !ok {
			return err
		}
		visible := row.TombstonedAt.IsZero()
		row.TombstonedAt = s.now()
		if err := tx.PutEntity(ctx, row); err != nil {
			return err
		}
		if visible && s.cfg.OnGone != nil {
			return s.cfg.OnGone(ctx, tx, node, purpose)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.cfg.Scheduler.CancelEntity(node)
	return nil
}
