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
	// InTx runs fn in one store transaction (see store.Store.InTx).
	InTx(ctx context.Context, fn func(tx *store.Tx) error) error
}

var _ Store = (*store.Store)(nil)

// FetchInfo says what a summary was admitted as. The zero value means unknown.
type FetchInfo struct {
	Class   governor.Class
	Purpose governor.Purpose
}

// StoreHook runs inside the store transaction that writes a refreshed summary,
// after the entity and group rows are written, with the summary stored before
// (nil for a first sighting), what Persist computed and how the summary was
// fetched. An error rolls the transaction back, so the summary is not stored,
// and is returned by PersistWith. The hook MUST use only tx: the store's single
// connection is occupied by it.
type StoreHook func(ctx context.Context, tx *store.Tx, prev *Summary, st Stored, info FetchInfo) error

// GoneHook runs inside the store transaction that sets tombstoned_at, and only
// when the entity was visible before. purpose is what the lookup that found the
// node gone was admitted under. An error rolls the transaction back, so the
// entity stays visible. The hook MUST use only tx.
type GoneHook func(ctx context.Context, tx *store.Tx, node string, purpose governor.Purpose) error

// Persister stores fetched summaries.
type Persister struct {
	Store    Store
	Identity *entity.Identity
	// TTL is summary_ttl: how long a stored summary stays fresh.
	TTL time.Duration
	// TerminalTTL is terminal_ttl: how long a stored summary of a merged or
	// closed PR stays fresh. Zero means TTL.
	TerminalTTL time.Duration
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Hook, when set, runs in the same transaction as the content write.
	Hook StoreHook
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
	// Prev is the summary stored before this one; nil for a first sighting (or a
	// stored row whose content does not decode).
	Prev *Summary
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
//
// Persist is PersistWith with the zero FetchInfo.
func (p *Persister) Persist(ctx context.Context, s Summary) (Stored, error) {
	return p.PersistWith(ctx, s, FetchInfo{})
}

// PersistWith is Persist for a summary admitted as info. It reads the previous
// rows, applies the mergeability rule, writes the entity and group rows and
// calls Hook (when set) in ONE store transaction: an error from Hook rolls the
// transaction back, so the summary is not stored, and is returned. The alias
// update runs BEFORE the transaction (it cannot run inside one), so Stored.Rename
// is known to the hook; it is idempotent and describes no content, so it
// survives a rollback.
func (p *Persister) PersistWith(ctx context.Context, s Summary, info FetchInfo) (Stored, error) {
	if s.Gone {
		return Stored{Summary: s}, nil
	}
	now := p.now()

	ref, err := entity.NewRef(s.Repo, s.Number)
	if err != nil {
		return Stored{}, fmt.Errorf("summary: %s: %w", s.NodeID, err)
	}
	up, err := p.Identity.Put(ctx, s.NodeID, ref)
	if err != nil {
		return Stored{}, err
	}

	var st Stored
	err = p.Store.InTx(ctx, func(tx *store.Tx) error {
		var err error
		st, err = p.persistIn(ctx, tx, s, now)
		if err != nil {
			return err
		}
		if up.Renamed {
			u := up
			st.Rename = &u
		}
		if p.Hook != nil {
			return p.Hook(ctx, tx, st.Prev, st, info)
		}
		return nil
	})
	if err != nil {
		return Stored{}, err
	}
	return st, nil
}

// persistIn reads the previous rows and writes the entity and group rows
// through tx.
func (p *Persister) persistIn(ctx context.Context, tx *store.Tx, s Summary, now time.Time) (Stored, error) {
	prevGroup, hadGroup, err := tx.GetGroup(ctx, entity.KindPR, s.NodeID, GroupName)
	if err != nil {
		return Stored{}, err
	}
	row, hadRow, err := tx.GetEntity(ctx, entity.KindPR, s.NodeID)
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
		if len(prevGroup.Content) > 0 {
			var prev Summary
			if json.Unmarshal(prevGroup.Content, &prev) == nil {
				st.Prev = &prev
			}
		}
	}
	if changed {
		st.PrevMergeable = prevMergeable
	}

	row.TombstonedAt = time.Time{} // the PR is visible again
	row.LastKnownMergeable = ""
	if entity.Known(carried) {
		row.LastKnownMergeable = carried
	}
	if err := tx.PutEntity(ctx, row); err != nil {
		return Stored{}, err
	}
	group := prevGroup
	if !hadGroup {
		group = store.GroupRow{Kind: entity.KindPR, ID: s.NodeID, Group: GroupName}
	}
	group.Content, group.Fingerprint = content, st.Fingerprint
	group.FetchedAt, group.FreshUntil = now, now.Add(p.FreshFor(s.State))
	group.LastError = ""
	group.Generation++
	group.FetchedForHead, group.FetchedForBase = s.HeadRefOid, s.BaseRefOid
	if err := tx.PutGroup(ctx, group); err != nil {
		return Stored{}, err
	}
	return st, nil
}

// FreshFor is how long a summary of a PR in the given state stays fresh: the
// terminal_ttl for a merged or closed PR, the summary_ttl otherwise.
func (p *Persister) FreshFor(state string) time.Duration {
	if IsTerminal(state) && p.TerminalTTL > 0 {
		return p.TerminalTTL
	}
	return p.TTL
}

func (p *Persister) now() time.Time {
	if p.Clock == nil {
		return time.Now()
	}
	return p.Clock.Now()
}
