package groupsvc

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// dueOf is when the group's next background task is due, and false when it has
// none. A group the rules table does not mark as background is never refreshed
// in the background. A stored row is due at its fresh_until; a stamped row with
// no fresh_until has no time limit and so no time-based task (its refresh is
// driven by its stamps). A missing or untimed row of a group with a TTL is due
// now.
func (s *Service) dueOf(group string, row store.GroupRow, exists bool) (time.Time, bool) {
	if !s.cfg.Rules.Background(group) {
		return time.Time{}, false
	}
	if exists && !row.FreshUntil.IsZero() {
		return row.FreshUntil, true
	}
	if exists && s.cfg.Rules.Stamped(group) {
		return time.Time{}, false
	}
	if s.cfg.Rules.TTL(group) > 0 {
		return time.Time{}, true
	}
	return time.Time{}, false
}

// backgroundTask is the task of the group's next cycle for an entity in the
// keep-alive set; ok is false when there is none (the entity is outside the set,
// or the group has no due time).
func (s *Service) backgroundTask(ctx context.Context, id, group string, row store.GroupRow, exists bool, members map[string]bool) (scheduler.Task, bool, error) {
	due, ok := s.dueOf(group, row, exists)
	if !ok {
		return scheduler.Task{}, false, nil
	}
	ent, found, err := s.cfg.Store.GetEntity(ctx, s.cfg.Spec.EntityKind, id)
	if err != nil || !found {
		return scheduler.Task{}, false, err
	}
	in, err := s.inKeepAlive(ctx, ent, members)
	if err != nil || !in {
		return scheduler.Task{}, false, err
	}
	return scheduler.Task{
		Kind: group, Entity: id, Group: group, Class: classFor(members[id]), Due: due, Access: ent.LastAccess,
	}, true, nil
}

// bookAfter books the next background cycle of a group the executor has just
// refreshed (or found superseded or fresh), from the row now stored. The
// executor's own task still counts as running, so only a QUEUED task of class 1
// or 2 is protected: it is left alone, never replaced by a background one.
func (s *Service) bookAfter(ctx context.Context, id, group string, members map[string]bool) error {
	row, exists, err := s.cfg.Store.GetGroup(ctx, s.cfg.Spec.EntityKind, id, group)
	if err != nil {
		return err
	}
	t, ok, err := s.backgroundTask(ctx, id, group, row, exists, members)
	if err != nil || !ok {
		return err
	}
	if c, queued := s.cfg.Scheduler.InspectQueued(id, group); queued && c <= governor.ClassInvalidated {
		return nil
	}
	return s.cfg.Scheduler.Reschedule(t)
}

// bookRead books the cycle of a row an Ensure served from the store: it is a
// Submit, which merges with a queued task (the better class and the earlier due
// win) and so only ever makes the booking more urgent.
func (s *Service) bookRead(ctx context.Context, id, group string, row store.GroupRow, members map[string]bool) error {
	t, ok, err := s.backgroundTask(ctx, id, group, row, true, members)
	if err != nil || !ok {
		return err
	}
	return s.cfg.Scheduler.Submit(t)
}
