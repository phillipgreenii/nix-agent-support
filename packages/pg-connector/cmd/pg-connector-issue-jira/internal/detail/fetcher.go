package detail

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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

// expand is what the detail search asks pjira to carry along: the comments are
// the stored content, and the changelog and comments are what the two operator
// facts are computed from.
var expand = []string{"changelog", "comments"}

// Origin is the one origin call the fetcher makes. *origin.Runner satisfies it;
// the call's class, purpose, group and batch size ride on ctx (origin.WithCall).
type Origin interface {
	Search(ctx context.Context, jql string, expand []string) (*internal.PjiraSearchResult, error)
}

// Facts computes the two operator facts of an issue as fetched, with its
// changelog and comments. (*internal.Backend).OperatorFacts satisfies it.
type Facts func(ctx context.Context, iss *internal.PjiraIssue) (statusChangedAt, operatorUpdatedAt string)

// EntityStore is the part of the daemon store the fetcher reads (the closed-at
// of the entity row). *store.Store satisfies it.
type EntityStore interface {
	groupsvc.Store
}

// Fetcher fetches the detail group; it implements groupsvc.Fetcher.
type Fetcher struct {
	Origin Origin
	Store  EntityStore
	Rules  groupkit.Table
	Facts  Facts
	Clock  governor.Clock
	// BodyCap is the byte cap of a stored comment body (change_body_cap).
	BodyCap int
	// Bucket is the governor bucket one search spends from; empty means
	// origin.Bucket.
	Bucket governor.Bucket
}

var _ groupsvc.Fetcher = (*Fetcher)(nil)

// Group implements groupsvc.Fetcher.
func (f *Fetcher) Group() string { return groups.Detail }

// MaxBatch implements groupsvc.Fetcher: one issue per call.
func (f *Fetcher) MaxBatch() int { return 1 }

// Spend implements groupsvc.Fetcher: one request of the declared bucket.
func (f *Fetcher) Spend(class governor.Class, _ int) governor.Request {
	b := f.Bucket
	if b == "" {
		b = origin.Bucket
	}
	return governor.Request{Bucket: b, Class: class, Cost: 1}
}

func (f *Fetcher) now() time.Time {
	if f.Clock == nil {
		return governor.SystemClock{}.Now()
	}
	return f.Clock.Now()
}

// Fetch implements groupsvc.Fetcher: one search per target, in order. A target
// the origin no longer shows is groupsvc.ErrGone. Once a call fails with a
// rate-limit answer, a refusal or a finished context, the targets after it
// carry the same error without a call.
func (f *Fetcher) Fetch(ctx context.Context, call groupsvc.CallMeta, targets []groupsvc.Target) []groupsvc.Outcome {
	out := make([]groupsvc.Outcome, len(targets))
	var stop error
	for i, t := range targets {
		if stop != nil {
			out[i].Err = stop
			continue
		}
		out[i] = f.fetchOne(ctx, call, t)
		if err := out[i].Err; err != nil && (origin.IsRateLimited(err) || errors.Is(err, governor.ErrRefused) || ctx.Err() != nil) {
			stop = err
		}
	}
	return out
}

func (f *Fetcher) fetchOne(ctx context.Context, call groupsvc.CallMeta, t groupsvc.Target) groupsvc.Outcome {
	key := t.Ref
	if key == "" {
		key = t.ID
	}
	if !internal.IsJiraKey(key) {
		return groupsvc.Outcome{Err: fmt.Errorf("detail: %q is not a Jira key", key)}
	}
	oc := origin.Call{Class: call.Class, Purpose: call.Purpose, Group: groups.Detail, BatchSize: 1}
	res, err := f.Origin.Search(origin.WithCall(ctx, oc), `key = "`+key+`"`, expand)
	if err != nil {
		return groupsvc.Outcome{Err: err, Cost: 1}
	}
	var iss *internal.PjiraIssue
	for i := range res.Items {
		if strings.EqualFold(res.Items[i].Key, key) {
			iss = &res.Items[i]
			break
		}
	}
	if iss == nil {
		return groupsvc.Outcome{Err: groupsvc.ErrGone, Cost: 1}
	}

	content := f.Content(ctx, iss)
	body, err := json.Marshal(content)
	if err != nil {
		return groupsvc.Outcome{Err: fmt.Errorf("detail: encode %s: %w", key, err), Cost: 1}
	}
	now := f.now()
	var closedAt time.Time
	if ent, ok, err := f.Store.GetEntity(ctx, groups.EntityKind, t.ID); err != nil {
		return groupsvc.Outcome{Err: err, Cost: 1}
	} else if ok {
		closedAt = ent.ClosedAt
	}
	fresh, _ := f.Rules.Schedule(groups.Detail, groups.Terminal(closedAt, now, f.Rules.TerminalTTL), now)
	return groupsvc.Outcome{
		Content:     body,
		Fingerprint: groups.FingerprintDetail(content, f.BodyCap),
		FreshUntil:  fresh,
		Cost:        1,
	}
}

// Content maps an issue fetched with its comments and changelog to the stored
// detail: the comments with their ids, authors, bodies (cut to BodyCap) and
// creation text, no edges (the origin tool exposes no dependency concept), no
// description (it emits none) and the two operator facts.
func (f *Fetcher) Content(ctx context.Context, iss *internal.PjiraIssue) groups.DetailContent {
	var d groups.DetailContent
	for _, c := range iss.Comments {
		d.Comments = append(d.Comments, groups.Comment{
			ID: c.ID, Author: authorOf(c.Author.DisplayName, c.Author.Email, c.Author.AccountID),
			Body: c.Body, Created: c.Created,
		})
	}
	// The bodies are cut now, the facts are added after: NormaliseDetail zeroes
	// the facts, which are stored but never compared.
	d = groups.NormaliseDetail(d, f.BodyCap)
	if f.Facts != nil {
		d.StatusChangedAt, d.OperatorUpdatedAt = f.Facts(ctx, iss)
	}
	return d
}

// authorOf is the first non-empty of a comment author's display name, email and
// account id.
func authorOf(names ...string) string {
	for _, n := range names {
		if n != "" {
			return n
		}
	}
	return ""
}
