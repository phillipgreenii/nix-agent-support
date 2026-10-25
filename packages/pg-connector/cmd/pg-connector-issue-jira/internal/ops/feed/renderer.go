package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feedop"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// rendererStore is the part of the daemon store the renderer reads.
type rendererStore interface {
	GetEntity(ctx context.Context, kind, id string) (store.EntityRow, bool, error)
	GetGroup(ctx context.Context, kind, id, group string) (store.GroupRow, bool, error)
}

var _ rendererStore = (*store.Store)(nil)

type renderer struct{ st rendererStore }

// NewRenderer returns the renderer of a delivered row's entity: the
// summary-level issue as the stored summary says now.
func NewRenderer(st *store.Store) feedop.Renderer { return renderer{st: st} }

// Row renders the entity of a delivered row: Version is the row's OWN version.
// A removed row of an entity that is gone renders the last stored summary, or a
// minimal entity carrying only the id when none is left.
func (r renderer) Row(ctx context.Context, row store.ChangeRow) (json.RawMessage, error) {
	return r.render(ctx, row.ID, row.Version)
}

// Replay renders the entity of a reset replay entry from its wire reference,
// with the entity row's current version (0 when there is no row).
func (r renderer) Replay(ctx context.Context, ref string) (json.RawMessage, error) {
	id := groups.FoldRef(ref)
	var version int64
	ent, ok, err := r.st.GetEntity(ctx, groups.EntityKind, id)
	if err != nil {
		return nil, err
	}
	if ok {
		version = ent.Version
	}
	return r.render(ctx, ref, version)
}

func (r renderer) render(ctx context.Context, ref string, version int64) (json.RawMessage, error) {
	id := groups.FoldRef(ref)
	g, found, err := r.st.GetGroup(ctx, groups.EntityKind, id, groups.Summary)
	if err != nil {
		return nil, err
	}
	var issue schema.Issue
	stored := false
	if found && len(g.Content) > 0 {
		var c groups.SummaryContent
		if err := json.Unmarshal(g.Content, &c); err != nil {
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("decode stored summary of %s: %v", id, err))
		}
		if c.Issue.ID != "" {
			issue, stored = c.Issue, true
		}
	}
	if stored {
		issue.AsOf, issue.Stale = "", false
		if !g.FetchedAt.IsZero() {
			issue.AsOf = g.FetchedAt.UTC().Format(time.RFC3339)
		} else {
			issue.Stale = true
		}
	} else {
		issue = schema.Issue{ID: id, Stale: true}
	}
	issue.Version = version
	return json.Marshal(issue)
}
