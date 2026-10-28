package changes

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// An orphan is a watched, active issue of the listed (non-owning) path that no
// watched query lists any longer: a one-off watch, or an entity that left its
// query for an ordinary reason (a deferral, a block, a relabel). It is neither
// removed nor deactivated; it is read by id at each poll, a capped number of
// them oldest last-attempt first, until it closes. This is change detection
// for a watched entity, not a sweep.

// OrphanEntities returns the watched, active issue entities of the listed
// path that no watched query's persisted watch set holds: entities whose
// watched column is true, whose backend column does not name an owning
// instance, and that are absent from every change_flow.watchset.issue.<query>
// set. It only reads the store; the order is the store's.
func OrphanEntities(st *store.Store, cfg *config.Config) ([]store.Entity, error) {
	all, err := st.ListEntities()
	if err != nil {
		return nil, fmt.Errorf("changes: list entities: %w", err)
	}
	owning := OwningSet(cfg, issueEntityType)
	held := map[string]bool{}
	for _, q := range cfg.WatchQueries(issueEntityType) {
		for id := range loadWatchSet(st, issueEntityType, q) {
			held[id] = true
		}
	}
	var out []store.Entity
	for _, ent := range all {
		if ent.EntityType != issueEntityType || ent.Inactive {
			continue
		}
		if ent.Watched == nil || !*ent.Watched {
			continue
		}
		if ent.Backend != "" && owning[ent.Backend] {
			continue
		}
		if held[ent.EntityID] {
			continue
		}
		out = append(out, ent)
	}
	return out, nil
}

// orphanAttemptTime parses an entity's read_attempt_at; never attempted (or
// unparsable) is the zero time, which sorts oldest.
func orphanAttemptTime(ent store.Entity) (time.Time, bool) {
	if ent.ReadAttemptAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ent.ReadAttemptAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// sortOrphans orders orphans oldest last-attempt first: a never-attempted one
// before any attempted, ties by id.
func sortOrphans(orphans []store.Entity) {
	sort.SliceStable(orphans, func(i, j int) bool {
		ti, oki := orphanAttemptTime(orphans[i])
		tj, okj := orphanAttemptTime(orphans[j])
		if oki != okj {
			return !oki
		}
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return orphans[i].EntityID < orphans[j].EntityID
	})
}

// skipUnchangedWrite is the hydration option of an orphan read: an unchanged
// normalised snapshot writes nothing (no row, no version bump).
func skipUnchangedWrite(o *pipeline.EntityChangeOptions) { o.SkipUnchangedWrite = true }

// readOrphans reads, FIRST in phase C and out of the poll's hydration budget,
// at most hydration.orphan_max_per_poll orphans by id, oldest last-attempt
// first. A read compares the normalised snapshot (never list_fp, which stays
// untouched through the nil list fingerprint) and writes nothing but the two
// read times when nothing changed; a changed read appends the classified row.
// After every attempt read_attempt_at is stamped; read_ok_at too when the read
// answered (no error, not degraded, not not_found). A failed, degraded or
// not_found read leaves the entity watched and active (the outcomes of those
// reads belong to the orphan read outcomes, not to this selection). An
// id read is recorded in st.hydrated so later phases skip it. When the budget
// stops an orphan no attempt is made and nothing is written for it.
func (e *Engine) readOrphans(ctx context.Context, opts Options, st *pollState, bud *budget) error {
	if opts.EntityType != issueEntityType {
		return nil
	}
	orphans, err := OrphanEntities(e.Store, e.Cfg)
	if err != nil {
		return err
	}
	sortOrphans(orphans)
	limit := e.Cfg.OrphanMaxPerPoll()
	read := 0
	for _, ent := range orphans {
		if read >= limit {
			break
		}
		id := ent.EntityID
		if _, done := st.hydrated[id]; done { // a --reset replay already read it
			continue
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("changes: %w", err)
		}
		if why := bud.blocked(); why != "" {
			st.noteQueries(nil, why)
			break
		}
		read++
		res, herr := e.hydrateResult(ctx, issueEntityType, id, gather.ChangeRemoved, OriginPGConnector, false, nil, skipUnchangedWrite)
		st.hydrated[id] = herr
		bud.note(herr)
		if herr != nil {
			st.side = append(st.side, fmt.Sprintf("orphan read %s: %s", id, firstErrLine(herr.Error())))
		}
		now := e.now().UTC().Format(time.RFC3339Nano)
		var okAt *string
		if herr == nil && !res.NotFound {
			okAt = &now
		}
		if terr := e.Store.SetEntityReadTimes(e.repo(), issueEntityType, id, &now, okAt); terr != nil && !errors.Is(terr, store.ErrNoEntity) {
			st.side = append(st.side, fmt.Sprintf("orphan read %s: read times: %s", id, firstErrLine(terr.Error())))
		}
	}
	return nil
}
