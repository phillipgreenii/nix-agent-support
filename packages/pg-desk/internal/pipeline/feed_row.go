package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const (
	// Query-membership kinds a backend MAY emit; an owning backend keeps no
	// per-query membership, so they are never stored as change rows.
	feedKindEnteredQuery = "entered_query"
	feedKindLeftQuery    = "left_query"

	// feedKindClosed and feedKindRemoved make a feed row terminal.
	feedKindClosed  = string(classify.KindClosed)
	feedKindRemoved = string(classify.KindRemoved)

	// feedReplayOrigin labels the change row of a RESET replay row.
	feedReplayOrigin = "reset"
)

// FeedRowInput is one feed row of an owning backend instance. Query is the
// query the row was read under ("" when the instance was read without one);
// StoreIdentity is the identity of the backend store the row's Seq belongs
// to. A Row with Seq == 0 is a RESET replay row.
type FeedRowInput struct {
	Instance, Query, StoreIdentity string
	Row                            gather.FeedRow
}

// FeedRowResult tells the caller what IngestFeedRow did with one row.
type FeedRowResult struct {
	// Skipped: the row was a redelivery (marker or failed-ingest entry);
	// nothing was written.
	Skipped bool
	// Written: the entity row (and the marker) was committed.
	Written bool
	// Degraded: the hydration answer was degraded; nothing was written and the
	// failure was recorded (Failed is also true).
	Degraded bool
	// Failed: the row could not be committed and was recorded in the
	// failed-ingest table; the caller acknowledges past it.
	Failed bool
	// Version is the new entity version when Written, else 0.
	Version int64
}

// feedCommit is one row to commit, from the feed or from the failed-ingest
// table.
type feedCommit struct {
	instance string
	entityID string
	kinds    []string
	origin   string
	marker   *store.FeedMarker // nil: write no marker (replay rows, retries)
	replay   bool
}

// feedOutcome is the result of commitFeedRow. A non-empty failure means
// nothing was written and the row must be recorded as failed.
type feedOutcome struct {
	written  bool
	version  int64
	failure  string
	degraded bool
}

// IngestFeedRow commits ONE feed row of an owning backend. It hydrates the
// entity with a fresh read pinned to in.Instance, writes the snapshot and a
// change row carrying the row's OWN kinds (the daemon's classification, stored
// as given, never re-classified), mirrors entity_meta.watched into the watched
// column in the same write, and advances the feed marker in the same
// transaction. A redelivered row is skipped. A hydration failure or a
// degraded answer records one failed-ingest entry and returns Failed with a
// nil error, so the caller keeps polling and acknowledges past the row; an
// error is returned only for a store or infrastructure fault.
func (p *Pipeline) IngestFeedRow(ctx context.Context, in FeedRowInput) (FeedRowResult, error) {
	if err := p.store.RequireNewSchema(); err != nil {
		return FeedRowResult{}, err
	}
	row := in.Row
	if in.Instance == "" || row.EntityID == "" {
		return FeedRowResult{}, fmt.Errorf("pipeline: feed row needs an instance and an entity id (instance=%q entity=%q)", in.Instance, row.EntityID)
	}
	replay := row.Seq == 0
	if !replay {
		skip, err := p.store.ShouldSkipFeedRow(in.Instance, in.StoreIdentity, row.Seq, row.EntityID)
		if err != nil {
			return FeedRowResult{}, fmt.Errorf("pipeline: feed row %s %s#%d: %w", in.Instance, row.EntityID, row.Seq, err)
		}
		if skip {
			return FeedRowResult{Skipped: true}, nil
		}
	}
	c := feedCommit{
		instance: in.Instance,
		entityID: row.EntityID,
		kinds:    feedRowKinds(row.Kinds, replay),
		origin:   defaultEntityChangeOrigin,
		replay:   replay,
	}
	if replay {
		c.origin = feedReplayOrigin
	} else {
		c.marker = &store.FeedMarker{Instance: in.Instance, StoreIdentity: in.StoreIdentity, Seq: row.Seq}
	}
	out, err := p.commitFeedRow(ctx, c)
	if err != nil {
		return FeedRowResult{}, err
	}
	if out.failure != "" {
		if err := p.store.RecordIngestFailure(store.IngestFailure{
			Instance: in.Instance, EntityID: row.EntityID, Seq: row.Seq,
			StoreIdentity: in.StoreIdentity, Kinds: c.kinds, LastError: out.failure,
		}); err != nil {
			return FeedRowResult{}, fmt.Errorf("pipeline: record ingest failure %s %s#%d: %w", in.Instance, row.EntityID, row.Seq, err)
		}
		return FeedRowResult{Failed: true, Degraded: out.degraded}, nil
	}
	return FeedRowResult{Written: out.written, Version: out.version}, nil
}

// RetryIngestFailures re-hydrates every failed-ingest entry of instance ("" =
// every instance), at every poll with no counter and no deferral. An entry
// that now hydrates is written with its RECORDED kinds as given (no marker,
// no skip check) and cleared; an entry that still fails keeps its first
// failure time and gets the new error. retried is the number of entries
// attempted and stillFailing how many of those failed again.
func (p *Pipeline) RetryIngestFailures(ctx context.Context, instance string) (retried, stillFailing int, err error) {
	if err := p.store.RequireNewSchema(); err != nil {
		return 0, 0, err
	}
	fails, err := p.store.ListIngestFailures(instance)
	if err != nil {
		return 0, 0, fmt.Errorf("pipeline: list ingest failures: %w", err)
	}
	for _, f := range fails {
		replay := f.Seq == 0
		c := feedCommit{
			instance: f.Instance, entityID: f.EntityID,
			kinds: feedRowKinds(f.Kinds, replay), origin: defaultEntityChangeOrigin, replay: replay,
		}
		if replay {
			c.origin = feedReplayOrigin
		}
		retried++
		out, err := p.commitFeedRow(ctx, c)
		if err != nil {
			return retried, stillFailing, err
		}
		if out.failure != "" {
			f.LastError = out.failure
			if err := p.store.RecordIngestFailure(f); err != nil {
				return retried, stillFailing, fmt.Errorf("pipeline: record ingest failure %s %s#%d: %w", f.Instance, f.EntityID, f.Seq, err)
			}
			stillFailing++
			continue
		}
		if err := p.store.ClearIngestFailure(f.Instance, f.EntityID, f.Seq); err != nil {
			return retried, stillFailing, fmt.Errorf("pipeline: clear ingest failure %s %s#%d: %w", f.Instance, f.EntityID, f.Seq, err)
		}
	}
	return retried, stillFailing, nil
}

// feedRowKinds returns the kinds stored for a row: the row's own kinds with
// the query-membership kinds dropped, or ["added"] for a replay row (the
// daemon sends replay rows with empty kinds). nil when nothing is left.
func feedRowKinds(kinds []string, replay bool) []string {
	if replay {
		return []string{string(classify.KindAdded)}
	}
	var out []string
	for _, k := range kinds {
		if k == feedKindEnteredQuery || k == feedKindLeftQuery || k == "" {
			continue
		}
		out = append(out, k)
	}
	return out
}

// commitFeedRow hydrates and commits one row. Only a store or infrastructure
// fault (or a cancelled context) is returned as an error; every hydration
// problem is reported through feedOutcome.failure.
func (p *Pipeline) commitFeedRow(ctx context.Context, c feedCommit) (feedOutcome, error) {
	ge, ok := p.entityGatherers[issueEntityType]
	if !ok {
		return feedOutcome{}, fmt.Errorf("pipeline: no gather adapter registered for entity type %q", issueEntityType)
	}
	ie, ok := interpret.EntityInterpreters()[issueEntityType]
	if !ok {
		return feedOutcome{}, fmt.Errorf("pipeline: no interpret adapter registered for entity type %q", issueEntityType)
	}
	repo := p.repo()
	now := p.clock.Now().UTC()
	at := now.Format(time.RFC3339)

	var (
		result gather.GatherResult
		err    error
	)
	if bg, ok := ge.(gather.BackendEntityGatherer); ok {
		result, err = bg.GatherEntityOn(ctx, c.entityID, c.instance, "")
	} else {
		result, err = ge.GatherEntity(ctx, c.entityID, "")
	}
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return feedOutcome{}, fmt.Errorf("pipeline: feed row %s %s: %w", c.instance, c.entityID, cerr)
		}
		reason := "hydration failed: " + err.Error()
		if errors.Is(err, gather.ErrUnavailable) {
			reason = "hydration unavailable: " + err.Error()
		}
		return feedOutcome{failure: reason}, nil
	}

	removedRow := slices.Contains(c.kinds, feedKindRemoved)
	active := !removedRow && !slices.Contains(c.kinds, feedKindClosed)
	t := true
	opts := store.WriteOptions{
		Backend: &c.instance, Watched: &t, ClearWatchedAfterAppend: !active, FeedMarker: c.marker,
	}

	if result.RemovedState == "not_found" {
		if !removedRow {
			return feedOutcome{failure: "hydration failed: issue not found"}, nil
		}
		return p.commitRemovedFeedRow(c, repo, at, opts)
	}
	if result.Degraded != "" {
		return feedOutcome{failure: "degraded answer: " + result.Degraded, degraded: true}, nil
	}
	// A stated entity_meta.watched of false is mirrored AFTER the append, like
	// a terminal row; NOT STATED counts as watched (a delivered feed row
	// exists only for a watched issue).
	if result.Watched != nil && !*result.Watched {
		opts.ClearWatchedAfterAppend = true
	}

	interp, err := ie(result, p.clock, p.cfg)
	if err != nil {
		return feedOutcome{failure: "interpret failed: " + err.Error()}, nil
	}
	interpRow, err := p.interpretationRow(issueEntityType, c.entityID, interp)
	if err != nil {
		return feedOutcome{failure: "interpret failed: " + err.Error()}, nil
	}
	factsJSON, headSHA := factsAndHead(result.Payload)
	asOf := result.AsOf
	if asOf == "" {
		asOf = interp.AsOf
	}
	hash := contentHash(issueCompareBytes(factsJSON))

	// The new derived link set, diffed once against the current one.
	newLinks, haveLinks, err := p.deriveLinks(issueEntityType, c.entityID, result.Payload, len(result.Payload) == 0, at)
	if err != nil {
		return feedOutcome{failure: "links failed: " + err.Error()}, nil
	}
	var linkChanges []classify.LinkChange
	if haveLinks {
		current, err := p.store.ListXrefLinksFrom(repo, issueEntityType, c.entityID)
		if err != nil {
			return feedOutcome{}, fmt.Errorf("pipeline: links %s %s: %w", issueEntityType, c.entityID, err)
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

	version, cur, found, exhausted, err := p.feedCAS(ctx, repo, c, at, active, opts, func(store.Entity, bool) (store.Entity, string) {
		return store.Entity{
			Repo: repo, EntityType: issueEntityType, EntityID: c.entityID,
			Facts: string(factsJSON), AsOf: asOf, ContentHash: hash, HeadSHA: headSHA,
		}, at
	})
	if err != nil {
		return feedOutcome{}, err
	}
	if exhausted {
		return feedOutcome{failure: "still losing the version race"}, nil
	}

	// A reset replay is a first observation: nothing propagates from it.
	observed := !c.replay && found && !cur.Inactive && !(cur.Version == 0 && cur.HydratedAt == "")
	propagate := false
	for _, k := range c.kinds {
		if k != string(classify.KindReconcile) {
			propagate = true
		}
	}
	var otherLinks []classify.Targeted
	if observed {
		for _, tg := range classify.LinkRecords(linkChanges, own) {
			if tg.EntityType != issueEntityType || tg.EntityID != c.entityID {
				otherLinks = append(otherLinks, tg)
			}
		}
	}
	if err := p.finishEntityChange(repo, issueEntityType, c.entityID, postCommit{
		observed: observed, propagate: propagate, otherLinks: otherLinks, own: own, at: at,
		interp: interpRow, haveLinks: haveLinks, newLinks: newLinks,
	}); err != nil {
		return feedOutcome{}, err
	}
	return feedOutcome{written: true, version: version}, nil
}

// commitRemovedFeedRow commits a removed row whose pinned read answered not
// found: the last stored facts stay (no snapshot rewrite), the entity is set
// inactive with watched cleared, and the marker advances in the same write.
// An entity that is not held creates nothing; the marker still advances.
func (p *Pipeline) commitRemovedFeedRow(c feedCommit, repo, at string, opts store.WriteOptions) (feedOutcome, error) {
	const retries = defaultEntityChangeRetries
	for attempt := 0; ; attempt++ {
		cur, found, err := p.store.GetEntity(repo, issueEntityType, c.entityID)
		if err != nil {
			return feedOutcome{}, fmt.Errorf("pipeline: read %s %s: %w", issueEntityType, c.entityID, err)
		}
		if !found {
			if c.marker != nil {
				if err := p.store.AdvanceFeedMarker(*c.marker); err != nil {
					return feedOutcome{}, fmt.Errorf("pipeline: advance feed marker %s: %w", c.instance, err)
				}
			}
			return feedOutcome{}, nil
		}
		hydratedAt := cur.HydratedAt
		if hydratedAt == "" {
			hydratedAt = at
		}
		v, err := p.store.WriteEntityStateWithLogOpts(store.Entity{
			Repo: repo, EntityType: issueEntityType, EntityID: c.entityID,
			Facts: cur.Facts, AsOf: cur.AsOf, ContentHash: cur.ContentHash, HeadSHA: cur.HeadSHA,
		}, cur.Version, hydratedAt, false, nil, c.kinds, c.origin, at, opts)
		if err == nil {
			own := classify.NewStoreWorkLookup(p.store, repo)
			observed := !c.replay && !cur.Inactive && !(cur.Version == 0 && cur.HydratedAt == "")
			if err := p.appendLocalRecords(repo, issueEntityType, c.entityID, observed, len(c.kinds) > 0, nil, own, at); err != nil {
				return feedOutcome{}, err
			}
			return feedOutcome{written: true, version: v}, nil
		}
		if !errors.Is(err, store.ErrVersionConflict) {
			return feedOutcome{}, fmt.Errorf("pipeline: write %s %s: %w", issueEntityType, c.entityID, err)
		}
		if attempt >= retries {
			return feedOutcome{failure: "still losing the version race"}, nil
		}
	}
}

// feedCAS is the compare-and-set write loop of one feed row: it reads the
// entity's current version, builds the entity to write from the current row
// (mk returns the entity and its hydrated_at) and retries a lost race up to
// the existing bound. It returns the new version, the row read for the
// winning attempt and whether the retries ran out.
func (p *Pipeline) feedCAS(ctx context.Context, repo string, c feedCommit, at string, active bool, opts store.WriteOptions, mk func(cur store.Entity, found bool) (store.Entity, string)) (version int64, cur store.Entity, found, exhausted bool, err error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, cur, found, false, fmt.Errorf("pipeline: feed row %s %s: %w", c.instance, c.entityID, err)
		}
		cur, found, err = p.store.GetEntity(repo, issueEntityType, c.entityID)
		if err != nil {
			return 0, cur, found, false, fmt.Errorf("pipeline: read %s %s: %w", issueEntityType, c.entityID, err)
		}
		var expected int64
		if found {
			expected = cur.Version
		}
		e, hydratedAt := mk(cur, found)
		v, werr := p.store.WriteEntityStateWithLogOpts(e, expected, hydratedAt, active, nil, c.kinds, c.origin, at, opts)
		if werr == nil {
			return v, cur, found, false, nil
		}
		if !errors.Is(werr, store.ErrVersionConflict) {
			return 0, cur, found, false, fmt.Errorf("pipeline: write %s %s: %w", issueEntityType, c.entityID, werr)
		}
		if attempt >= defaultEntityChangeRetries {
			return 0, cur, found, true, nil
		}
	}
}
