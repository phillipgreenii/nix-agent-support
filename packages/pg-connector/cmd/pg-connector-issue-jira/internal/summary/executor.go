package summary

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
)

// expand is what a summary search asks pjira to carry along: the comments give
// the comment summary, and the changelog is what the detail group later reads.
var expand = []string{"changelog", "comments"}

// executor adapts the service to scheduler.Executor.
type executor struct{ s *Service }

func (e executor) MaxBatch() int { return e.s.cfg.BatchSize }

// Spend is one request of the declared bucket whatever the batch size: one
// search carries the whole batch.
func (e executor) Spend(class governor.Class, _ int) governor.Request {
	return governor.Request{Bucket: e.s.cfg.Bucket, Class: class, Cost: 1}
}

func purposeOf(class governor.Class) governor.Purpose {
	switch class {
	case governor.ClassInteractive:
		return governor.PurposeInteractive
	case governor.ClassInvalidated:
		return governor.PurposeWrite
	case governor.ClassFill:
		return governor.PurposeFill
	default:
		return governor.PurposeRefresh
	}
}

// keyInJQL is the JQL that selects the keys: key in ("A-1", "B-2"). Every key
// is matched against the Jira key shape first, so none can carry JQL syntax.
func keyInJQL(keys []string) (string, error) {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		if !internal.IsJiraKey(k) {
			return "", fmt.Errorf("summary: %q is not a Jira key", k)
		}
		quoted[i] = `"` + k + `"`
	}
	return "key in (" + strings.Join(quoted, ", ") + ")", nil
}

// errTruncated fails a batch whose search came back truncated: a missing key
// cannot be told from one past the cut, so no absence is proved.
var errTruncated = errors.New("summary: search result truncated; absence cannot be proved")

// sent is one item of a batch that goes out.
type sent struct {
	idx int // index into the items
	id  string
	tok groupkit.Token
}

// Execute runs one batch. It drops the items that need no work (not retained,
// or already fresh), takes a generation token for each of the rest, makes ONE
// search for all of them, stores each issue the search returned and tombstones
// each one it did not. A governor refusal is returned as every sent item's
// error, so the scheduler holds the class. After each store it books the next
// refresh of a retained non-member issue.
func (e executor) Execute(ctx context.Context, items []scheduler.Item) []scheduler.Result {
	s := e.s
	res := make([]scheduler.Result, len(items))
	now := s.now()
	members, err := s.members(ctx)
	if err != nil {
		return failAll(res, err)
	}

	var (
		out   []sent
		class = governor.ClassFill
		due   []string
		fill  []string
	)
	for i, it := range items {
		id := groups.FoldRef(it.Task.Entity)
		if !internal.IsJiraKey(id) {
			res[i].Skipped = true
			continue
		}
		if it.Task.Class != governor.ClassInteractive {
			keep, err := s.cfg.KeepAlive.Retain(ctx, id)
			if err != nil {
				res[i].Err = err
				continue
			}
			if !keep {
				res[i].Skipped = true
				continue
			}
		}
		if it.Role == scheduler.RoleDue && it.Task.Class >= governor.ClassQuery {
			// Another path (an interactive read, an earlier batch) may have
			// refreshed it since this task was queued: book the next cycle.
			g, ok, err := s.cfg.Store.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
			if err != nil {
				res[i].Err = err
				continue
			}
			if ok && g.FreshUntil.After(now) {
				res[i].Skipped = true
				if err := s.bookNext(ctx, id, g.FreshUntil, members); err != nil {
					res[i].Err = err
				}
				continue
			}
		}
		// The token is taken BEFORE the search, so a write between the fetch
		// and the commit makes the commit lose.
		tok, err := s.cfg.Persister.Begin(ctx, id)
		if err != nil {
			res[i].Err = err
			continue
		}
		out = append(out, sent{idx: i, id: id, tok: tok})
		if it.Role == scheduler.RoleDue {
			class = min(class, it.Task.Class)
			due = append(due, id)
		} else {
			fill = append(fill, id)
		}
	}
	if len(out) == 0 {
		return res
	}
	if class == governor.ClassFill { // only riders left: admit as a keep-alive refresh
		class = governor.ClassKeepAlive
	}
	if s.cfg.Observer != nil {
		if len(due) > 0 {
			s.cfg.Observer.BatchSent(due, scheduler.RoleDue)
		}
		if len(fill) > 0 {
			s.cfg.Observer.BatchSent(fill, scheduler.RoleFill)
		}
	}

	keys := make([]string, len(out))
	for k, o := range out {
		keys[k] = o.id
	}
	jql, err := keyInJQL(keys)
	if err != nil {
		for _, o := range out {
			res[o.idx].Err = err
		}
		return res
	}
	purpose := purposeOf(class)
	started := s.now()
	call := origin.Call{Class: class, Purpose: purpose, Group: groups.Summary, BatchSize: len(out)}
	got, err := s.cfg.Fetcher.Search(origin.WithCall(ctx, call), jql, expand)
	if err == nil && got.Truncated {
		err = errTruncated
	}
	if err != nil {
		for _, o := range out {
			res[o.idx].Err = err
		}
		return res
	}

	byKey := make(map[string]*internal.PjiraIssue, len(got.Items))
	for i := range got.Items {
		byKey[groups.FoldRef(got.Items[i].Key)] = &got.Items[i]
	}
	info := engine.RefreshInfo{Started: started, Class: class, Purpose: purpose}
	for _, o := range out {
		iss, found := byKey[o.id]
		if !found {
			// The origin no longer shows the issue: stop refreshing it.
			if err := s.cfg.Persister.MarkGone(ctx, o.id, purpose); err != nil {
				res[o.idx].Err = err
				continue
			}
			if s.cfg.Observer != nil {
				s.cfg.Observer.Gone(o.id)
			}
			continue
		}
		res[o.idx].Err = s.store(ctx, o.tok, o.id, iss, info, members)
	}
	return res
}

// store persists one fetched issue and books its next refresh. A Persist that
// lost the generation race stored nothing and books nothing: the write notice's
// re-pull already queued one.
func (s *Service) store(ctx context.Context, tok groupkit.Token, id string, iss *internal.PjiraIssue, info engine.RefreshInfo, members map[string]bool) error {
	st, err := s.cfg.Persister.Persist(ctx, tok, ContentFromPjira(iss, s.now()), info)
	if err != nil {
		return err
	}
	if st.FreshUntil.IsZero() {
		return nil
	}
	return s.bookNext(ctx, id, st.FreshUntil, members)
}

func failAll(res []scheduler.Result, err error) []scheduler.Result {
	for i := range res {
		res[i].Err = err
	}
	return res
}
