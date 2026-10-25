package detail

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
)

// The result words of the revalidation metric (the shared observer's).
const (
	// ResultExtended: the stored detail's fresh_until was extended instead of a
	// re-pull.
	ResultExtended = "extended"
	// ResultRepulled: the detail was queued for a fetch (a changed token, or the
	// baseline of a first sighting).
	ResultRepulled = "repulled"
	// ResultMaxAge: the detail could not be extended because it is at its
	// maximum age, so a re-pull was queued.
	ResultMaxAge = "max_age"
)

// Revalidate is the function packet serve installs on
// summary.Persister.AfterCommit. It runs after a summary was stored, makes no
// origin call and appends no change row: it only reads the store and queues or
// extends the detail group, so it does not block the persister.
//
//   - A first sighting (nil prev) queues the baseline detail fetch for an issue
//     the keep-alive rule retains, as a background task.
//   - A changed Updated token queues the detail refresh at once (class 2).
//   - An unchanged token extends the stored detail's fresh_until to
//     min(max_age_at, now + detail_ttl), and when the row is already at its
//     maximum age queues a re-pull instead.
//   - An issue for which groups.Terminal is true is neither extended nor
//     re-pulled: its stored detail serves until eviction.
//
// A refresh already queued at class 1 or 2, or running, is left alone: it is
// neither replaced nor postponed.
func (s *Service) Revalidate(ctx context.Context, prev *groups.SummaryContent, next groups.SummaryContent, st summary.Stored) {
	if next.Gone || next.Issue.ID == "" {
		return
	}
	id := groups.FoldRef(next.Issue.ID)
	now := s.clk.Now()
	var err error
	switch {
	case prev == nil || st.First:
		err = s.populate(ctx, id, now)
	default:
		var terminal bool
		if terminal, err = s.terminal(ctx, id, now); err != nil || terminal {
			break
		}
		if prev.Updated != next.Updated {
			err = s.svc.QueueGroup(ctx, id, groups.Detail, governor.ClassInvalidated, now)
			if err == nil {
				s.svc.Revalidated(groups.Detail, ResultRepulled)
			}
			break
		}
		err = s.extend(ctx, id, now)
	}
	if err != nil && s.cfg.OnError != nil && !errors.Is(err, context.Canceled) {
		s.cfg.OnError(err)
	}
}

// terminal reports whether the issue has been closed for the terminal TTL.
func (s *Service) terminal(ctx context.Context, id string, now time.Time) (bool, error) {
	ent, ok, err := s.cfg.Store.GetEntity(ctx, groups.EntityKind, id)
	if err != nil || !ok {
		return false, err
	}
	return groups.Terminal(ent.ClosedAt, now, s.cfg.Rules.TerminalTTL), nil
}

// populate books the baseline detail fetch of a first sighting when the issue
// is in the keep-alive set. A detail already fresh is skipped by the executor.
func (s *Service) populate(ctx context.Context, id string, now time.Time) error {
	keep, err := s.cfg.KeepAlive.Retain(ctx, id)
	if err != nil || !keep {
		return err
	}
	if err := s.svc.RescheduleGroup(ctx, id, groups.Detail, now); err != nil {
		return err
	}
	s.svc.Revalidated(groups.Detail, ResultRepulled)
	return nil
}

// extend revalidates the stored detail of an issue whose token did not change.
// The write is generation-checked, so an invalidation that lands first wins.
func (s *Service) extend(ctx context.Context, id string, now time.Time) error {
	row, ok, err := s.cfg.Store.GetGroup(ctx, groups.EntityKind, id, groups.Detail)
	if err != nil || !ok || len(row.Content) == 0 {
		return err
	}
	class, queued, running, err := s.svc.Inspect(ctx, id, groups.Detail)
	if err != nil {
		return err
	}
	if running || (queued && class <= governor.ClassInvalidated) {
		return nil
	}
	until, ok := s.cfg.Rules.Extend(row, groups.Detail, now)
	if !ok {
		if row.MaxAgeAt.IsZero() || now.Before(row.MaxAgeAt) {
			return nil // no maximum recorded: the row's own schedule stands
		}
		// At the maximum age: the stored detail may not be served as current,
		// so a re-pull is queued (replacing a later-due background task).
		if err := s.svc.RescheduleGroup(ctx, id, groups.Detail, now); err != nil {
			return err
		}
		s.svc.Revalidated(groups.Detail, ResultMaxAge)
		return nil
	}
	tok := groupkit.Token{Kind: groups.EntityKind, ID: id, Group: groups.Detail, Generation: row.Generation}
	row.FreshUntil = until
	stored, err := groupkit.Commit(ctx, s.cfg.Store, tok, row)
	if err != nil || !stored {
		return err // an invalidation landed first and wins
	}
	s.svc.Revalidated(groups.Detail, ResultExtended)
	return s.svc.RescheduleGroup(ctx, id, groups.Detail, until)
}
