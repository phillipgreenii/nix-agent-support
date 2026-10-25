package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// EnsureOptions are the options of Ensure.
type EnsureOptions struct {
	// Fresh forces the fetch even when the stored summary is still fresh.
	Fresh bool
}

// Result is the answer of Ensure.
type Result struct {
	// Content is the issue's summary content: the stored one, or the one just
	// fetched.
	Content groups.SummaryContent
	// Row is the stored summary group row behind the answer (the zero row when
	// the fetched content could not be stored).
	Row store.GroupRow
	// FetchedNow is true when this call fetched the summary from the origin.
	FetchedNow bool
	// Stale is true when the content is known to be out of date: it was served
	// stored because the fetch could not run, or the fetch lost the race to a
	// write and the write's re-pull is queued.
	Stale bool
	// AgeSeconds is the governor clock's now minus the stored row's fetched
	// time (zero when nothing is stored).
	AgeSeconds int64
}

// Ensure is the read-through of one issue:
//
//   - a key that is not Jira-shaped answers scriptout.ErrNotFound with no
//     origin call;
//   - a stored summary inside its fresh_until is a local hit (no origin call)
//     unless opts.Fresh;
//   - otherwise ONE interactive (class 1) search fetches and stores the issue
//     and tracks it (KeepAlive.Access), so a direct read follows the same
//     retention rule as a query result; an empty search result tombstones a
//     known issue and answers scriptout.ErrNotFound;
//   - when the fetch cannot run (the origin is paused after a 429, the governor
//     refuses, the origin fails or the search is truncated) a stored summary
//     inside the expiry is served stale, and otherwise the answer is
//     scriptout.ErrUnavailable naming the cause. A tombstoned issue has nothing
//     to serve stale.
//
// ctx cancelling the fetch returns ctx's error.
func (s *Service) Ensure(ctx context.Context, key string, opts EnsureOptions) (Result, error) {
	key = strings.TrimSpace(key)
	if !internal.IsJiraKey(key) {
		return Result{}, scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("%q is not a Jira issue key", key))
	}
	id := groups.FoldRef(key)
	now := s.now()
	stored, haveStored, err := s.storedSummary(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if haveStored && !opts.Fresh && stored.row.FreshUntil.After(now) {
		if err := s.cfg.KeepAlive.Access(ctx, id); err != nil {
			return Result{}, err
		}
		return Result{Content: stored.content, Row: stored.row, AgeSeconds: ageSeconds(now, stored.row)}, nil
	}

	// The token is taken BEFORE the fetch, so a write in between makes the
	// commit lose.
	tok, err := s.cfg.Persister.Begin(ctx, id)
	if err != nil {
		return Result{}, err
	}
	started := now
	jql, err := keyInJQL([]string{id})
	if err != nil {
		return Result{}, scriptout.WrapError(scriptout.ErrNotFound, err.Error())
	}
	call := origin.Call{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive, Group: groups.Summary, BatchSize: 1}
	got, err := s.cfg.Fetcher.Search(origin.WithCall(ctx, call), jql, expand)
	if err == nil && got.Truncated {
		err = errTruncated
	}
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return s.unavailable(id, now, stored, haveStored, err)
	}

	var iss *internal.PjiraIssue
	for i := range got.Items {
		if groups.FoldRef(got.Items[i].Key) == id {
			iss = &got.Items[i]
			break
		}
	}
	if iss == nil {
		if err := s.cfg.Persister.MarkGone(ctx, id, governor.PurposeInteractive); err != nil {
			return Result{}, err
		}
		if s.cfg.Observer != nil {
			s.cfg.Observer.Gone(id)
		}
		return Result{}, scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("issue %s not found", id))
	}

	content := ContentFromPjira(iss, s.now())
	st, err := s.cfg.Persister.Persist(ctx, tok, content, engine.RefreshInfo{
		Started: started, Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive,
	})
	if err != nil {
		return Result{}, err
	}
	if st.FreshUntil.IsZero() {
		// Lost the race to a write: its re-pull is queued, so book nothing. The
		// content is what the origin said before the write.
		row, _, err := s.cfg.Store.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
		if err != nil {
			return Result{}, err
		}
		return Result{Content: content, Row: row, FetchedNow: true, Stale: true, AgeSeconds: ageSeconds(s.now(), row)}, nil
	}
	if err := s.cfg.KeepAlive.Access(ctx, id); err != nil {
		return Result{}, err
	}
	members, err := s.members(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := s.bookNext(ctx, id, st.FreshUntil, members); err != nil {
		return Result{}, err
	}
	row, _, err := s.cfg.Store.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: content, Row: row, FetchedNow: true, AgeSeconds: ageSeconds(s.now(), row)}, nil
}

// storedContent is the decoded stored summary of a visible issue. A tombstoned
// issue, a missing group and content that does not decode all read as nothing
// stored.
type storedContent struct {
	content groups.SummaryContent
	row     store.GroupRow
}

func (s *Service) storedSummary(ctx context.Context, id string) (storedContent, bool, error) {
	ent, ok, err := s.cfg.Store.GetEntity(ctx, groups.EntityKind, id)
	if err != nil {
		return storedContent{}, false, err
	}
	if ok && !ent.TombstonedAt.IsZero() {
		return storedContent{}, false, nil
	}
	g, ok, err := s.cfg.Store.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
	if err != nil || !ok || len(g.Content) == 0 {
		return storedContent{}, false, err
	}
	var c groups.SummaryContent
	if json.Unmarshal(g.Content, &c) != nil {
		return storedContent{}, false, nil
	}
	return storedContent{content: c, row: g}, true, nil
}

// unavailable is the answer when the fetch could not run: the stored summary
// served stale when it is inside the expiry, else scriptout.ErrUnavailable
// naming the cause.
func (s *Service) unavailable(id string, now time.Time, stored storedContent, have bool, cause error) (Result, error) {
	if have && now.Sub(stored.row.FetchedAt) <= s.cfg.Expiry {
		return Result{Content: stored.content, Row: stored.row, Stale: true, AgeSeconds: ageSeconds(now, stored.row)}, nil
	}
	reason := cause.Error()
	switch {
	case errors.Is(cause, origin.ErrPaused):
		reason = "the origin is paused: " + reason
	case errors.Is(cause, governor.ErrRefused):
		reason = "the request budget refused the call: " + reason
	}
	return Result{}, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("issue %s has no stored summary and the origin cannot be asked: %s", id, reason))
}

func ageSeconds(now time.Time, row store.GroupRow) int64 {
	if row.FetchedAt.IsZero() {
		return 0
	}
	return int64(now.Sub(row.FetchedAt) / time.Second)
}
