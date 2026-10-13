package groupsvc

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
)

// Rebuild queues the background group tasks from the stored due times, as the
// daemon does on start (call it after the summary service's Rebuild and after
// every group's Register). Every entity in the keep-alive set that has a stored
// summary gets, for each registered group with a due time, a task due at the
// row's fresh_until (now when the row is missing). Nothing is fetched here: the
// scheduler asks the governor before every batch, so a restart serves the fresh
// groups with no origin call, and after a long outage the catch-up runs at the
// governor's rate. It returns how many tasks it queued.
func (s *Service) Rebuild(ctx context.Context) (int, error) {
	rows, err := s.cfg.Store.ListEntities(ctx, entity.KindPR)
	if err != nil {
		return 0, err
	}
	members, err := s.members(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ent := range rows {
		in, err := s.inKeepAlive(ctx, ent, members)
		if err != nil {
			return n, err
		}
		if !in {
			continue
		}
		// A group fetch needs the stored summary; the summary refresh hook queues
		// the groups of an entity that has none yet.
		if _, _, have, err := s.loadSummary(ctx, ent.ID); err != nil {
			return n, err
		} else if !have {
			continue
		}
		for _, g := range s.registered() {
			row, exists, err := s.cfg.Store.GetGroup(ctx, entity.KindPR, ent.ID, g)
			if err != nil {
				return n, err
			}
			t, ok, err := s.backgroundTask(ctx, ent.ID, g, row, exists, members)
			if err != nil {
				return n, err
			}
			if !ok {
				continue
			}
			if err := s.cfg.Scheduler.Submit(t); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
