package summary

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Store is the part of the daemon store Persister uses; *store.Store satisfies
// it.
type Store interface {
	GetEntity(ctx context.Context, kind, id string) (store.EntityRow, bool, error)
	PutEntity(ctx context.Context, e store.EntityRow) error
	GetGroup(ctx context.Context, kind, id, group string) (store.GroupRow, bool, error)
	PutGroup(ctx context.Context, g store.GroupRow) error
}

var _ Store = (*store.Store)(nil)

// Persister stores fetched summaries.
type Persister struct {
	Store    Store
	Identity *entity.Identity
	// TTL is summary_ttl: how long a stored summary stays fresh.
	TTL time.Duration
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Stored is what Persist did with one summary.
type Stored struct {
	// Summary is what was stored and what the wire returns: its Mergeable is the
	// carried-forward value (entity.Carry), not the observed one.
	Summary Summary
	// Fingerprint is Summary's fingerprint; PrevFingerprint is the one stored
	// before ("" for a first sighting).
	Fingerprint     string
	PrevFingerprint string
	// First is true when the entity had no summary group before.
	First bool
	// MergeabilityChanged is true when mergeability moved from one known value to
	// a different known value (UNKNOWN in between or not), PrevMergeable to
	// Summary.Mergeable. A later phase turns it into a change row.
	MergeabilityChanged bool
	PrevMergeable       string
	// Rename is set when the alias held a different reference for this node id:
	// the repository was renamed or transferred. The alias has been updated.
	Rename *entity.Update
}

// Persist stores one fetched summary as the entity's `summary` group, together
// with its entity row, and updates the alias.
//
// It applies the mergeability rule against the last known value carried in the
// entity row: the head and base the previous summary was fetched for decide
// whether that value still counts, and the carried value replaces the observed
// one before the fingerprint is taken. The group row records the head and base
// this summary describes (fetched_for_head, fetched_for_base), fetched_at as the
// clock's now, and fresh_until as now plus TTL.
//
// When no previous summary row exists the carried value is not trusted (there is
// no head and base to check it against), so the first summary after a missing
// row starts the carry afresh and cannot report a change.
//
// A Gone summary is not stored: it returns a Stored with Summary.Gone and
// nothing else. The entity row keeps its version and keep-alive flag; version
// bumps belong to the change log.
func (p *Persister) Persist(ctx context.Context, s Summary) (Stored, error) {
	if s.Gone {
		return Stored{Summary: s}, nil
	}
	now := p.now()

	prevGroup, hadGroup, err := p.Store.GetGroup(ctx, entity.KindPR, s.NodeID, GroupName)
	if err != nil {
		return Stored{}, err
	}
	row, hadRow, err := p.Store.GetEntity(ctx, entity.KindPR, s.NodeID)
	if err != nil {
		return Stored{}, err
	}
	if !hadRow {
		row = store.EntityRow{Kind: entity.KindPR, ID: s.NodeID}
	}

	prevMergeable := ""
	headChanged, baseChanged := false, false
	if hadGroup {
		prevMergeable = row.LastKnownMergeable
		headChanged = prevGroup.FetchedForHead != s.HeadRefOid
		baseChanged = prevGroup.FetchedForBase != s.BaseRefOid
	}
	carried, changed := entity.Carry(prevMergeable, s.Mergeable, headChanged, baseChanged)
	s.Mergeable = carried

	content, err := json.Marshal(s)
	if err != nil {
		return Stored{}, fmt.Errorf("summary: encode %s: %w", s.NodeID, err)
	}
	st := Stored{
		Summary: s, Fingerprint: s.Fingerprint(), First: !hadGroup,
		MergeabilityChanged: changed,
	}
	if hadGroup {
		st.PrevFingerprint = prevGroup.Fingerprint
	}
	if changed {
		st.PrevMergeable = prevMergeable
	}

	row.LastKnownMergeable = ""
	if entity.Known(carried) {
		row.LastKnownMergeable = carried
	}
	if err := p.Store.PutEntity(ctx, row); err != nil {
		return Stored{}, err
	}
	group := prevGroup
	if !hadGroup {
		group = store.GroupRow{Kind: entity.KindPR, ID: s.NodeID, Group: GroupName}
	}
	group.Content, group.Fingerprint = content, st.Fingerprint
	group.FetchedAt, group.FreshUntil = now, now.Add(p.TTL)
	group.LastError = ""
	group.Generation++
	group.FetchedForHead, group.FetchedForBase = s.HeadRefOid, s.BaseRefOid
	if err := p.Store.PutGroup(ctx, group); err != nil {
		return Stored{}, err
	}

	ref, err := entity.NewRef(s.Repo, s.Number)
	if err != nil {
		return Stored{}, fmt.Errorf("summary: %s: %w", s.NodeID, err)
	}
	up, err := p.Identity.Put(ctx, s.NodeID, ref)
	if err != nil {
		return Stored{}, err
	}
	if up.Renamed {
		st.Rename = &up
	}
	return st, nil
}

func (p *Persister) now() time.Time {
	if p.Clock == nil {
		return time.Now()
	}
	return p.Clock.Now()
}
