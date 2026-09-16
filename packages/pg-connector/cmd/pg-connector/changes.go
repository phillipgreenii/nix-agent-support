// changes.go: the "pg-connector <type> changes" verb — the umbrella-facing
// CLI surface for this docket's delta ledger (ledger.go), wired as a
// subcommand of each type's own new<Type>Cmd() exactly as "list" already
// is (pr.go's newPrCmd, issue.go's newIssueCmd), never as a fourth
// top-level command [design: section 5.1, 5.3, 5.5].
//
// SCOPE NOTE (thread): this packet's own Contract text (and the design's
// section 4.2, "list --query therefore applies to pr, issue, and
// thread") names three types for "changes." But "thread" has no CLI
// surface, no schema.Thread type, and no connector.thread registry entry
// ANYWHERE in this module today — the design's own phasing note (section
// 8's acceptance-criteria table: "All items are phase 7 except the
// thread items, which land in phase 13 with the Slack backend") confirms
// Thread is deliberately deferred to a later phase. There is accordingly
// no new<Type>Cmd() for "thread" to attach a changes verb to, and this
// packet's own Files section lists no thread.go/schema.Thread work
// either. This file therefore implements changes for pr and issue only —
// matching what "list" itself actually covers TODAY (list.go's own
// header comment: "ci and scm never get a list verb" — thread isn't
// mentioned there either, since it doesn't exist yet) — a contract-drift
// finding recorded in this packet's own closeout, not silently invented
// around.
//
// Algorithm (per backend, one independent Ledger per (type, backend,
// query) — ledger.go's LedgerKey):
//
//  1. Load that backend's ledger. If --reset, ResetConsumer(consumerID)
//     FIRST (design: section 5.1/5.3) — every subsequent step in this
//     call sees the zeroed cursor.
//  2. Unless --cached: call Ledger.Refresh against this backend's "list"
//     op (forwarding the ledger's own stored opaque cursor — this is the
//     one caller in this module that ever supplies a non-null cursor to
//     "list"; see this file's own update to docs/behavior/interfaces.md).
//     On success, Evict(false) then save. On a query_not_recognized
//     failure, Evict(true) (rule 3 — wipes and deletes the file itself;
//     no separate save). On any OTHER failure, per Refresh's own
//     contract and design section 5.2 ("a backend that answers
//     unavailable or any error leaves its file untouched for that
//     refresh"), do neither Evict nor save, and do not advance this
//     backend's consumer cursor. --cached skips this entire step,
//     including Evict ("no eviction on a cache-only read").
//  3. Compute this call's reported changes via mergeChanges (below),
//     combining ChangesSince's catch-up list with this pass's own
//     Refresh-classified full-body changes.
//  4. Write the combined (all backends) response to stdout and flush.
//  5. ONLY THEN (design: section 5.3, "the cursor advances only after
//     the verb's own output is fully written and flushed"): for every
//     backend that reached step 2's success or --cached path,
//     AdvanceConsumer(consumerID, now) and save again. A crash between
//     step 4 and this step leaves the consumer's cursor exactly where it
//     was before this call, so the NEXT call's ChangesSince recomputes
//     and reports the same entries again — a duplicate, never a loss.
//
// Phase 14 (bead pg2-2j5ac.42.2, docket pg2-2j5ac.42) adds two further
// edits to this file, both scoped to the umbrella entity cache
// (cache.go/cache_dispatch.go), never to the ledger above:
//
//   - mergeChanges's own "since" loop's ChangeRemoved branch (step 3,
//     the ONLY place a ChangeRemoved changesEntry is ever built) now
//     looks up that id in the same backend's entity cache first: a live
//     (non-tombstoned), within-max-age copy there is used as Entity
//     INSTEAD OF idOnlyEntity(id) — "a removed change carries the last
//     content" [design of record section 5.6]. Purely a READ, never a
//     cache write — mergeChanges runs before step 4's write+flush.
//   - a new step 6, run alongside step 5 (order between the two is
//     immaterial — they touch disjoint on-disk files): for every id this
//     call's own response reported as ChangeRemoved, tombstone that id
//     in its backend's own entity cache (Cache.Remove — idempotent) and
//     evict, so a later show/list cache-fallback read stops offering a
//     removed entity's stale content once its removal has actually been
//     reported. See commitCacheTombstones's own doc comment.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// ensureLedgerDirExists creates the ledger directory if it does not
// exist yet. loadLedger (ledger.go, out of this packet's own scope)
// flocks a sibling lock file that it expects to be able to CREATE, which
// requires the parent directory to already exist — true once ANY
// saveLedger call has ever run (saveLedger's own os.MkdirAll), but not
// yet true on a host's very first "changes" call before any ledger has
// ever been saved [bug found via this packet's own "on a fresh ledger"
// acceptance criterion]. Establishing this precondition here, once, at
// this packet's own CLI entry point keeps the fix entirely on this
// packet's side of the seam rather than touching ledger.go's own
// locking code (this docket's sibling packet's file).
func ensureLedgerDirExists() error {
	dir, err := ledgerDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// rawListResult mirrors schema.PRListResult/schema.IssueListResult's wire
// shape but keeps each entity as raw JSON rather than decoding into a
// typed schema.PR/schema.Issue: changes.go's own implementation is
// entity-type-agnostic (one implementation shared by pr and issue via
// newChangesCmd below), and Ledger.Refresh/canonicalHash/entityID already
// operate on json.RawMessage throughout — decoding into a typed struct
// and re-marshaling would risk losing fields canonicalHash needs to hash
// faithfully, for no benefit.
type rawListResult struct {
	Entities   []json.RawMessage `json:"entities"`
	PresentIDs []string          `json:"present_ids"`
	Cursor     json.RawMessage   `json:"cursor"`
	Truncated  bool              `json:"truncated"`
}

// changesEntry is one item in the wire response's changes[] array
// (Produces section's illustrative shape:
// {"change": "added", "source": "...", "entity": {}}).
type changesEntry struct {
	Change ChangeKind      `json:"change"`
	Source string          `json:"source"`
	Entity json.RawMessage `json:"entity"`
}

// changesSourceRow is one item in the wire response's sources[] array —
// deliberately NOT outcome.go's shared SourceResult shape: this packet's
// own Contract adapts the design's illustrative example verbatim
// ({"backend", "status", "version", "truncated"}), a different field set
// than SourceResult's {"source", "status", "count", "reason"}. The
// EXIT-CODE/query_not_recognized CLASSIFICATION logic is still reused
// unchanged from list.go (via outcomeSources below feeding
// listExitCode/allQueryNotRecognized) — only this wire ROW SHAPE differs.
type changesSourceRow struct {
	Backend   string       `json:"backend"`
	Status    SourceStatus `json:"status"`
	Version   int64        `json:"version"`
	Truncated bool         `json:"truncated"`
}

type changesWire struct {
	Sources []changesSourceRow `json:"sources"`
	Changes []changesEntry     `json:"changes"`
}

// changesBackendResult is one backend's own outcome within a "changes"
// fan-out call — enough state to build both the wire response
// (changesWire) and, after that response is written and flushed, to
// commit this backend's own consumer-cursor advance (commitAdvances).
type changesBackendResult struct {
	backend     string
	key         LedgerKey
	ledger      *Ledger // nil when this backend's own ledger could not even be loaded
	status      SourceStatus
	reason      string
	truncated   bool
	skipAdvance bool // true: step 5 (advance+save) must not run for this backend
	entries     []changesEntry
}

// newChangesCmd builds the "changes" cobra command for entityType (pr or
// issue), shared by newPrChangesCmd/newIssueChangesCmd below —
// entity-type-agnostic per this file's own header comment.
func newChangesCmd(entityType string) *cobra.Command {
	var query, consumer string
	var cached, reset bool
	cmd := &cobra.Command{
		Use:   "changes",
		Short: fmt.Sprintf("Report %s entities changed since a consumer's last call, via the delta ledger", entityType),
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered "+entityType+" backend")
	cmd.Flags().StringVar(&query, "query", "", "named query to run, resolved against each backend's own config.queries (required)")
	cmd.Flags().StringVar(&consumer, "consumer", "", "consumer id whose cursor this call reads and advances (required)")
	cmd.Flags().BoolVar(&cached, "cached", false, "skip the backend refresh entirely; return only what the ledger already knows past the cursor")
	cmd.Flags().BoolVar(&reset, "reset", false, "reset this consumer's cursor to zero before computing changes, so the next call replays every live entity as added")
	_ = cmd.MarkFlagRequired("query")
	_ = cmd.MarkFlagRequired("consumer")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return err
		}
		if err := ensureLedgerDirExists(); err != nil {
			return err
		}
		// Phase 14 (bead pg2-2j5ac.42.2): mergeChanges's own ChangeRemoved
		// branch and this RunE's own post-flush commitCacheTombstones step
		// (below) both call loadCache, which needs the cache directory to
		// already exist for the same reason ensureLedgerDirExists exists
		// for the ledger above — establish it here too.
		if err := ensureCacheDirExists(); err != nil {
			return err
		}
		backends, err := resolveListBackends(reg, entityType, *backendFlag)
		if err != nil {
			return err
		}
		results := fanOutChanges(cmd.Context(), reg, entityType, backends, query, consumer, cached, reset)
		outcomeSources := changesOutcomeSources(results)
		if allQueryNotRecognized(outcomeSources) {
			return writeTargetedResult(cmd, nil, listQueryNotRecognizedErr(entityType, query), func(json.RawMessage) (string, error) { return "", nil })
		}
		wire := changesWireFor(results)
		exitCode := listExitCode(outcomeSources)
		werr := writeFanOutResult(cmd, wire, exitCode, func() string { return humanizeChangesOutcome(wire) })
		// Step 5 (design: section 5.3) runs regardless of exitCode/werr —
		// the response above has already been written and flushed by the
		// time we get here; a save failure here is a more severe,
		// unexpected internal failure than an ordinary degraded/failed
		// fan-out outcome, so it takes priority over werr if both occur.
		if commitErr := commitAdvances(results, consumer); commitErr != nil {
			return commitErr
		}
		// Step 6 (this file's own header comment, phase 14): tombstone
		// every id this call's own response reported as ChangeRemoved in
		// its backend's own entity cache. Same post-flush position as
		// step 5 above; order between the two is immaterial (they touch
		// disjoint on-disk files).
		if commitErr := commitCacheTombstones(reg, entityType, results); commitErr != nil {
			return commitErr
		}
		return werr
	}
	return cmd
}

// fanOutChanges runs this packet's per-backend changes algorithm (this
// file's own header comment, steps 1-3) across every backend in
// backends, one independent Ledger per backend.
func fanOutChanges(ctx context.Context, reg *Registry, entityType string, backends []string, query, consumerID string, cached, reset bool) []changesBackendResult {
	pruneAfter := resolveConsumerPruneAfter(reg)
	results := make([]changesBackendResult, 0, len(backends))
	for _, b := range backends {
		key := LedgerKey{Type: entityType, Backend: b, Query: query}
		res := changesBackendResult{backend: b, key: key}

		l, err := loadLedger(key)
		if err != nil {
			res.status = SourceDegraded
			res.reason = err.Error()
			res.skipAdvance = true
			results = append(results, res)
			continue
		}
		if reset {
			l.ResetConsumer(consumerID)
		}
		// Captured AFTER any --reset, so 0 on both a genuinely fresh
		// ledger and a just-reset consumer — mergeChanges below treats
		// those two cases identically, by design.
		preCursor := l.Consumers[consumerID].Cursor

		var refreshChanges []LedgerChange
		if cached {
			res.status = SourceSucceeded
		} else {
			var truncated bool
			listFn := changesListFn(ctx, reg, b, query, &truncated)
			var refreshErr error
			refreshChanges, refreshErr = l.Refresh(listFn)
			if refreshErr != nil {
				sr := classifyListSource(b, refreshErr)
				res.status = sr.Status
				res.reason = sr.Reason
				res.skipAdvance = true
				if errors.Is(refreshErr, scriptout.ErrQueryNotRecognized) {
					// Rule 3: Evict itself deletes the on-disk file: no
					// separate saveLedger call (ledger.go's own Evict doc
					// comment — saveLedger would just recreate what Evict
					// just removed).
					l.Evict(key, time.Now(), pruneAfter, true)
				}
				// Any other error (including "this backend doesn't
				// implement list at all"): design section 5.2, "leaves
				// its file untouched for that refresh" — no Evict, no
				// save, no advance.
				results = append(results, res)
				continue
			}
			l.Evict(key, time.Now(), pruneAfter, false)
			if err := saveLedger(key, l); err != nil {
				res.status = SourceDegraded
				res.reason = err.Error()
				res.skipAdvance = true
				results = append(results, res)
				continue
			}
			res.truncated = truncated
			res.status = SourceSucceeded
		}

		res.entries = mergeChanges(b, l, consumerID, preCursor, refreshChanges, entityType, reg)
		res.ledger = l
		results = append(results, res)
	}
	return results
}

// changesListFn builds a Ledger.Refresh listFn that invokes backend's own
// "list" op for query, forwarding cursor as-is (the ledger's own stored
// opaque cursor blob) — the one caller in this module that ever supplies
// a non-null cursor to "list" (see this packet's own update to
// docs/behavior/interfaces.md's "list" section). ids_only is always
// false: changes needs full entity bodies to report a freshly-added
// entity's content (mergeChanges below). *lastTruncated is set from this
// call's own result so the caller can report it on the response's
// sources[] row.
func changesListFn(ctx context.Context, reg *Registry, backend, query string, lastTruncated *bool) func(json.RawMessage) ([]json.RawMessage, []string, json.RawMessage, bool, error) {
	return func(cursor json.RawMessage) ([]json.RawMessage, []string, json.RawMessage, bool, error) {
		resp, err := invokeOne(ctx, reg, backend, "list", map[string]any{"query": query, "cursor": cursor, "ids_only": false})
		if err != nil {
			return nil, nil, nil, false, err
		}
		var result rawListResult
		if err := scriptout.Decode(resp.Result, &result); err != nil {
			return nil, nil, nil, false, err
		}
		*lastTruncated = result.Truncated
		return result.Entities, result.PresentIDs, result.Cursor, result.Truncated, nil
	}
}

// mergeChanges computes one backend's own contribution to the response's
// changes[] array for consumerID, combining l.ChangesSince(consumerID)
// (the general catch-up list — everything past preCursor, however many
// refreshes ago it happened) with refreshChanges (this pass's OWN
// Refresh-computed classification, which alone carries full entity
// bodies and correctly distinguishes ChangeAdded from ChangeChanged;
// ledger.go's own ChangesSince doc comment: "ChangesSince has no full
// entity body to offer ... degrades to the same {"id": ...} envelope"
// for every catch-up entry, added or changed alike).
//
// At preCursor == 0 — a genuinely fresh ledger, OR a consumer just reset
// by --reset — this additionally reinterprets ChangesSince's raw output:
//   - every live catch-up entry is reported as ChangeAdded, never
//     ChangeChanged: a cursor-0 consumer has by definition never seen
//     anything before, so nothing shown to it now is a "change" to prior
//     knowledge it never had. This is what makes the acceptance
//     criterion "on a fresh ledger reports every matching entity as
//     added" hold for entries ChangesSince alone would otherwise label
//     "changed" (ChangesSince has no "added" concept of its own — see
//     its doc comment above), and, symmetrically, what makes "--reset
//     reports every live entity as added again" hold too.
//   - every removed/tombstoned entry is DROPPED entirely, never reported:
//     telling a consumer that never knew an entity existed that it was
//     "removed" is meaningless. This is what makes the acceptance
//     criterion "--reset ... with no removed tombstone for anything that
//     was actually removed before the reset" hold.
//
// Neither reinterpretation modifies ledger.go's own ChangesSince (out of
// this packet's scope, per its own Contract) — both are this packet's own
// CLI-layer synthesis of the wire response, squarely within the
// "implementer's choice" freedom this packet's Contract leaves for how
// the response is assembled.
//
// entityType/reg (added by phase 14, bead pg2-2j5ac.42.2) are used SOLELY
// by the ChangeRemoved branch below, to look up a removed id in that
// backend's own entity cache — READ-ONLY, no cache write of any kind
// happens in this function (see this file's own header comment for why).
func mergeChanges(backend string, l *Ledger, consumerID string, preCursor int64, refreshChanges []LedgerChange, entityType string, reg *Registry) []changesEntry {
	full := make(map[string]LedgerChange, len(refreshChanges))
	for _, c := range refreshChanges {
		if c.Change == ChangeRemoved {
			continue // Refresh's own removed entries are already id-only; nothing extra to offer over ChangesSince's own removed rows.
		}
		if id, err := entityID(c.Entity); err == nil {
			full[id] = c
		}
	}

	since := l.ChangesSince(consumerID)
	out := make([]changesEntry, 0, len(since))
	for _, c := range since {
		id, err := entityID(c.Entity)
		if err != nil {
			continue
		}
		if fc, ok := full[id]; ok {
			// This pass's own Refresh call classified id itself — use its
			// precise Change kind and full entity body.
			out = append(out, changesEntry{Change: fc.Change, Source: backend, Entity: fc.Entity})
			continue
		}
		if c.Change == ChangeRemoved {
			if preCursor == 0 {
				continue // never seen it; a removal is meaningless to report.
			}
			// "A removed change carries the last content" [design of
			// record section 5.6]: if this backend's own entity cache
			// still holds a live (non-tombstoned), within-max-age copy of
			// id, use it instead of c.Entity's idOnlyEntity(id) fallback.
			// cacheEnabled is deliberately NOT checked here — Get alone
			// already behaves correctly for an opted-out type/backend
			// (nothing was ever Put for it, so Get naturally reports no
			// entry, and the idOnlyEntity fallback applies exactly as it
			// would for any other cache miss).
			entity := c.Entity
			if cache, err := loadCache(CacheKey{Type: entityType, Backend: backend}); err == nil {
				if content, _, ok := cache.Get(id, resolveCacheMaxAge(reg), time.Now()); ok {
					entity = content
				}
			}
			out = append(out, changesEntry{Change: ChangeRemoved, Source: backend, Entity: entity})
			continue
		}
		kind := c.Change // ChangeChanged, per ChangesSince's own contract.
		if preCursor == 0 {
			kind = ChangeAdded
		}
		out = append(out, changesEntry{Change: kind, Source: backend, Entity: c.Entity})
	}
	return out
}

// commitAdvances is step 5 (this file's own header comment): called only
// AFTER the response has been written and flushed, it advances and
// persists every backend's own consumer cursor that reached a state
// where advancing is meaningful (skipAdvance is false and this backend's
// ledger was actually loaded).
func commitAdvances(results []changesBackendResult, consumerID string) error {
	now := time.Now()
	for _, r := range results {
		if r.skipAdvance || r.ledger == nil {
			continue
		}
		r.ledger.AdvanceConsumer(consumerID, now)
		if err := saveLedger(r.key, r.ledger); err != nil {
			return fmt.Errorf("changes: advance consumer %q for backend %q: %w", consumerID, r.backend, err)
		}
	}
	return nil
}

// commitCacheTombstones is this file's own step 6 (this file's header
// comment, phase 14/bead pg2-2j5ac.42.2): for every id THIS call's own
// response reported as ChangeRemoved for backend b, tombstone that id in
// b's own entity cache (Cache.Remove — idempotent, so a repeat report
// during a lagging consumer's catch-up is a no-op, never restarting the
// retention clock) and evict, so a later show/list cache-fallback read
// stops offering a removed entity's stale content once its removal has
// actually been reported [design: docket design field, "Produces"].
// consumersPassed is built per backend from that backend's own
// already-loaded *Ledger (results[i].ledger, set only on fanOutChanges'
// own success path — the same skip condition commitAdvances above uses),
// matching Ledger.Evict's rule 1 ("vacuously true when zero consumers
// remain"). Called only AFTER the response has been written and flushed
// (same call site as commitAdvances) — never before, per this docket's
// own cache-write ordering rule.
func commitCacheTombstones(reg *Registry, entityType string, results []changesBackendResult) error {
	now := time.Now()
	for _, r := range results {
		if r.skipAdvance || r.ledger == nil {
			continue
		}
		var removedIDs []string
		for _, e := range r.entries {
			if e.Change != ChangeRemoved {
				continue
			}
			if id, err := entityID(e.Entity); err == nil {
				removedIDs = append(removedIDs, id)
			}
		}
		if len(removedIDs) == 0 {
			continue
		}

		key := CacheKey{Type: entityType, Backend: r.backend}
		c, err := loadCache(key)
		if err != nil {
			return fmt.Errorf("changes: load cache for backend %q: %w", r.backend, err)
		}
		for _, id := range removedIDs {
			c.Remove(id, now)
		}

		ledger := r.ledger
		consumersPassed := func(id string) bool {
			entry, ok := ledger.Entries[id]
			if !ok || entry.RemovedAtVersion == nil {
				return false
			}
			for _, cs := range ledger.Consumers {
				if cs.Cursor < *entry.RemovedAtVersion {
					return false
				}
			}
			return true
		}
		c.Evict(resolveCacheSizeCap(reg), cacheTombstoneRetention, consumersPassed, now)
		if err := saveCache(key, c); err != nil {
			return fmt.Errorf("changes: save cache for backend %q: %w", r.backend, err)
		}
	}
	return nil
}

// changesOutcomeSources adapts results into the shared outcome.go
// SourceResult shape SOLELY so listExitCode/allQueryNotRecognized (both
// reused unchanged from list.go, per this packet's own Contract) can
// compute the exit code and the query_not_recognized carve-out — never
// written to the wire response itself (changesWireFor's changesSourceRow
// is the wire shape).
func changesOutcomeSources(results []changesBackendResult) []SourceResult {
	out := make([]SourceResult, 0, len(results))
	for _, r := range results {
		out = append(out, SourceResult{Source: r.backend, Status: r.status, Count: len(r.entries), Reason: r.reason})
	}
	return out
}

func changesWireFor(results []changesBackendResult) changesWire {
	w := changesWire{Sources: make([]changesSourceRow, 0, len(results)), Changes: make([]changesEntry, 0)}
	for _, r := range results {
		var version int64
		if r.ledger != nil {
			version = r.ledger.Version
		}
		w.Sources = append(w.Sources, changesSourceRow{Backend: r.backend, Status: r.status, Version: version, Truncated: r.truncated})
		w.Changes = append(w.Changes, r.entries...)
	}
	return w
}

// newPrChangesCmd/newIssueChangesCmd are the two new<Type>ChangesCmd()
// constructors this packet's own Contract names, each attached from that
// type's own new<Type>Cmd() (pr.go's newPrCmd, issue.go's newIssueCmd) —
// see this file's own header comment for why there is no
// newThreadChangesCmd.
func newPrChangesCmd() *cobra.Command    { return newChangesCmd("pr") }
func newIssueChangesCmd() *cobra.Command { return newChangesCmd("issue") }

// humanizeChangesOutcome formats a "changes" fan-out outcome for human
// display, mirroring humanizePRListOutcome's own shape.
func humanizeChangesOutcome(w changesWire) string {
	var b strings.Builder
	if len(w.Changes) == 0 {
		b.WriteString("changes: (none)\n")
	} else {
		fmt.Fprintf(&b, "changes (%d):\n", len(w.Changes))
		for _, c := range w.Changes {
			id, err := entityID(c.Entity)
			if err != nil {
				id = "?"
			}
			fmt.Fprintf(&b, "  %s %s (from %s)\n", c.Change, id, c.Source)
		}
	}
	b.WriteString("sources:\n")
	for i, s := range w.Sources {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "  %s: %s version=%d truncated=%t", s.Backend, s.Status, s.Version, s.Truncated)
	}
	return strings.TrimRight(b.String(), "\n")
}
