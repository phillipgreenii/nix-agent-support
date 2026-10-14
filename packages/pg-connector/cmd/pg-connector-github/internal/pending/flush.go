package pending

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/identity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
)

// Names of the two flushes RegisterFlushes registers with the identity tracker.
const (
	FlushPendingGroups = "pending-groups"
	FlushSidecarView   = "sidecar-view"
)

// FlushStore is the part of the store the pending-groups flush needs.
// *store.Store satisfies it.
type FlushStore interface {
	ListEntities(ctx context.Context, kind string) ([]store.EntityRow, error)
	DeleteGroup(ctx context.Context, kind, id, group string) error
}

var _ FlushStore = (*store.Store)(nil)

// Clock is the time source of a SidecarView and of the flushes; identity.Clock
// satisfies it.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SidecarLoader reads one PR's posted-review sidecar; posted.Store satisfies it.
type SidecarLoader interface {
	Load(owner, repo string, pr int) (posted.State, error)
}

var _ SidecarLoader = posted.Store{}

// SidecarView is the daemon's view of the posted-review sidecar for the acting
// identity. The sidecar is per PR and carries no viewer, so after an identity
// change the entries recorded before the change are not reported: Last hides a
// last_append whose at is earlier than the time of the last identity change.
// The sidecar FILES are the single source of truth (the old binary shares them)
// and are never modified or deleted by this type or by a flush.
type SidecarView struct {
	// Store reads the sidecar files.
	Store SidecarLoader

	mu      sync.Mutex
	changed time.Time // zero until an identity change is marked
}

// MarkIdentityChanged records that the acting identity changed at at; a
// last_append recorded before it is no longer reported.
func (v *SidecarView) MarkIdentityChanged(at time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.changed = at
}

// Last returns the sidecar's last_append for repo (owner/name) and number, or
// nil when there is none, when the sidecar is missing, unconfigured or
// unreadable (local bookkeeping with no bearing on whether a review is
// pending), or when the append was recorded before the last identity change. An
// append whose time cannot be read is hidden once an identity change has been
// marked, because it cannot be shown to belong to the current identity.
func (v *SidecarView) Last(repo string, number int) *pr.LastAppend {
	if v == nil || v.Store == nil {
		return nil
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil
	}
	st, err := v.Store.Load(owner, name, number)
	if err != nil || st.LastAppend == nil {
		return nil
	}
	v.mu.Lock()
	changed := v.changed
	v.mu.Unlock()
	if !changed.IsZero() {
		at, err := time.Parse(time.RFC3339, st.LastAppend.At)
		if err != nil || at.Before(changed) {
			return nil
		}
	}
	return &pr.LastAppend{At: st.LastAppend.At, Added: st.LastAppend.Added, Head: st.LastAppend.Head}
}

// RegisterFlushes registers the two flushes of the pending group's
// identity-scoped data with the tracker: FlushPendingGroups deletes every stored
// pending row of kind pr (the content belongs to the previous identity and MUST
// NOT be served stale), and FlushSidecarView marks the identity change on view
// at the time the flush runs. The tracker retries a failed flush on its next
// refresh. A nil clock means the system clock; a nil view registers only the
// first flush.
func RegisterFlushes(t *identity.Tracker, st FlushStore, view *SidecarView, clock Clock) {
	if clock == nil {
		clock = systemClock{}
	}
	t.RegisterFlush(FlushPendingGroups, FlushPendingGroupsFunc(st))
	if view != nil {
		t.RegisterFlush(FlushSidecarView, func(context.Context) error {
			view.MarkIdentityChanged(clock.Now())
			return nil
		})
	}
}

// FlushPendingGroupsFunc returns the flush that deletes every stored pending
// group of every PR and leaves every other group.
func FlushPendingGroupsFunc(st FlushStore) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		ents, err := st.ListEntities(ctx, entity.KindPR)
		if err != nil {
			return fmt.Errorf("list entities: %w", err)
		}
		for _, e := range ents {
			if err := st.DeleteGroup(ctx, entity.KindPR, e.ID, groups.Pending); err != nil {
				return fmt.Errorf("delete pending group of %s: %w", e.ID, err)
			}
		}
		return nil
	}
}
