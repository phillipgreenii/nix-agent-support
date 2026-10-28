package changes

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// FeedConsumer is the consumer name pg-desk reads an owning backend's change
// feed under at the connector.
const FeedConsumer = "pg-desk"

// feedLockType is the consumer-lock type of the feed lock. It is distinct from
// the entity type the router consumer's own deliver lock uses, so the two are
// never nested on one key.
const feedLockType = "issue-feed"

// Meta keys the feed step records for the doctor and the alert packets.
const (
	// MetaFeedObservedPrefix + <instance>.<query> holds the RFC3339
	// last_success_at of that query's sources_freshness row.
	MetaFeedObservedPrefix = "change_flow.feed_observed."
	// MetaFeedCursorExpiredPrefix + <instance> holds the RFC3339 time of the
	// latest cursor_expired: answer. The poll never deletes it.
	MetaFeedCursorExpiredPrefix = "change_flow.feed_cursor_expired."
)

// ReasonFeedUnavailable is the source reason of an owning backend whose
// daemon answered unavailable.
const ReasonFeedUnavailable = "unavailable"

// Feeder reads and acknowledges an owning backend's change feed.
// *gather.Gatherer satisfies it.
type Feeder interface {
	ListFeed(ctx context.Context, entityType, query, consumer, backend string, reset bool) (gather.FeedResult, error)
	AckFeed(ctx context.Context, entityType, query, consumer, backend string, nextSeq int64) (ackedSeq int64, err error)
}

// Ingester commits feed rows. *pipeline.Pipeline satisfies it.
type Ingester interface {
	IngestFeedRow(ctx context.Context, in pipeline.FeedRowInput) (pipeline.FeedRowResult, error)
	RetryIngestFailures(ctx context.Context, instance string) (retried, stillFailing int, err error)
}

// feedInstance is one owning backend instance and the selected owning queries
// served by it.
type feedInstance struct {
	name    string
	queries []string
}

// feedOutcome is one instance's feed step result: reasons are the degraded
// reasons of its queries; failed is true when no answer could be read at all
// (it counts as a failed source in the total-failure test).
type feedOutcome struct {
	inst    feedInstance
	reasons []string
	failed  bool
}

// splitQueries partitions the selected queries into those listed and diffed
// (every query of a type but issue; the non-owning ones of issue) and the
// owning ones read from their backend's feed.
func (e *Engine) splitQueries(entityType string, selected []string) (listed, owning []string) {
	if entityType != "issue" {
		return selected, nil
	}
	for _, q := range selected {
		if b, _ := e.Cfg.WatchQueryBackend(entityType, q); b.OwnsChanges {
			owning = append(owning, q)
		} else {
			listed = append(listed, q)
		}
	}
	return listed, owning
}

// feedInstances groups the selected owning queries by backend instance, sorted
// by instance name.
func (e *Engine) feedInstances(entityType string, owning []string) []feedInstance {
	byInst := map[string]*feedInstance{}
	for _, q := range owning {
		b, _ := e.Cfg.WatchQueryBackend(entityType, q)
		fi := byInst[b.Instance]
		if fi == nil {
			fi = &feedInstance{name: b.Instance}
			byInst[b.Instance] = fi
		}
		fi.queries = append(fi.queries, q)
	}
	names := make([]string, 0, len(byInst))
	for n := range byInst {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]feedInstance, 0, len(names))
	for _, n := range names {
		out = append(out, *byInst[n])
	}
	return out
}

// runFeed is the feed step: one pass over every owning instance under the feed
// lock. The lock is taken once, before the first instance, and released after
// the last acknowledgement; it is NOT held while the router consumer's own
// deliver lock is taken (phase D runs after this returns). The step has its
// own hydration budget.
func (e *Engine) runFeed(ctx context.Context, entityType string, insts []feedInstance, st *pollState) ([]feedOutcome, error) {
	if e.Feeder == nil || e.Ingester == nil {
		return nil, fmt.Errorf("changes: an owning backend is configured but the engine has no feed reader or ingester")
	}
	unlock, err := e.Store.LockConsumer(feedLockType, FeedConsumer)
	if err != nil {
		return nil, fmt.Errorf("changes: lock feed consumer %s: %w", FeedConsumer, err)
	}
	defer unlock()

	bud := &budget{left: e.Cfg.HydrationMaxPerPoll()}
	out := make([]feedOutcome, 0, len(insts))
	for _, inst := range insts {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("changes: %w", err)
		}
		o, err := e.feedStep(ctx, entityType, inst, bud, st)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// feedSourceOf picks the sources[] row of the instance: the row naming it, or
// the only row when the answer has exactly one.
func feedSourceOf(res gather.FeedResult, instance string) (gather.FeedSource, bool) {
	for _, s := range res.Sources {
		if s.Backend == instance {
			return s, true
		}
	}
	if len(res.Sources) == 1 {
		return res.Sources[0], true
	}
	return gather.FeedSource{}, false
}

// feedStep reads, commits and acknowledges one owning instance's feed with an
// EMPTY query (the daemon keeps one cursor per consumer and rejects a second
// query scope). An error is returned only for a store or infrastructure
// fault; a read that fails is a degraded outcome and commits and acknowledges
// nothing.
func (e *Engine) feedStep(ctx context.Context, entityType string, inst feedInstance, bud *budget, st *pollState) (feedOutcome, error) {
	o := feedOutcome{inst: inst}
	fail := func(reason string) feedOutcome {
		o.failed = true
		o.reasons = appendUnique(o.reasons, reason)
		return o
	}

	// The failed-ingest table first, level-triggered, before any new row.
	retried, still, err := e.Ingester.RetryIngestFailures(ctx, inst.name)
	if err != nil {
		return o, fmt.Errorf("changes: retry failed ingests of %s: %w", inst.name, err)
	}
	if still > 0 {
		st.side = append(st.side, fmt.Sprintf("feed %s: %d of %d failed-ingest entries still failing", inst.name, still, retried))
	}

	ans, err := e.Feeder.ListFeed(ctx, entityType, "", FeedConsumer, inst.name, false)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return o, fmt.Errorf("changes: %w", cerr)
		}
		return fail(feedReadReason("feed read", err)), nil
	}
	src, ok := feedSourceOf(ans, inst.name)
	if !ok {
		return fail(fmt.Sprintf("feed read: no source answered for backend %s", inst.name)), nil
	}

	replay := false
	switch {
	case src.CursorExpired:
		// A dropped key is a gap for the sole detector: record it (never
		// deleted by the poll), reset the marker and re-read ONCE with reset
		// true. The instance is degraded for THIS poll only.
		if err := e.Store.SetMeta(MetaFeedCursorExpiredPrefix+inst.name, e.now().UTC().Format(time.RFC3339)); err != nil {
			return o, fmt.Errorf("changes: record feed cursor expiry of %s: %w", inst.name, err)
		}
		if err := e.Store.ResetFeedMarker(inst.name); err != nil {
			return o, fmt.Errorf("changes: reset feed marker of %s: %w", inst.name, err)
		}
		o.reasons = appendUnique(o.reasons, firstErrLine(sourceReason(src)))
		ans, err = e.Feeder.ListFeed(ctx, entityType, "", FeedConsumer, inst.name, true)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return o, fmt.Errorf("changes: %w", cerr)
			}
			return fail(feedReadReason("feed re-read after cursor_expired", err)), nil
		}
		src, ok = feedSourceOf(ans, inst.name)
		if !ok {
			return fail(fmt.Sprintf("feed re-read: no source answered for backend %s", inst.name)), nil
		}
		if src.CursorExpired {
			o.reasons = appendUnique(o.reasons, firstErrLine(sourceReason(src)))
			return o, nil
		}
		replay = true
	default:
		m, found, err := e.Store.FeedMarker(inst.name)
		if err != nil {
			return o, fmt.Errorf("changes: read feed marker of %s: %w", inst.name, err)
		}
		if found && m.StoreIdentity != src.StoreIdentity {
			// A different store restarts the sequence: reset before any row of
			// this answer is processed.
			if err := e.Store.ResetFeedMarker(inst.name); err != nil {
				return o, fmt.Errorf("changes: reset feed marker of %s: %w", inst.name, err)
			}
		}
	}

	healthy := src.Status == "succeeded" || src.Status == "disabled"
	if !healthy {
		o.reasons = appendUnique(o.reasons, firstErrLine(sourceReason(src)))
	} else {
		e.recordFeedObserved(inst, ans)
	}

	rows := append([]gather.FeedRow(nil), ans.Rows...)
	if !replay {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })
	}
	lastSeq, processed, cut := int64(0), 0, false
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return o, fmt.Errorf("changes: %w", err)
		}
		// A reset read is a replay: every returned row is processed whatever
		// the budget.
		if !replay && bud.left <= 0 {
			cut = true
			break
		}
		res, err := e.Ingester.IngestFeedRow(ctx, pipeline.FeedRowInput{
			Instance: inst.name, StoreIdentity: src.StoreIdentity, Row: row,
		})
		if err != nil {
			return o, fmt.Errorf("changes: ingest feed row %s %s#%d: %w", inst.name, row.EntityID, row.Seq, err)
		}
		if !res.Skipped && !replay {
			bud.left--
		}
		if res.Failed {
			st.side = append(st.side, fmt.Sprintf("feed %s %s#%d: hydration failed (recorded, retried next poll)", inst.name, row.EntityID, row.Seq))
		}
		processed++
		lastSeq = row.Seq
	}
	if cut {
		o.reasons = appendUnique(o.reasons, ReasonHydrationBudget)
	}

	// Acknowledge only after the commit: the scanned next position once the
	// whole batch is processed (an empty poll too), the last processed row's
	// seq when the budget cut the batch; never for a degraded source.
	if !healthy {
		return o, nil
	}
	var ack *int64
	switch {
	case cut && processed > 0:
		ack = &lastSeq
	case cut:
		// Nothing was processed: nothing is acknowledged.
	default:
		ack = src.NextSeq
	}
	if ack != nil {
		if _, err := e.Feeder.AckFeed(ctx, entityType, "", FeedConsumer, inst.name, *ack); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return o, fmt.Errorf("changes: %w", cerr)
			}
			// Non-fatal: the feed marker makes the redelivery harmless.
			o.reasons = appendUnique(o.reasons, feedReadReason("feed acknowledge", err))
		}
	}
	return o, nil
}

// recordFeedObserved writes one change_flow.feed_observed.<instance>.<query>
// key per sources_freshness row of an owning query of the instance that has a
// last_success_at. Best-effort: a write error is a warning.
func (e *Engine) recordFeedObserved(inst feedInstance, ans gather.FeedResult) {
	owned := map[string]bool{}
	for _, q := range e.Cfg.OwningWatchQueries("issue") {
		if b, _ := e.Cfg.WatchQueryBackend("issue", q); b.Instance == inst.name {
			owned[q] = true
		}
	}
	for _, f := range ans.Freshness {
		if !owned[f.Query] || f.LastSuccessAt == "" {
			continue
		}
		if err := e.Store.SetMeta(MetaFeedObservedPrefix+inst.name+"."+f.Query, f.LastSuccessAt); err != nil {
			e.warnf("changes: record feed observation %s.%s: %v\n", inst.name, f.Query, err)
		}
	}
}

// feedSources reports each selected owning query as a Source: ok, or degraded
// with the instance's reasons. It never reports membership.
func (e *Engine) feedSources(entityType string, owning []string, outcomes []feedOutcome) []Source {
	byInst := map[string]feedOutcome{}
	for _, o := range outcomes {
		byInst[o.inst.name] = o
	}
	out := make([]Source, 0, len(owning))
	for _, q := range owning {
		b, _ := e.Cfg.WatchQueryBackend(entityType, q)
		o := byInst[b.Instance]
		if len(o.reasons) == 0 {
			out = append(out, Source{Query: q, Status: StatusOK})
			continue
		}
		out = append(out, Source{Query: q, Status: StatusDegraded, Reason: strings.Join(o.reasons, "; ")})
	}
	return out
}

func sourceReason(s gather.FeedSource) string {
	if s.Reason != "" {
		return s.Reason
	}
	return fmt.Sprintf("%s %s", s.Backend, s.Status)
}

func feedReadReason(what string, err error) string {
	if isUnavailable(err) {
		return what + ": " + ReasonFeedUnavailable
	}
	return what + ": " + firstErrLine(err.Error())
}

func isUnavailable(err error) bool { return errors.Is(err, gather.ErrUnavailable) }

// resetReplay is phase B (--reset): it replays the active entities of the type
// to the consumer, origin reset. For issue only WATCHED active entities are
// replayed (an unwatched one is skipped, whoever owns it), always as kind
// added: an OWNING entity through IngestFeedRow as a replay row {Seq 0, kinds
// [added]} (no marker write), any other through a hydration whose first
// observation classifies as added. pr and thread replay every active entity as
// before, as reconcile.
func (e *Engine) resetReplay(ctx context.Context, entityType string, st *pollState) error {
	all, err := e.Store.ListEntities()
	if err != nil {
		return fmt.Errorf("changes: list entities: %w", err)
	}
	var targets []store.Entity
	for _, ent := range all {
		if ent.EntityType != entityType || ent.Inactive {
			continue
		}
		// An issue is replayed only while watched: nothing else is observed.
		if entityType == "issue" && (ent.Watched == nil || !*ent.Watched) {
			continue
		}
		targets = append(targets, ent)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].EntityID < targets[j].EntityID })

	owning := map[string]bool{}
	for _, inst := range e.Cfg.OwningInstances(entityType) {
		owning[inst] = true
	}
	var nonOwningWatched map[string]bool
	for _, ent := range targets {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("changes: %w", err)
		}
		id := ent.EntityID
		inst := ""
		if len(owning) > 0 {
			inst = ent.Backend
			if inst == "" {
				// A NULL backend: an id a non-owning watched query holds is
				// non-owning; otherwise the sole owning instance claims a
				// ticket-shaped id.
				if nonOwningWatched == nil {
					nonOwningWatched, _ = WatchedIDs(e.Store, e.Cfg.NonOwningWatchQueries(entityType), entityType, nil)
				}
				if !nonOwningWatched[id] {
					inst, _ = e.Cfg.OwningInstanceForID(entityType, id)
				}
			}
			if !owning[inst] {
				inst = ""
			}
		}
		if inst == "" {
			herr := e.hydrate(ctx, entityType, id, gather.ChangeChanged, OriginReset, true, nil)
			st.hydrated[id] = herr
			if herr != nil {
				st.side = append(st.side, fmt.Sprintf("reset %s: %s", id, firstErrLine(herr.Error())))
			}
			continue
		}
		if ent.Watched == nil || !*ent.Watched {
			continue
		}
		if e.Ingester == nil {
			return fmt.Errorf("changes: an owning backend is configured but the engine has no ingester")
		}
		res, err := e.Ingester.IngestFeedRow(ctx, pipeline.FeedRowInput{
			Instance: inst,
			Row:      gather.FeedRow{Seq: 0, Kinds: []string{"added"}, EntityID: id},
		})
		if err != nil {
			return fmt.Errorf("changes: reset replay %s: %w", id, err)
		}
		if res.Failed {
			herr := errors.New("hydration failed (recorded, retried next poll)")
			st.hydrated[id] = herr
			st.side = append(st.side, fmt.Sprintf("reset %s: %s", id, herr))
			continue
		}
		st.hydrated[id] = nil
	}
	return nil
}
