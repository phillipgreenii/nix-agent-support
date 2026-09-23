// ledger.go: pg-connector's delta ledger — a self-contained internal engine
// tracking the per-(type, backend, query) persisted state (fetch cursor,
// entity hash index, version counter, and per-consumer cursor positions),
// its refresh algorithm, and its three eviction rules [design: section
// 5.2/5.3/5.4]. This file has NO CLI surface of its own — it produces a Go
// API that this docket's sibling "changes/ledger show/ledger clear verbs"
// packet wires into cobra commands.
//
// On-disk layout (freedom boundary — not decided by the design beyond
// "flock on a sibling lock file" and "temp-file-and-rename"): one JSON file
// per LedgerKey under $XDG_STATE_HOME/pg-connector/ledger/ (falling back to
// ~/.local/state/pg-connector/ledger/, matching pg-pr's own DefaultPath
// convention in packages/pg-pr/internal/store/store.go), named
// "<type>__<backend>__<query>.json" — a double-underscore separator, since
// none of type/backend/query's real values contain "__". ListLedgerKeys'
// decode step below depends on matching this encoding exactly; both
// functions are this file's own, kept consistent with each other by
// construction (ledgerFileName/ledgerKeyFromFileName are the one pair of
// functions responsible for it). Concurrency uses flock on a sibling
// "<file>.lock" file plus a temp-file-and-rename write, so two pg-router
// queries and an operator invocation may run concurrently against the same
// ledger file without corrupting it [design: section 5.2].
//
// The ledger holds hashes, not entities — it is derived state; losing or
// clearing it costs one re-emission of every entity as added, never data
// loss [design: section 5.2].
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// LedgerKey identifies one persisted ledger file. Instance additionally
// disambiguates two invocations of the identically-named (Type, Backend,
// Query) triple that nonetheless target two independent underlying data
// sources via a per-invocation environment override (bead pg2-84i8o's root
// cause) — e.g. the "issue" type's pg-connector-issue-beads backend,
// registered ONCE under connector.issue but invoked once per bd tracker
// via $PG_CONNECTOR_ISSUE_BEADS_DIR, with the SAME backend name and query
// for every tracker (modules/zm/default.nix's "Two trackers, one role"
// design, phillipg-nix-ziprecruiter). Without this field, two trackers
// sharing one ledger file meant each tracker's own Refresh call saw the
// OTHER tracker's previously-indexed entries as "removed" (an id absent
// from THIS call's own presentIDs, because it was never this tracker's
// entity to begin with) and reported a spurious cross-tracker change —
// the confirmed root cause of a bead created in one tracker triggering a
// dispatch under the OTHER tracker's own emitted event type. Empty for
// every (Type, Backend, Query) combination that has no such per-invocation
// override (every backend besides pg-connector-issue-beads, today), so
// this is a zero-behavior-change addition for them — see
// ledgerInstanceDiscriminator (changes.go) for where a non-empty value
// comes from.
type LedgerKey struct{ Type, Backend, Query, Instance string }

// ConsumerState is one consumer's position within a ledger.
type ConsumerState struct {
	Cursor   int64     `json:"cursor"`    // version-last-changed the consumer has advanced past
	LastSeen time.Time `json:"last_seen"` // updated on every changes() call by that consumer, per key
}

// LedgerEntry is one entity's tracked state within a ledger.
type LedgerEntry struct {
	Hash               string `json:"hash"`                         // canonical hash, AsOf/Stale excluded
	VersionLastChanged int64  `json:"version_last_changed"`         // version at which this entry last changed
	RemovedAtVersion   *int64 `json:"removed_at_version,omitempty"` // nil unless tombstoned
}

// Ledger is the full persisted state for one (type, backend, query).
type Ledger struct {
	Cursor    json.RawMessage          `json:"cursor,omitempty"` // opaque backend cursor blob; nil means "no cursor yet"
	Entries   map[string]LedgerEntry   `json:"entries"`          // keyed by entity id
	Version   int64                    `json:"version"`          // current version counter
	Consumers map[string]ConsumerState `json:"consumers"`        // keyed by consumer id
}

// ChangeKind mirrors the wire "change" field values.
type ChangeKind string

const (
	ChangeAdded   ChangeKind = "added"
	ChangeChanged ChangeKind = "changed"
	ChangeRemoved ChangeKind = "removed"
)

// LedgerChange is one classified entity change, either freshly computed by
// Refresh (Entity carries the full fetched body for added/changed) or
// reported by ChangesSince (see that method's own doc comment for why its
// added/changed entries necessarily degrade to an id-only Entity too — the
// ledger itself never retains entity bodies, only hashes).
type LedgerChange struct {
	Change ChangeKind
	Entity json.RawMessage // full entity for added/changed; {"id": ...} only for removed
}

// ledgerDirName/ledgerKeySeparator are the two constants ledgerFileName and
// ledgerKeyFromFileName both depend on, kept in exactly one place so the
// encode and decode directions cannot drift from each other.
const (
	ledgerSubdir       = "pg-connector"
	ledgerDirName      = "ledger"
	ledgerKeySeparator = "__"
)

// ledgerDir resolves the directory every ledger file lives under, honouring
// XDG_STATE_HOME with the same fallback pg-pr's own store.DefaultPath uses
// (packages/pg-pr/internal/store/store.go): ~/.local/state/<name>.
func ledgerDir() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, ledgerSubdir, ledgerDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ledger: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", ledgerSubdir, ledgerDirName), nil
}

// ledgerFileName is key's on-disk filename: "<type>__<backend>__<query>.json"
// when key.Instance is empty (every pre-existing key shape, unchanged), or
// "<type>__<backend>__<query>__<hex(instance)>.json" when it is not. The
// instance component is hex-encoded (never the raw value) because Instance
// today carries an arbitrary filesystem path (bead pg2-84i8o's
// $PG_CONNECTOR_ISSUE_BEADS_DIR) that may itself contain "/", which would
// otherwise be misread as a directory separator inside what must stay a
// single flat filename. See this file's header comment for why "__" is a
// safe separator between the OTHER components, none of which can contain
// it.
func ledgerFileName(key LedgerKey) string {
	parts := []string{key.Type, key.Backend, key.Query}
	if key.Instance != "" {
		parts = append(parts, hex.EncodeToString([]byte(key.Instance)))
	}
	return strings.Join(parts, ledgerKeySeparator) + ".json"
}

// ledgerKeyFromFileName decodes a filename produced by ledgerFileName back
// into a LedgerKey. Returns (_, false) for anything that doesn't match the
// exact 3-part "<type>__<backend>__<query>.json" or 4-part
// "<type>__<backend>__<query>__<hex(instance)>.json" shape (e.g. a stray
// non-ledger file dropped into the same directory, or a 4th segment that
// fails to decode as hex) — ListLedgerKeys skips those rather than
// erroring.
func ledgerKeyFromFileName(name string) (LedgerKey, bool) {
	if !strings.HasSuffix(name, ".json") {
		return LedgerKey{}, false
	}
	trimmed := strings.TrimSuffix(name, ".json")
	parts := strings.Split(trimmed, ledgerKeySeparator)
	switch len(parts) {
	case 3:
		return LedgerKey{Type: parts[0], Backend: parts[1], Query: parts[2]}, true
	case 4:
		instance, err := hex.DecodeString(parts[3])
		if err != nil {
			return LedgerKey{}, false
		}
		return LedgerKey{Type: parts[0], Backend: parts[1], Query: parts[2], Instance: string(instance)}, true
	default:
		return LedgerKey{}, false
	}
}

// ledgerPath resolves the on-disk path for key under
// $XDG_STATE_HOME/pg-connector/ledger/ (falling back per the same XDG
// resolution this repo already uses elsewhere for state dirs).
func ledgerPath(key LedgerKey) (string, error) {
	dir, err := ledgerDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ledgerFileName(key)), nil
}

// lockPath is the sibling lock file saveLedger/loadLedger flock on, per
// this file's own header comment's concurrency rule.
func lockPath(path string) string { return path + ".lock" }

// newEmptyLedger returns a zero-value *Ledger with its maps initialized
// non-nil (so callers can index/assign into Entries/Consumers immediately),
// used by loadLedger when no file exists yet.
func newEmptyLedger() *Ledger {
	return &Ledger{Entries: map[string]LedgerEntry{}, Consumers: map[string]ConsumerState{}}
}

// loadLedger reads key's ledger file, returning a zero-value *Ledger
// (Entries/Consumers non-nil, empty) if the file does not exist yet.
func loadLedger(key LedgerKey) (*Ledger, error) {
	path, err := ledgerPath(key)
	if err != nil {
		return nil, err
	}

	fl := flock.New(lockPath(path))
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("ledger: lock %s: %w", path, err)
	}
	defer func() { _ = fl.Unlock() }()

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newEmptyLedger(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: read %s: %w", path, err)
	}

	var l Ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("ledger: decode %s: %w", path, err)
	}
	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}
	if l.Consumers == nil {
		l.Consumers = map[string]ConsumerState{}
	}
	return &l, nil
}

// saveLedger writes l for key via flock-on-sibling-lock-file plus
// temp-file-and-rename (design: section 5.2's concurrency rule).
func saveLedger(key LedgerKey, l *Ledger) error {
	path, err := ledgerPath(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ledger: create ledger dir %s: %w", dir, err)
	}

	fl := flock.New(lockPath(path))
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("ledger: lock %s: %w", path, err)
	}
	defer func() { _ = fl.Unlock() }()

	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("ledger: encode %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".ledger-*.tmp")
	if err != nil {
		return fmt.Errorf("ledger: create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ledger: write temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ledger: close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ledger: rename temp file onto %s: %w", path, err)
	}
	return nil
}

// deleteLedger removes key's on-disk ledger file (and its lock file)
// entirely. Used by Evict rule 3 (design: section 5.4 rule 3, "drop the
// whole ledger... for this key"), and by this docket's sibling
// "changes verbs" packet's `ledger clear` command (design: section 5.1,
// "ledger clear... drops ledger entries... matching the filters").
func deleteLedger(key LedgerKey) error {
	path, err := ledgerPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ledger: remove %s: %w", path, err)
	}
	if err := os.Remove(lockPath(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ledger: remove lock file for %s: %w", path, err)
	}
	return nil
}

// ListLedgerKeys enumerates every persisted (type, backend, query) key
// currently on disk under $XDG_STATE_HOME/pg-connector/ledger/, by scanning
// that directory and decoding each ledger file's name back into a LedgerKey
// via ledgerKeyFromFileName (the same deterministic filename encoding
// ledgerPath/ledgerFileName use to construct it). Used by this docket's
// sibling "changes verbs" packet's `ledger show`/`ledger clear` commands to
// resolve PARTIAL filters: given zero or more of --type/--backend/--query,
// a stored key MATCHES the filter iff every flag that WAS given equals that
// key's corresponding field — an omitted flag matches any value in that
// field. (Example: `--type pr` alone matches every stored key whose Type is
// "pr", regardless of Backend or Query.) An absent ledger directory (no
// ledger has ever been written) returns (nil, nil), not an error.
func ListLedgerKeys() ([]LedgerKey, error) {
	dir, err := ledgerDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: list %s: %w", dir, err)
	}
	var out []LedgerKey
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		key, ok := ledgerKeyFromFileName(e.Name())
		if !ok {
			continue
		}
		out = append(out, key)
	}
	return out, nil
}

// canonicalHash returns a stable hex-encoded SHA-256 digest of entity's
// canonical serialization, with the AsOf and Stale fields excluded (design:
// section 5.2) — the two fields every schema entity type carries that vary
// on every fetch (as-of time) or reflect only whether THIS particular read
// was successful (staleness), neither of which is entity content [binding
// decision: "Canonical hash MUST exclude exactly AsOf and Stale from every
// entity before hashing — no other field is excluded"]. Decoding into
// map[string]json.RawMessage and re-marshaling gives a byte-stable
// serialization independent of the original field order or whitespace:
// encoding/json sorts map[string]... keys alphabetically when marshaling, so
// two structurally-equal entities always hash identically regardless of how
// their source JSON was formatted.
func canonicalHash(entity json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entity, &fields); err != nil {
		return "", fmt.Errorf("ledger: decode entity for hashing: %w", err)
	}
	delete(fields, "as_of")
	delete(fields, "stale")
	data, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("ledger: encode entity for hashing: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// entityID extracts entity's own "id" field — every schema entity type
// carries one (e.g. schema.PR.ID's own doc comment: "carried over as-is
// from pg-pr's existing api.Comment.ID string convention") — used to index
// Ledger.Entries.
func entityID(entity json.RawMessage) (string, error) {
	var fields struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(entity, &fields); err != nil {
		return "", fmt.Errorf("ledger: decode entity id: %w", err)
	}
	if fields.ID == "" {
		return "", fmt.Errorf("ledger: entity has no id field: %s", entity)
	}
	return fields.ID, nil
}

// idOnlyEntity builds the {"id": ...} envelope LedgerChange.Entity carries
// for a removed change (this file's own LedgerChange doc comment: "{"id":
// ...} only for removed").
func idOnlyEntity(id string) json.RawMessage {
	data, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: id})
	if err != nil {
		// Marshaling a plain string field can only fail on an invalid UTF-8
		// id, which entityID's own source (a decoded JSON string) cannot
		// produce — kept as a defensive fallback rather than a panic.
		return json.RawMessage(`{"id":""}`)
	}
	return data
}

// Refresh calls listFn (the backend's list op, signature matching
// registry.go's existing backend-invocation shape) with l's stored cursor.
// Classification uses BOTH return values, never entities alone:
//   - added/changed: computed from `entities` — hash each over the
//     canonical serialization (AsOf/Stale excluded); absent from
//     l.Entries -> added; hash differs from the stored one -> changed.
//   - removed: computed from `presentIDs`, NEVER from `entities` (a
//     cursor-scoped refresh may return only an incremental subset of
//     entities while presentIDs still names the FULL current match set —
//     design: section 4.2's present_ids contract). An indexed id absent
//     from `presentIDs` is removed.
//   - CRITICAL: when `truncated` is true, Refresh MUST NOT mark ANY
//     indexed-but-absent id as removed — leave those LedgerEntry rows
//     exactly as they are. `presentIDs` under `truncated=true` is
//     known-INCOMPLETE (the backend could not enumerate the full match
//     set), so "absent from presentIDs" is not evidence of removal in that
//     call; treating it as such would emit a FALSE removal into every
//     downstream consumer (design: section 5.2's refresh algorithm,
//     "unless truncated, indexed ids absent from present_ids as removed" —
//     the "unless truncated" clause is load-bearing, not decorative).
//
// Bumps l.Version once if anything changed, stores the new cursor, and
// returns the classified changes. On any listFn error (including
// "unavailable"), Refresh returns the error and leaves the ledger file on
// disk UNTOUCHED (caller must not call saveLedger in that path) — design:
// section 5.2, "a backend that answers unavailable or any error leaves its
// file untouched for that refresh." (Refresh itself never touches disk; the
// guarantee holds because it returns before mutating l at all in the error
// path below, so a caller following the contract has nothing to save.)
func (l *Ledger) Refresh(listFn func(cursor json.RawMessage) (entities []json.RawMessage, presentIDs []string, cursorOut json.RawMessage, truncated bool, err error)) ([]LedgerChange, error) {
	entities, presentIDs, cursorOut, truncated, err := listFn(l.Cursor)
	if err != nil {
		return nil, err
	}

	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}

	type hashedEntity struct {
		id     string
		hash   string
		entity json.RawMessage
	}
	hashed := make([]hashedEntity, 0, len(entities))
	for _, e := range entities {
		id, err := entityID(e)
		if err != nil {
			return nil, err
		}
		hash, err := canonicalHash(e)
		if err != nil {
			return nil, err
		}
		hashed = append(hashed, hashedEntity{id: id, hash: hash, entity: e})
	}

	present := make(map[string]bool, len(presentIDs))
	for _, id := range presentIDs {
		present[id] = true
	}

	changed := false
	nextVersion := l.Version + 1
	var changes []LedgerChange

	for _, h := range hashed {
		existing, ok := l.Entries[h.id]
		switch {
		case !ok:
			changed = true
			l.Entries[h.id] = LedgerEntry{Hash: h.hash, VersionLastChanged: nextVersion}
			changes = append(changes, LedgerChange{Change: ChangeAdded, Entity: h.entity})
		case existing.Hash != h.hash:
			changed = true
			existing.Hash = h.hash
			existing.VersionLastChanged = nextVersion
			// A re-added-looking hash change on a previously-tombstoned
			// entry is not a case this docket's design addresses (design:
			// out of scope); left untouched here (RemovedAtVersion is not
			// cleared) rather than guessed at.
			l.Entries[h.id] = existing
			changes = append(changes, LedgerChange{Change: ChangeChanged, Entity: h.entity})
		default:
			// Unchanged content (possibly under a bumped AsOf/Stale, which
			// canonicalHash already excludes) — nothing to do.
		}
	}

	if !truncated {
		// Deterministic order for the returned changes slice, independent
		// of Go's randomized map iteration order.
		ids := make([]string, 0, len(l.Entries))
		for id := range l.Entries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			entry := l.Entries[id]
			if entry.RemovedAtVersion != nil {
				continue // already tombstoned
			}
			if present[id] {
				continue
			}
			changed = true
			v := nextVersion
			entry.RemovedAtVersion = &v
			l.Entries[id] = entry
			changes = append(changes, LedgerChange{Change: ChangeRemoved, Entity: idOnlyEntity(id)})
		}
	}

	if changed {
		l.Version = nextVersion
	}
	l.Cursor = cursorOut

	return changes, nil
}

// ChangesSince returns every entry whose VersionLastChanged or
// RemovedAtVersion exceeds consumerID's stored cursor for this ledger.
// Does NOT advance the cursor — the caller (a later packet's changes verb)
// advances only after its own output is fully written and flushed (design:
// section 5.3).
//
// Entity content: for a removed entry, Entity is the {"id": ...} envelope,
// exactly as Refresh's own returned LedgerChange does. For an added/changed
// entry, ChangesSince has no full entity body to offer — this ledger, by
// this docket's own binding decision, "holds hashes, not entities"
// (LedgerEntry carries only Hash/VersionLastChanged/RemovedAtVersion, never
// a cached body) — so it degrades to the same {"id": ...} envelope. A
// caller wanting the full body for an entry Refresh JUST classified as
// added/changed gets it from Refresh's own return value in the same
// invocation; ChangesSince exists to identify which additional
// consumer-specific catch-up entries are owed (by id and kind) when a
// consumer's cursor lags behind the ledger's current version across
// multiple refreshes, not to reconstruct their historical content.
func (l *Ledger) ChangesSince(consumerID string) []LedgerChange {
	cursor := l.Consumers[consumerID].Cursor

	ids := make([]string, 0, len(l.Entries))
	for id := range l.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var out []LedgerChange
	for _, id := range ids {
		entry := l.Entries[id]
		switch {
		case entry.RemovedAtVersion != nil && *entry.RemovedAtVersion > cursor:
			out = append(out, LedgerChange{Change: ChangeRemoved, Entity: idOnlyEntity(id)})
		case entry.RemovedAtVersion == nil && entry.VersionLastChanged > cursor:
			out = append(out, LedgerChange{Change: ChangeChanged, Entity: idOnlyEntity(id)})
		}
	}
	return out
}

// AdvanceConsumer sets consumerID's cursor to l.Version and updates LastSeen
// to now. The caller MUST call this only after flushing output.
func (l *Ledger) AdvanceConsumer(consumerID string, now time.Time) {
	if l.Consumers == nil {
		l.Consumers = map[string]ConsumerState{}
	}
	l.Consumers[consumerID] = ConsumerState{Cursor: l.Version, LastSeen: now}
}

// ResetConsumer sets consumerID's cursor to zero (design: section 5.1,
// "--reset sets every cursor for that consumer to zero first").
func (l *Ledger) ResetConsumer(consumerID string) {
	if l.Consumers == nil {
		l.Consumers = map[string]ConsumerState{}
	}
	cs := l.Consumers[consumerID]
	cs.Cursor = 0
	l.Consumers[consumerID] = cs
}

// Evict applies the three eviction rules in section 5.4, given the resolved
// consumer-prune duration (state.consumer_prune_after, default 30 days if
// the config key is absent or unparsable) and whether the last Refresh for
// this ledger answered query_not_recognized (rule 3):
//  1. drop a removed LedgerEntry once EVERY consumer's cursor has passed its
//     RemovedAtVersion;
//  2. drop a ConsumerState unseen (LastSeen) for consumerPruneAfter;
//  3. if queryNotRecognized, drop the WHOLE ledger (entries, cursor, and
//     every consumer) for this key.
//
// When queryNotRecognized is true, Evict itself calls deleteLedger(key)
// INTERNALLY before returning — deleting the on-disk file entirely, not
// merely zeroing l's in-memory fields. The caller does not need to (and
// MUST NOT separately) call deleteLedger for rule 3; Evict already did.
// deleteLedger's own error is deliberately swallowed here (Evict's
// signature carries no error return): deleteLedger already treats a
// missing file as success, so only a genuine I/O failure would be lost,
// exactly the same trade-off this file's temp-file-and-rename write path
// already accepts for its own best-effort cleanup steps.
//
// Rule 2 runs before rule 1 so that a consumer PRUNED for staleness in this
// same call cannot indefinitely block rule 1 from dropping a removed entry
// it was the last holdout for — a dead consumer has no further opinion to
// protect. Rule 1's "every consumer" is vacuously true when zero consumers
// remain (after rule 2, or if none were ever registered), so a removed
// entry with no tracking consumer at all is dropped immediately.
func (l *Ledger) Evict(key LedgerKey, now time.Time, consumerPruneAfter time.Duration, queryNotRecognized bool) {
	if queryNotRecognized {
		l.Entries = map[string]LedgerEntry{}
		l.Consumers = map[string]ConsumerState{}
		l.Cursor = nil
		l.Version = 0
		_ = deleteLedger(key)
		return
	}

	// Rule 2: drop a consumer unseen for longer than consumerPruneAfter.
	for id, cs := range l.Consumers {
		if now.Sub(cs.LastSeen) > consumerPruneAfter {
			delete(l.Consumers, id)
		}
	}

	// Rule 1: drop a removed entry once every REMAINING consumer's cursor
	// has passed its RemovedAtVersion.
	for id, entry := range l.Entries {
		if entry.RemovedAtVersion == nil {
			continue
		}
		allPast := true
		for _, cs := range l.Consumers {
			if cs.Cursor < *entry.RemovedAtVersion {
				allPast = false
				break
			}
		}
		if allPast {
			delete(l.Entries, id)
		}
	}
}

// resolveConsumerPruneAfter reads state.consumer_prune_after from the
// parsed config (extending registry.go's Registry/parseRegistry — see
// Registry.StateValue there). CONFIRMED on-disk value (ZR machine config,
// phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix,
// "state.consumer_prune_after = "30d";"): the rendered string uses a bare
// integer-plus-"d" (days) suffix, e.g. "30d" — NOT Go's native
// time.ParseDuration format, which has no "d" unit at all (only
// ns/us/ms/s/m/h) and would ERROR on this exact string. The "<N>d" form is
// parsed as the PRIMARY accepted form (parseDayDuration below); a plain Go
// duration string (e.g. "720h") is additionally accepted as a
// forward-compatibility fallback via time.ParseDuration. Defaults to 30
// days when the key or file is absent, or when the value is neither a
// valid "<N>d" string nor a valid Go duration string.
func resolveConsumerPruneAfter(reg *Registry) time.Duration {
	const def = 30 * 24 * time.Hour

	v, ok := reg.StateValue("consumer_prune_after")
	if !ok {
		return def
	}
	if d, ok := parseDayDuration(v); ok {
		return d
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

// parseDayDuration parses the "<N>d" day-suffix form
// resolveConsumerPruneAfter's own doc comment describes (e.g. "30d" ->
// 30*24h). Returns (_, false) for anything else, including a negative or
// non-integer N.
func parseDayDuration(s string) (time.Duration, bool) {
	if !strings.HasSuffix(s, "d") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * 24 * time.Hour, true
}
