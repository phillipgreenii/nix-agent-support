package explain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// MaxLastChanges is how many of the issue's newest change rows an answer
// carries.
const MaxLastChanges = 5

// The values of Result.TrackedWhy.
const (
	TrackedMember  = "member"  // a member of a configured query
	TrackedRead    = "read"    // read by a caller
	TrackedWatched = "watched" // watched
)

// Result is the answer.
type Result struct {
	ID string `json:"id"`

	// Tracked is whether the issue is refreshed in the background; TrackedWhy
	// says why (empty when it is not tracked). RetainedByKeepAlive is the
	// keep-alive rule's verdict alone; ClosedAt is when the issue closed (the
	// zero time while it is open).
	Tracked             bool      `json:"tracked"`
	TrackedWhy          string    `json:"tracked_why,omitempty"`
	RetainedByKeepAlive bool      `json:"retained_by_keep_alive"`
	ClosedAt            time.Time `json:"closed_at,omitzero"`
	Tombstoned          bool      `json:"tombstoned,omitempty"`

	// Watched, WatchedVia and WatchedAt are the sticky watched flag, its
	// recorded cause and when it was first set.
	Watched    bool      `json:"watched"`
	WatchedVia string    `json:"watched_via,omitempty"`
	WatchedAt  time.Time `json:"watched_at,omitzero"`

	Groups      []GroupExplain  `json:"groups"`
	Pending     *PendingExplain `json:"pending,omitempty"`
	LastChanges []ChangeExplain `json:"last_changes,omitempty"`
}

// GroupExplain is one group of the issue.
type GroupExplain struct {
	Group      string    `json:"group"`
	FetchedAt  time.Time `json:"fetched_at,omitzero"`
	FreshUntil time.Time `json:"fresh_until,omitzero"`
	MaxAgeAt   time.Time `json:"max_age_at,omitzero"`
	// Fresh is the freshness table's verdict at the time of the answer and
	// FreshReason says why a group is not fresh.
	Fresh       bool   `json:"fresh"`
	FreshReason string `json:"fresh_reason,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	// QueuePosition is the 1-based rank of the group's queued task (1 is next in
	// line), -1 when it is not queued.
	QueuePosition int  `json:"queue_position"`
	Queued        bool `json:"queued"`
	Running       bool `json:"running"`
}

// PendingExplain is the issue's pending settle row: the groups it still awaits
// and the instant after which it is ended whether or not they arrived.
type PendingExplain struct {
	Awaited  []string  `json:"awaited"`
	Deadline time.Time `json:"deadline"`
}

// ChangeExplain is one change row in short form.
type ChangeExplain struct {
	Seq    int64     `json:"seq"`
	Kinds  []string  `json:"kinds"`
	At     time.Time `json:"at"`
	Origin string    `json:"origin,omitempty"`
}

type handler struct{ deps Deps }

type wireArgs struct {
	ID string `json:"id"`
}

func (h *handler) handle(ctx context.Context, raw json.RawMessage) (any, error) {
	var a wireArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode explain args: "+err.Error())
	}
	a.ID = strings.TrimSpace(a.ID)
	if a.ID == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, `explain needs the argument "id" (an issue key)`)
	}
	notFound := scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("no stored issue %q", a.ID))
	if !internal.IsJiraKey(a.ID) {
		return nil, notFound
	}
	id := groups.FoldRef(a.ID)

	ent, ok, err := h.deps.Store.GetEntity(ctx, groups.EntityKind, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound
	}
	router.Annotate(ctx, "store", false, 0, 0)
	now := h.deps.Clock.Now()

	res := Result{
		ID:         id,
		ClosedAt:   utc(ent.ClosedAt),
		Tombstoned: !ent.TombstonedAt.IsZero(),
		Watched:    !ent.WatchedAt.IsZero(),
		WatchedVia: ent.WatchedVia,
		WatchedAt:  utc(ent.WatchedAt),
		Groups:     []GroupExplain{},
	}
	if err := h.fillTracked(ctx, &res, ent); err != nil {
		return nil, err
	}
	if err := h.fillGroups(ctx, &res, now); err != nil {
		return nil, err
	}
	if err := h.fillPending(ctx, &res); err != nil {
		return nil, err
	}
	if err := h.fillChanges(ctx, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// fillTracked sets the tracked verdict: a live entity is tracked while a
// configured query lists it or the keep-alive rule retains it, and the cause is
// the first of member, watched and read that holds.
func (h *handler) fillTracked(ctx context.Context, res *Result, ent store.EntityRow) error {
	retained, err := h.deps.KeepAlive.Retain(ctx, res.ID)
	if err != nil {
		return err
	}
	res.RetainedByKeepAlive = retained
	if res.Tombstoned {
		return nil
	}
	member, err := h.isMember(ctx, res.ID)
	if err != nil {
		return err
	}
	if !member && !retained {
		return nil
	}
	res.Tracked = true
	switch {
	case member:
		res.TrackedWhy = TrackedMember
	case !ent.WatchedAt.IsZero():
		res.TrackedWhy = TrackedWatched
	default:
		res.TrackedWhy = TrackedRead
	}
	return nil
}

// isMember reports whether a configured query's stored result lists id.
func (h *handler) isMember(ctx context.Context, id string) (bool, error) {
	rows, err := h.deps.Store.ListQueries(ctx, true)
	if err != nil {
		return false, err
	}
	for _, q := range rows {
		for _, m := range q.IDs {
			if groups.FoldRef(m) == id {
				return true, nil
			}
		}
	}
	return false, nil
}

// fillGroups gives every group of an issue, stored or not, in canonical order:
// a group never stored is reported with its verdict ("no content stored") and
// its queue state, since a queued first fetch is the usual reason for it.
func (h *handler) fillGroups(ctx context.Context, res *Result, now time.Time) error {
	rows, err := h.deps.Store.ListGroups(ctx, groups.EntityKind, res.ID)
	if err != nil {
		return err
	}
	byName := make(map[string]store.GroupRow, len(rows))
	for _, r := range rows {
		byName[r.Group] = r
	}
	for _, name := range groups.All() {
		row, ok := byName[name]
		if !ok {
			row = store.GroupRow{Group: name}
		}
		f := h.deps.Rules.Fresh(row, groupkit.Stamps{}, now)
		g := GroupExplain{
			Group:         name,
			FetchedAt:     utc(row.FetchedAt),
			FreshUntil:    utc(row.FreshUntil),
			MaxAgeAt:      utc(row.MaxAgeAt),
			Fresh:         f.IsFresh(),
			FreshReason:   f.Reason,
			LastError:     row.LastError,
			QueuePosition: -1,
		}
		if pos, queued := h.deps.Scheduler.Position(res.ID, name); queued {
			g.QueuePosition = pos
		}
		_, g.Queued, g.Running = h.deps.Scheduler.Inspect(res.ID, name)
		res.Groups = append(res.Groups, g)
	}
	return nil
}

func (h *handler) fillPending(ctx context.Context, res *Result) error {
	rows, err := h.deps.Store.ListPending(ctx)
	if err != nil {
		return err
	}
	for _, p := range rows {
		if p.Kind != changes.LogKindIssue || p.ID != res.ID {
			continue
		}
		aw, err := engine.DecodeAwaited(p.Awaited)
		if err != nil {
			return fmt.Errorf("pending row %s/%s: %w", p.Kind, p.ID, err)
		}
		pe := &PendingExplain{Awaited: make([]string, 0, len(aw)), Deadline: utc(p.Deadline)}
		for _, a := range aw {
			pe.Awaited = append(pe.Awaited, a.Group)
		}
		res.Pending = pe
		return nil
	}
	return nil
}

func (h *handler) fillChanges(ctx context.Context, res *Result) error {
	rows, err := h.deps.Store.LastChanges(ctx, changes.LogKindIssue, res.ID, MaxLastChanges)
	if err != nil {
		return err
	}
	if len(rows) > MaxLastChanges {
		rows = rows[:MaxLastChanges]
	}
	for _, c := range rows {
		res.LastChanges = append(res.LastChanges, ChangeExplain{Seq: c.Seq, Kinds: c.Kinds, At: utc(c.At), Origin: c.Origin})
	}
	return nil
}

// utc is t in UTC at whole-second precision, the zero time unchanged.
func utc(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(time.Second)
}
