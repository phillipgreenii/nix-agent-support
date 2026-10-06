package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const (
	// defaultEntityChangeRetries is the lost-race retry bound when
	// EntityChangeOptions.MaxRetries is not positive.
	defaultEntityChangeRetries = 5

	// defaultEntityChangeOrigin labels the entity's own change_log row when
	// the caller names no origin.
	defaultEntityChangeOrigin = "pg-connector"

	// localChangeOrigin labels the change_log rows pg-desk itself originates
	// for OTHER entities (link and propagation records).
	localChangeOrigin = "pg-desk"

	// derivedOriginPrefix and legacyDerivedOrigin pick the rows the extractors
	// own out of an entity's links: external and legacy rows are never
	// diffed, so they are never reported as removed.
	derivedOriginPrefix = "derived:"
	legacyDerivedOrigin = "derived:legacy"
)

// EntityChangeOptions tunes one RunEntityChange call.
type EntityChangeOptions struct {
	// Origin is the change_log origin of the entity's own snapshot row
	// (pg-connector, sweep, reset, or the caller's choice); empty means
	// pg-connector. The local-source rows for other entities use pg-desk.
	Origin string
	// ThreadActiveWindow is passed to classify.ThreadResolved; zero means the
	// 7d default.
	ThreadActiveWindow time.Duration
	// MaxRetries bounds the lost-race retries; <= 0 means a small default.
	MaxRetries int
	// ForceReconcile makes the old snapshot count as unobserved even when a
	// row exists (--reset, a new watched query: first observation).
	ForceReconcile bool
	// ListFP, when non-nil, is the list fingerprint observed in THIS tick's
	// listing of the entity; it is written to list_fp in the same transaction
	// as the snapshot, hydrated_at, active and change_log rows, so it commits
	// or rolls back with them. Nil (a read that did not come from a list: the
	// removal confirmation read, --reset, the remote sweep) leaves list_fp
	// untouched. The pipeline never derives it from the hydrated payload.
	ListFP *string

	// beforeWrite, when set, runs once per compare-and-set attempt after the
	// old snapshot is read and before the write (a test seam for a
	// deterministic lost race).
	beforeWrite func(attempt int)
}

// EntityChangeResult tells the caller what one RunEntityChange call did.
type EntityChangeResult struct {
	// Kinds are the kinds appended on the entity's own row (empty when
	// nothing changed).
	Kinds []string
	// Version is the entity's new version (0 when nothing was written).
	Version int64
	// Retries is the number of lost-race retries this call made.
	Retries int
	// Written is whether the entity row was written.
	Written bool
	// Degraded is the gather result's degraded note; non-empty means the read
	// was NOT written and the previous snapshot stands (the entity stays
	// active and due).
	Degraded string
	// NotFound is true when the source reported not_found on a
	// gather.ChangeRemoved call; nothing was written.
	NotFound bool
}

// RunEntityChange is the named pipeline entry point Phase 6's `changes` and
// `refresh` call: it hydrates one entity through the generic per-type
// registries, classifies the previous snapshot against the new one (plus the
// local change sources), and writes the typed snapshot, hydrated_at, active
// and the entity's change_log row in ONE transaction conditional on the
// version it read. On a lost race it re-reads the entity, re-classifies and
// counts the retry. New schema only; it always writes active = true.
//
// A failed read, a degraded read and a not_found read each leave the previous
// snapshot in place and log nothing: every one returns BEFORE any store write.
// A not_found read on a gather.ChangeRemoved call returns NotFound with a nil
// error (removal is the caller's membership decision, never made here).
//
// Step order, pinned so no link_changed is lost: (1) gather and interpret;
// (2) derive the new link set in memory and diff it against the entity's
// current derived links, once, across retries; (3) the compare-and-set loop;
// (4) only after the commit, the other-entity records, the propagation
// records, the interpretation row and, LAST, the derived-link replacement. A
// crash inside (4) MAY duplicate a link_changed on the next hydration (the
// diff is recomputed against the unreplaced set) but never loses one.
//
// RunGenericEntity's non-CAS path is unchanged.
func (p *Pipeline) RunEntityChange(ctx context.Context, entityType, entityID string, change gather.ChangeKind, opts EntityChangeOptions) (EntityChangeResult, error) {
	if err := p.store.RequireNewSchema(); err != nil {
		return EntityChangeResult{}, err
	}
	ge, ok := p.entityGatherers[entityType]
	if !ok {
		return EntityChangeResult{}, fmt.Errorf("pipeline: no gather adapter registered for entity type %q", entityType)
	}
	ie, ok := interpret.EntityInterpreters()[entityType]
	if !ok {
		return EntityChangeResult{}, fmt.Errorf("pipeline: no interpret adapter registered for entity type %q", entityType)
	}

	// (1) gather and interpret: every error and non-write outcome returns
	// before any store write.
	result, err := ge.GatherEntity(ctx, entityID, change)
	if err != nil {
		return EntityChangeResult{}, fmt.Errorf("pipeline: gather %s %s: %w", entityType, entityID, err)
	}
	if result.RemovedState == "not_found" {
		if change != gather.ChangeRemoved {
			return EntityChangeResult{}, fmt.Errorf("pipeline: %s %s: not found", entityType, entityID)
		}
		return EntityChangeResult{NotFound: true}, nil
	}
	if result.Degraded != "" {
		return EntityChangeResult{Degraded: result.Degraded}, nil
	}
	interp, err := ie(result, p.clock, p.cfg)
	if err != nil {
		return EntityChangeResult{}, fmt.Errorf("pipeline: interpret %s %s: %w", entityType, entityID, err)
	}
	interpRow, err := p.interpretationRow(entityType, entityID, interp)
	if err != nil {
		return EntityChangeResult{}, fmt.Errorf("pipeline: interpret %s %s: %w", entityType, entityID, err)
	}

	repo := p.repo()
	now := p.clock.Now().UTC()
	at := now.Format(time.RFC3339)
	factsJSON, headSHA := factsAndHead(result.Payload)
	asOf := result.AsOf
	if asOf == "" {
		asOf = interp.AsOf
	}
	newSnap := classify.Snapshot{
		Type: entityType, ID: entityID, Exists: true, Active: true,
		Payload: factsJSON, AsOf: asOf, ContentHash: contentHash(factsJSON), HeadSHA: headSHA,
	}
	origin := opts.Origin
	if origin == "" {
		origin = defaultEntityChangeOrigin
	}
	maxRetries := opts.MaxRetries
	if maxRetries <= 0 {
		maxRetries = defaultEntityChangeRetries
	}

	// (2) the new derived link set, diffed once against the current one.
	removed := result.RemovedState != "" || len(result.Payload) == 0
	newLinks, haveLinks, err := p.deriveLinks(entityType, entityID, result.Payload, removed, at)
	if err != nil {
		return EntityChangeResult{}, fmt.Errorf("pipeline: links %s %s: %w", entityType, entityID, err)
	}
	var linkChanges []classify.LinkChange
	if haveLinks {
		current, err := p.store.ListXrefLinksFrom(repo, entityType, entityID)
		if err != nil {
			return EntityChangeResult{}, fmt.Errorf("pipeline: links %s %s: %w", entityType, entityID, err)
		}
		var derived []store.XrefLink
		for _, l := range current {
			if strings.HasPrefix(l.Origin, derivedOriginPrefix) && l.Origin != legacyDerivedOrigin {
				derived = append(derived, l)
			}
		}
		linkChanges = classify.DiffLinks(derived, newLinks)
	}
	own := classify.NewStoreWorkLookup(p.store, repo)

	// (3) the compare-and-set loop.
	var (
		res        EntityChangeResult
		observed   bool
		propagate  bool
		otherLinks []classify.Targeted
	)
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("pipeline: %s %s: %w", entityType, entityID, err)
		}
		row, found, err := p.store.GetEntity(repo, entityType, entityID)
		if err != nil {
			return res, fmt.Errorf("pipeline: read %s %s: %w", entityType, entityID, err)
		}
		var expected int64
		old := classify.Snapshot{Type: entityType, ID: entityID}
		if found {
			expected = row.Version
			old.Active = !row.Inactive
			if !opts.ForceReconcile && !(row.Version == 0 && row.HydratedAt == "") {
				old.Exists = true
				old.Payload = []byte(row.Facts)
				old.AsOf, old.ContentHash, old.HeadSHA = row.AsOf, row.ContentHash, row.HeadSHA
			}
		}
		observed = old.Exists && old.Active

		kindSet := map[string]bool{}
		propagate = false
		for _, r := range classify.Classify(old, newSnap) {
			kindSet[string(r.Kind)] = true
			if r.Kind != classify.KindReconcile {
				propagate = true
			}
		}
		otherLinks = nil
		if observed {
			for _, t := range classify.LinkRecords(linkChanges, own) {
				if t.EntityType == entityType && t.EntityID == entityID {
					kindSet[string(t.Kind)] = true
				} else {
					otherLinks = append(otherLinks, t)
				}
			}
			if classify.ThreadResolved(old, newSnap, now, opts.ThreadActiveWindow) {
				kindSet[string(classify.KindResolved)] = true
				propagate = true
			}
		}
		var kinds []string
		for k := range kindSet {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)

		if opts.beforeWrite != nil {
			opts.beforeWrite(attempt)
		}
		v, err := p.store.WriteEntityStateWithLogFP(store.Entity{
			Repo: repo, EntityType: entityType, EntityID: entityID,
			Facts: string(factsJSON), AsOf: asOf, ContentHash: newSnap.ContentHash, HeadSHA: headSHA,
		}, expected, at, true, opts.ListFP, kinds, origin, at)
		if err == nil {
			res.Kinds, res.Version, res.Written = kinds, v, true
			break
		}
		if !errors.Is(err, store.ErrVersionConflict) {
			return res, fmt.Errorf("pipeline: write %s %s: %w", entityType, entityID, err)
		}
		if res.Retries >= maxRetries {
			return res, fmt.Errorf("pipeline: write %s %s: still losing the version race after %d retries: %w", entityType, entityID, res.Retries, err)
		}
		res.Retries++
	}

	// (4) after the commit: local records for other entities, the
	// interpretation, and the derived-link replacement LAST.
	if err := p.appendLocalRecords(repo, entityType, entityID, observed, propagate, otherLinks, own, at); err != nil {
		return res, err
	}
	if err := p.store.UpsertInterpretation(interpRow); err != nil {
		return res, fmt.Errorf("pipeline: persist interpretation %s %s: %w", entityType, entityID, err)
	}
	if haveLinks {
		if err := p.store.ReplaceDerivedXrefs(repo, entityType, entityID, newLinks); err != nil {
			return res, fmt.Errorf("pipeline: links %s %s: %w", entityType, entityID, err)
		}
	}
	return res, nil
}

// appendLocalRecords appends, at origin pg-desk, the link records naming the
// OTHER entity of each changed link and, when the entity's own change was a
// non-reconcile one, the one-hop propagation records for the entities linked
// to it. Each (entity, kind) is appended at most once per call; an entity with
// no row is skipped. An unobserved hydration appends nothing.
func (p *Pipeline) appendLocalRecords(repo, entityType, entityID string, observed, propagate bool, otherLinks []classify.Targeted, own classify.WorkLookup, at string) error {
	if !observed {
		return nil
	}
	targets := otherLinks
	if propagate {
		from, err := p.store.ListXrefLinksFrom(repo, entityType, entityID)
		if err != nil {
			return fmt.Errorf("pipeline: propagate %s %s: %w", entityType, entityID, err)
		}
		to, err := p.store.ListXrefLinksTo(repo, entityType, entityID)
		if err != nil {
			return fmt.Errorf("pipeline: propagate %s %s: %w", entityType, entityID, err)
		}
		targets = append(append([]classify.Targeted(nil), targets...),
			classify.PropagationRecords(entityType, entityID, append(from, to...), own)...)
	}
	type key struct{ t, id, kind string }
	done := map[key]bool{}
	for _, t := range targets {
		k := key{t.EntityType, t.EntityID, string(t.Kind)}
		if done[k] {
			continue
		}
		done[k] = true
		if _, err := p.store.AppendEntityChange(repo, t.EntityType, t.EntityID, []string{string(t.Kind)}, localChangeOrigin, at); err != nil && !errors.Is(err, store.ErrNoEntity) {
			return fmt.Errorf("pipeline: append %s for %s %s: %w", t.Kind, t.EntityType, t.EntityID, err)
		}
	}
	return nil
}
