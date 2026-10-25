package summary

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// Rebuild rebuilds the scheduler's queue from the stored due times, as the
// daemon does on start: it books a class 4 (keep-alive) summary task, due at the
// stored fresh_until (now when there is none), for every retained,
// non-tombstoned issue that is not a member of a configured query. A task that
// is not due yet still rides a batch as fill. An issue that has been closed for
// the terminal TTL or longer is not retained, so nothing is queued for it: it
// is served from the store and never refreshed. Nothing is fetched here; the
// scheduler asks the governor before every batch, so a restart after a long
// outage catches up at the governor's rate. It returns how many tasks it queued.
func (s *Service) Rebuild(ctx context.Context) (queued int, err error) {
	rows, err := s.cfg.Store.ListEntities(ctx, groups.EntityKind)
	if err != nil {
		return 0, err
	}
	members, err := s.members(ctx)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if !row.TombstonedAt.IsZero() || members[row.ID] || !internal.IsJiraKey(row.ID) {
			continue
		}
		keep, err := s.cfg.KeepAlive.Retain(ctx, row.ID)
		if err != nil {
			return queued, err
		}
		if !keep {
			continue
		}
		g, _, err := s.cfg.Store.GetGroup(ctx, groups.EntityKind, row.ID, groups.Summary)
		if err != nil {
			return queued, err
		}
		// A zero fresh_until (no stored summary yet) is due now.
		if err := s.cfg.Scheduler.Submit(s.task(row.ID, governor.ClassKeepAlive, g.FreshUntil, row.LastAccess)); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}
