# ccpool session metadata + search — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-01ys` (P3, feature, labels `ccpool`/`human`) — D1 RESOLVED 2026-06-23: PURSUE + commit to Option 2 (pr-pool consumes ccpool as a library). **Re-scoped to sub-feature (2) ONLY** — sub-feature (1) "first-class session-state query" is SUBSUMED by `ccpool state` and is NOT built here.

**Goal:** Let a session carry arbitrary key/value metadata (e.g. `bead=zr-abc`, `role=worker`, `pool=pr-pool`) and let callers query/filter sessions by that metadata, exposed as a **real in-process Go library** (a new exported `github.com/phillipgreenii/ccpool/sessionmeta` package that pr-pool imports — D1/Option 2) with the CLI (`ccpool meta set/get/list/rm` + `ccpool list --filter k=v`) kept as a convenience surface.

**Architecture:** A new `session_metadata` table holds one row per (external_id, key) with a text value — a normalized KV side-table keyed to `sessions.external_id`, the caller's stable handle (ADR 0015). The metadata CRUD + filter logic lives on `internal/store` (private, used by ccpool's own CLI), and is re-exported through a NEW public package `github.com/phillipgreenii/ccpool/sessionmeta` that opens the ccpool pool DB by path and exposes `Set`/`Get`/`Meta`/`Delete`/`ListByMeta`. This is the **primary consumer contract** (Option 2): pr-pool adds a sibling `replace => ../ccpool` and imports `sessionmeta` directly — no shelling out. We do NOT promote all of `internal/store` out of `internal/` (it has 30+ internal callers and exposes the whole session FACT machinery); `sessionmeta` is a deliberately minimal facade so pr-pool depends only on the metadata surface. The CLI adds a `meta` subcommand for set/get/list/rm and extends `list` with a repeatable `--filter key=value` (AND-combined). Metadata is decoupled from the session FACT row: deleting a session deletes its metadata (cascade in `store.Delete`); metadata never participates in the reconciled-state machine. **Concurrency:** ccpool's daemon/hooks and pr-pool may open the same SQLite DB concurrently; the DB is opened WAL + `busy_timeout(5000)` (see `store.Open`), and every metadata write is a single autocommit statement (UPSERT/DELETE) — see the concurrency DESIGN note + Task 8.

**Tech Stack:** Go 1.25 (stdlib `flag`, `database/sql`, `reflect`-based table tests), `modernc.org/sqlite`. Repo: `phillipgreenii-nix-agent-support/packages/ccpool`. Module: `github.com/phillipgreenii/ccpool`.

**Branch:** `ccpool-session-metadata` (off `main`).

---

## DESIGN

### Storage schema + migration

Migrations are embedded SQL applied in version order by `internal/store/schema.go` (`//go:embed migrations/*.sql`; filename pattern `NNN_name.sql`, parsed by `fmt.Sscanf(name, "%d_", &version)`, applied in a tx, recorded in `schema_migrations`). Highest existing version is `005_retry_state.sql`. Add **`006_session_metadata.sql`**:

```sql
-- pg2-01ys: arbitrary KV metadata per session, keyed to sessions.external_id
-- (the caller's stable handle, ADR 0015). One row per (external_id, key); a key
-- holds exactly one value (set replaces). external_id is NOT a FK to sessions
-- (sessions can be pruned/recreated under the same external_id; metadata is
-- cleaned up explicitly by store.Delete). Indexed on (key, value) so a
-- filter "role=worker" is an indexed lookup, and on external_id so per-session
-- fetch + cascade-delete are cheap.
CREATE TABLE session_metadata (
    external_id TEXT NOT NULL,
    key         TEXT NOT NULL,
    value       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (external_id, key)
);
CREATE INDEX session_metadata_key_value ON session_metadata(key, value);
```

Notes:

- `PRIMARY KEY (external_id, key)` makes set an UPSERT (`ON CONFLICT(external_id,key) DO UPDATE`).
- Keyed by `external_id` (TEXT) not the surrogate `sessions.id`, matching how `turns` is keyed (ADR 0015) and how every CLI/library caller addresses sessions.
- No FK constraint: SQLite FKs are off by default in this codebase and `sessions` rows are pruned/recreated under a stable `external_id`. Cleanup is explicit in `store.Delete` (Task 5).
- `value` defaults to `''` so a bare flag/tag (`ccpool meta set x role`) with no value is representable.

### Internal store methods (private, package `internal/store`)

The logic lives on `*Store` (mirroring the existing `ops.go` signature style — `ctx` first, `external_id` string handle, `(T, bool, error)` for lookups). These stay `internal` (used by the ccpool CLI); the public `sessionmeta` package below wraps them.

```go
// SetMeta upserts value for (externalID, key). Empty key is an error. An empty
// value is allowed (a bare tag). Replaces any existing value for that key.
func (s *Store) SetMeta(ctx context.Context, externalID, key, value string) error

// GetMeta returns the value for (externalID, key). ok=false (no error) when the
// key is not set for that session.
func (s *Store) GetMeta(ctx context.Context, externalID, key string) (value string, ok bool, err error)

// Meta returns all metadata for externalID as a map (empty map, never nil, when
// none). Deterministic content; iterate keys sorted at the call site if order
// matters.
func (s *Store) Meta(ctx context.Context, externalID string) (map[string]string, error)

// DeleteMeta removes (externalID, key). Removing an absent key is not an error.
func (s *Store) DeleteMeta(ctx context.Context, externalID, key string) error

// ListExternalIDsByMeta returns the external_ids whose metadata matches ALL of
// filters (AND across keys; exact value match per key). An empty filters map
// returns all external_ids that have ANY metadata row. Result is sorted
// ascending for determinism.
func (s *Store) ListExternalIDsByMeta(ctx context.Context, filters map[string]string) ([]string, error)
```

`store.Delete` (existing, `ops.go:215`) is extended to also delete the session's metadata rows so a purge leaves no orphans.

`ListExternalIDsByMeta` query (AND across N key=value pairs) uses a GROUP BY/HAVING count so it stays a single indexed query:

```sql
SELECT external_id FROM session_metadata
WHERE (key, value) IN ( (?,?), (?,?), ... )   -- one pair per filter
GROUP BY external_id
HAVING COUNT(*) = ?                            -- = number of filters
ORDER BY external_id ASC
```

### CLI surface

New top-level subcommand `meta` (registered in `cmd/ccpool/main.go` `pickSubcommand` known-set + switch), with four verbs:

```
ccpool meta set <external_id> <key> [value]   # value defaults to "" (a bare tag)
ccpool meta get <external_id> <key>           # prints value (exit 1 if unset)
ccpool meta list <external_id>                # prints "key=value" lines, sorted; --json emits an object
ccpool meta rm <external_id> <key>            # idempotent
```

Extend `ccpool list` with a repeatable filter:

```
ccpool list --filter role=worker --filter pool=pr-pool [--json] [--all] [--state s]
```

`--filter` is repeatable (`flag.Value`, same pattern as `new`'s `--env`); multiple filters are AND-combined; each is an exact `key=value` match. When any `--filter` is present, `list` first resolves the matching `external_id` set via `ListExternalIDsByMeta`, then shows only rows whose `external_id` is in that set (intersected with the existing state/retention view). The `--json` shape additionally gains a `meta` object per row so a consumer gets metadata in one call:

```json
{
  "external_id": "zr-abc",
  "...": "...",
  "meta": { "role": "worker", "bead": "zr-abc" }
}
```

Exit conventions match the existing read commands: `0` success, `1` config/store/no-such-key, `2` usage.

### Public library package `github.com/phillipgreenii/ccpool/sessionmeta` (the Option-2 surface)

**Why a new package rather than promoting `internal/store`.** `internal/store` is imported by 30+ files (every `cmd/ccpool/*` command, `internal/session`, `internal/state`, `internal/wait`) and exposes the entire session-FACT machinery (`Session`, `State`, `Transition`, `Turn`, retry windows, the event log). Promoting it wholesale out of `internal/` would (a) make all of that an external API surface pr-pool could couple to, and (b) require rewriting ~30 import paths. Instead we add ONE new top-level exported package `sessionmeta` (a sibling of `cmd/` and `internal/`, at `packages/ccpool/sessionmeta/`) that is a thin facade: it opens the ccpool pool DB and delegates to the same store methods. pr-pool imports only `sessionmeta`; `internal/store` stays private and its internal callers are untouched.

Package API (`packages/ccpool/sessionmeta/sessionmeta.go`):

```go
// Package sessionmeta is ccpool's PUBLIC, importable surface for attaching and
// querying arbitrary key/value metadata on a ccpool session (pg2-01ys, Option 2).
// It is the ONLY exported ccpool Go API; the rest of ccpool stays internal.
package sessionmeta

// Store is a handle to a ccpool pool's metadata, backed by the pool's SQLite DB.
// Open it against a pool's DB path; Close when done. Safe for concurrent use by
// multiple processes: the DB is opened WAL + busy_timeout, and every write is a
// single autocommit statement.
type Store struct { /* wraps *internal/store.Store */ }

// Open opens the metadata store for the ccpool pool whose SQLite DB is at dbPath.
// dbPath is the pool's resolved DB file (see DBPathForPool). Migrations run on
// open (idempotent), so a fresh pool DB is created with the schema if absent.
func Open(dbPath string) (*Store, error)

// OpenPool opens the metadata store for the pool rooted at poolRoot (an empty
// poolRoot opens the default XDG pool), resolving the DB path the same way the
// ccpool CLI does. This is the convenience pr-pool uses when it knows the pool
// root rather than the raw DB path.
func OpenPool(poolRoot string) (*Store, error)

func (s *Store) Close() error

// Set upserts value for (externalID, key). Empty key is an error; empty value is
// a valid bare tag. Replaces any existing value.
func (s *Store) Set(ctx context.Context, externalID, key, value string) error

// Get returns the value for (externalID, key). ok=false (no error) when unset.
func (s *Store) Get(ctx context.Context, externalID, key string) (value string, ok bool, err error)

// Meta returns all metadata for externalID (non-nil empty map when none).
func (s *Store) Meta(ctx context.Context, externalID string) (map[string]string, error)

// Delete removes (externalID, key). Removing an absent key is not an error.
func (s *Store) Delete(ctx context.Context, externalID, key string) error

// ListByMeta returns external_ids whose metadata matches ALL filters (AND across
// keys; exact value per key), sorted ascending. Empty filters returns every
// external_id that has any metadata row.
func (s *Store) ListByMeta(ctx context.Context, filters map[string]string) ([]string, error)
```

`DBPathForPool(poolRoot string) string` is exported from `sessionmeta` too (a tiny wrapper over `config.LoadForPool(poolRoot).DBPath` — or `config`'s resolver), so pr-pool can discover the DB path without depending on `internal/config`. (`internal/config` already exposes `LoadForPool(root)` which stamps `Config.DBPath`; `sessionmeta` calls it internally — `sessionmeta` may import `internal/config`/`internal/store` because it lives INSIDE the ccpool module.)

### Concurrency / DB ownership (IMPORTANT)

Two processes will open the same pool DB: the ccpool binary (hooks/daemon/CLI, which writes `sessions`/`turns` and now `session_metadata`) and pr-pool (which, via `sessionmeta`, writes `session_metadata` and reads via `ListByMeta`). Safety rests on three existing facts plus one constraint this plan keeps:

- **WAL + busy_timeout (already set):** `store.Open` opens every connection with `journal_mode(WAL)` + `busy_timeout(5000)` + `synchronous(NORMAL)` (see `store.go:110-116`). WAL lets readers run concurrently with a writer; `busy_timeout` makes a blocked writer retry for 5s instead of failing with `SQLITE_BUSY`. `sessionmeta.Open` MUST use the identical DSN (it reuses `store.Open`, so it does automatically).
- **Single-statement autocommit writes:** every metadata write (`Set` = one UPSERT, `Delete` = one DELETE) is a single autocommit statement — it takes the write lock for microseconds and releases it. There is no read-modify-write metadata transaction that could deadlock or lost-update across processes. (`Set` is an atomic UPSERT, not select-then-update, precisely so two writers to different keys never clobber.)
- **Disjoint write targets in practice:** ccpool writes `sessions`/`turns`/transitions; pr-pool writes `session_metadata`. They contend only on the single SQLite write lock (serialized by WAL), not on the same rows. Concurrent `Set` to the SAME (external_id,key) from both processes is last-writer-wins by design (acceptable: metadata is advisory bookkeeping, not a fact ledger).
- **Constraint kept:** the metadata API performs NO multi-statement transactions, so `busy_timeout` fully covers cross-process contention. (If a future need for a metadata transaction arises, it must be documented as a cross-process concern — out of scope here.)

This is verified by a concurrency test (Task 8) that opens TWO independent `sessionmeta.Store` handles on the same on-disk DB file and hammers concurrent `Set` from goroutines, asserting no error and a consistent final read. See BLOCKING CONCERN #1 for the one residual risk (a long-held ccpool write transaction elsewhere could still exhaust the 5s busy_timeout under pathological load).

### How pr-pool consumes it (Option 2)

pr-pool today shells out to ccpool (`pr-pool/internal/ccpool/cli.go`); claude-transcript is already a sibling Go dependency of pr-pool (`pr-pool/go.mod`: `require github.com/phillipgreenii/claude-transcript v0.0.0` + `replace => ../claude-transcript`). pr-pool adds ccpool the SAME way:

- **go.mod:** `require github.com/phillipgreenii/ccpool v0.0.0` + `replace github.com/phillipgreenii/ccpool => ../ccpool` (Tasks 9-10).
- **Stamp at dispatch:** open `meta, _ := sessionmeta.OpenPool(poolRoot)` once; after launching a worker, `meta.Set(ctx, externalID, "bead", beadID)` / `"role"` / `"pool"`.
- **Query its pool:** `ids, _ := meta.ListByMeta(ctx, map[string]string{"pool": "pr-pool"})` replaces bespoke bookkeeping (no `ccpool list --json` parse for the metadata case).
- **gomod2nix Pattern B:** pr-pool's `default.nix` src-fileset must now union `../ccpool` (in addition to `../claude-transcript`), `modRoot = "pr-pool"`, regenerate `gomod2nix.toml` (Task 10). Note: ccpool itself depends on `../claude-transcript`; with ccpool in pr-pool's build tree, pr-pool's fileset must include `../claude-transcript` as well so ccpool's own replace resolves.

pr-pool's actual adoption (wiring `sessionmeta` into the orchestrator) is a SEPARATE pr-pool bead; this plan delivers the ccpool library + proves pr-pool can build against it (Task 10 adds a minimal pr-pool import + build check), but does not rewrite pr-pool's orchestration.

---

### Task 1: Migration 006 creates `session_metadata`

**Files:**

- Create: `packages/ccpool/internal/store/migrations/006_session_metadata.sql`
- Test: `packages/ccpool/internal/store/store_test.go` (add a migration-presence test next to `TestOpen_migratesSessionsTable`)

- [ ] **Step 1: Write the failing test**

Add to `internal/store/store_test.go`:

```go
func TestOpen_migratesSessionMetadataTable(t *testing.T) {
	st := newTestStore(t)
	var n int
	err := st.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_metadata'").Scan(&n)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("session_metadata table count = %d, want 1", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/store/ -run SessionMetadataTable -v`
Expected: FAIL — `session_metadata table count = 0, want 1` (no such table yet).

- [ ] **Step 3: Create the migration**

Create `internal/store/migrations/006_session_metadata.sql` with exactly:

```sql
-- pg2-01ys: arbitrary KV metadata per session, keyed to sessions.external_id
-- (the caller's stable handle, ADR 0015). One row per (external_id, key); a key
-- holds exactly one value (set replaces). external_id is NOT a FK to sessions
-- (sessions can be pruned/recreated under the same external_id; metadata is
-- cleaned up explicitly by store.Delete). Indexed on (key, value) so a
-- filter "role=worker" is an indexed lookup, and external_id is the PK prefix so
-- per-session fetch + cascade-delete are cheap.
CREATE TABLE session_metadata (
    external_id TEXT NOT NULL,
    key         TEXT NOT NULL,
    value       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (external_id, key)
);
CREATE INDEX session_metadata_key_value ON session_metadata(key, value);
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/ccpool && go test ./internal/store/ -run SessionMetadataTable -v`
Expected: PASS. (The embed picks the new file up automatically; `migrate` applies it as version 6.)

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/006_session_metadata.sql internal/store/store_test.go
git commit -m "feat(ccpool): add session_metadata table (migration 006)"
```

---

### Task 2: Store `SetMeta`/`GetMeta`/`Meta`/`DeleteMeta`

**Files:**

- Create: `packages/ccpool/internal/store/meta.go`
- Test: `packages/ccpool/internal/store/meta_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/store/meta_test.go`:

```go
package store

import (
	"context"
	"reflect"
	"testing"
)

func TestSetMeta_setGetRoundTrips(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetMeta(ctx, "ext-a", "role", "worker"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	got, ok, err := st.GetMeta(ctx, "ext-a", "role")
	if err != nil || !ok {
		t.Fatalf("GetMeta: ok=%v err=%v", ok, err)
	}
	if got != "worker" {
		t.Errorf("GetMeta = %q, want %q", got, "worker")
	}
}

func TestSetMeta_replacesExistingValue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_ = st.SetMeta(ctx, "ext-a", "bead", "zr-old")
	if err := st.SetMeta(ctx, "ext-a", "bead", "zr-new"); err != nil {
		t.Fatalf("SetMeta replace: %v", err)
	}
	got, _, _ := st.GetMeta(ctx, "ext-a", "bead")
	if got != "zr-new" {
		t.Errorf("after replace GetMeta = %q, want zr-new", got)
	}
}

func TestSetMeta_emptyKeyErrors(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetMeta(context.Background(), "ext-a", "", "v"); err == nil {
		t.Fatal("SetMeta with empty key must error")
	}
}

func TestSetMeta_allowsEmptyValueAsBareTag(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetMeta(ctx, "ext-a", "pinned", ""); err != nil {
		t.Fatalf("SetMeta bare tag: %v", err)
	}
	got, ok, _ := st.GetMeta(ctx, "ext-a", "pinned")
	if !ok || got != "" {
		t.Errorf("bare tag GetMeta = (%q,%v), want (\"\",true)", got, ok)
	}
}

func TestGetMeta_missingKeyOkFalse(t *testing.T) {
	st := newTestStore(t)
	_, ok, err := st.GetMeta(context.Background(), "ext-a", "nope")
	if err != nil || ok {
		t.Fatalf("GetMeta(missing): ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestMeta_returnsAllAsMap(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_ = st.SetMeta(ctx, "ext-a", "role", "worker")
	_ = st.SetMeta(ctx, "ext-a", "bead", "zr-1")
	_ = st.SetMeta(ctx, "ext-b", "role", "feedback") // other session, must not leak in
	got, err := st.Meta(ctx, "ext-a")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	want := map[string]string{"role": "worker", "bead": "zr-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Meta = %v, want %v", got, want)
	}
}

func TestMeta_emptyIsNonNilEmptyMap(t *testing.T) {
	st := newTestStore(t)
	got, err := st.Meta(context.Background(), "ext-none")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("Meta(empty) = %v, want non-nil empty map", got)
	}
}

func TestDeleteMeta_removesKeyIdempotently(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_ = st.SetMeta(ctx, "ext-a", "role", "worker")
	if err := st.DeleteMeta(ctx, "ext-a", "role"); err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}
	if _, ok, _ := st.GetMeta(ctx, "ext-a", "role"); ok {
		t.Error("key still present after DeleteMeta")
	}
	if err := st.DeleteMeta(ctx, "ext-a", "role"); err != nil {
		t.Fatalf("DeleteMeta(absent) must be nil, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/store/ -run Meta -v`
Expected: FAIL — undefined: `st.SetMeta` / `st.GetMeta` / `st.Meta` / `st.DeleteMeta` (compile error).

- [ ] **Step 3: Write the implementation**

Create `internal/store/meta.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SetMeta upserts value for (externalID, key). An empty key is an error; an
// empty value is allowed (a bare tag). Replaces any existing value for the key.
func (s *Store) SetMeta(ctx context.Context, externalID, key, value string) error {
	if key == "" {
		return fmt.Errorf("set meta: key is required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session_metadata (external_id, key, value) VALUES (?,?,?)
		 ON CONFLICT(external_id, key) DO UPDATE SET value = excluded.value`,
		externalID, key, value)
	if err != nil {
		return fmt.Errorf("set meta %q/%q: %w", externalID, key, err)
	}
	return nil
}

// GetMeta returns the value for (externalID, key). ok=false (no error) when the
// key is not set for that session.
func (s *Store) GetMeta(ctx context.Context, externalID, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM session_metadata WHERE external_id = ? AND key = ?`,
		externalID, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get meta %q/%q: %w", externalID, key, err)
	}
	return v, true, nil
}

// Meta returns all metadata for externalID as a map (non-nil empty map when
// none).
func (s *Store) Meta(ctx context.Context, externalID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM session_metadata WHERE external_id = ?`, externalID)
	if err != nil {
		return nil, fmt.Errorf("meta %q: %w", externalID, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// DeleteMeta removes (externalID, key). Removing an absent key is not an error.
func (s *Store) DeleteMeta(ctx context.Context, externalID, key string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM session_metadata WHERE external_id = ? AND key = ?`, externalID, key)
	if err != nil {
		return fmt.Errorf("delete meta %q/%q: %w", externalID, key, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./internal/store/ -run Meta -v`
Expected: PASS (all eight Meta tests green).

- [ ] **Step 5: Commit**

```bash
git add internal/store/meta.go internal/store/meta_test.go
git commit -m "feat(ccpool): store SetMeta/GetMeta/Meta/DeleteMeta"
```

---

### Task 3: Store `ListExternalIDsByMeta` (AND-filter)

**Files:**

- Modify: `packages/ccpool/internal/store/meta.go`
- Test: `packages/ccpool/internal/store/meta_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/store/meta_test.go`:

```go
func TestListExternalIDsByMeta_singleFilter(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_ = st.SetMeta(ctx, "ext-a", "role", "worker")
	_ = st.SetMeta(ctx, "ext-b", "role", "worker")
	_ = st.SetMeta(ctx, "ext-c", "role", "feedback")
	got, err := st.ListExternalIDsByMeta(ctx, map[string]string{"role": "worker"})
	if err != nil {
		t.Fatalf("ListExternalIDsByMeta: %v", err)
	}
	want := []string{"ext-a", "ext-b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (sorted)", got, want)
	}
}

func TestListExternalIDsByMeta_andCombinesFilters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// ext-a matches BOTH; ext-b only role; ext-c only pool.
	_ = st.SetMeta(ctx, "ext-a", "role", "worker")
	_ = st.SetMeta(ctx, "ext-a", "pool", "pr-pool")
	_ = st.SetMeta(ctx, "ext-b", "role", "worker")
	_ = st.SetMeta(ctx, "ext-c", "pool", "pr-pool")
	got, err := st.ListExternalIDsByMeta(ctx, map[string]string{"role": "worker", "pool": "pr-pool"})
	if err != nil {
		t.Fatalf("ListExternalIDsByMeta: %v", err)
	}
	want := []string{"ext-a"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AND filter got %v, want %v", got, want)
	}
}

func TestListExternalIDsByMeta_emptyFiltersReturnsAllWithMeta(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_ = st.SetMeta(ctx, "ext-a", "role", "worker")
	_ = st.SetMeta(ctx, "ext-a", "pool", "p") // ext-a has 2 keys; must appear ONCE
	_ = st.SetMeta(ctx, "ext-b", "role", "feedback")
	got, err := st.ListExternalIDsByMeta(ctx, map[string]string{})
	if err != nil {
		t.Fatalf("ListExternalIDsByMeta: %v", err)
	}
	want := []string{"ext-a", "ext-b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("empty filter got %v, want %v (distinct, sorted)", got, want)
	}
}

func TestListExternalIDsByMeta_noMatchReturnsEmpty(t *testing.T) {
	st := newTestStore(t)
	got, err := st.ListExternalIDsByMeta(context.Background(), map[string]string{"role": "ghost"})
	if err != nil {
		t.Fatalf("ListExternalIDsByMeta: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/store/ -run ListExternalIDsByMeta -v`
Expected: FAIL — undefined: `st.ListExternalIDsByMeta` (compile error).

- [ ] **Step 3: Write the implementation**

Append to `internal/store/meta.go`:

```go
import added at top already covers context/sql/fmt; add "sort" to the import block.

// ListExternalIDsByMeta returns external_ids whose metadata matches ALL filters
// (AND across keys; exact value match per key), sorted ascending. An empty
// filters map returns every external_id that has ANY metadata row (distinct).
func (s *Store) ListExternalIDsByMeta(ctx context.Context, filters map[string]string) ([]string, error) {
	if len(filters) == 0 {
		return s.scanExternalIDs(ctx,
			`SELECT DISTINCT external_id FROM session_metadata ORDER BY external_id ASC`)
	}
	// Build "(key,value) IN ((?,?),(?,?),...)" then require a row per filter via
	// HAVING COUNT(*) = len(filters). PRIMARY KEY(external_id,key) guarantees a
	// session contributes at most one matching row per key, so the count is exact.
	placeholders := ""
	args := make([]any, 0, len(filters)*2+1)
	i := 0
	for k, v := range filters {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "(?,?)"
		args = append(args, k, v)
		i++
	}
	args = append(args, len(filters))
	q := `SELECT external_id FROM session_metadata
	      WHERE (key, value) IN (` + placeholders + `)
	      GROUP BY external_id
	      HAVING COUNT(*) = ?
	      ORDER BY external_id ASC`
	return s.scanExternalIDs(ctx, q, args...)
}

// scanExternalIDs runs q and collects the single-column external_id result.
func (s *Store) scanExternalIDs(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list external_ids by meta: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(out) // ORDER BY already sorts; belt-and-suspenders for determinism
	return out, nil
}
```

(Update the `meta.go` import block to `import ( "context"; "database/sql"; "fmt"; "sort" )`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./internal/store/ -run ListExternalIDsByMeta -v`
Expected: PASS (all four filter tests green).

- [ ] **Step 5: Commit**

```bash
git add internal/store/meta.go internal/store/meta_test.go
git commit -m "feat(ccpool): store ListExternalIDsByMeta AND-filter query"
```

---

### Task 4: `Delete` cascades metadata

**Files:**

- Modify: `packages/ccpool/internal/store/ops.go:215-221` (the `Delete` method)
- Test: `packages/ccpool/internal/store/meta_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/store/meta_test.go`:

```go
func TestDelete_cascadesMetadata(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// A session row keyed ext-a, with two metadata rows.
	if err := st.Insert(ctx, Session{ExternalID: "ext-a", State: Starting}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	_ = st.SetMeta(ctx, "ext-a", "role", "worker")
	_ = st.SetMeta(ctx, "ext-a", "bead", "zr-1")
	if err := st.Delete(ctx, "ext-a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	m, err := st.Meta(ctx, "ext-a")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("metadata not cascaded on Delete: %v", m)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/store/ -run Delete_cascadesMetadata -v`
Expected: FAIL — `metadata not cascaded on Delete: map[bead:zr-1 role:worker]` (current `Delete` only removes the `sessions` row).

- [ ] **Step 3: Extend `Delete`**

In `internal/store/ops.go`, replace the body of `Delete` (currently a single `DELETE FROM sessions`) with a two-statement delete (no tx needed — orphaned metadata is harmless if the second statement somehow fails, but doing sessions-then-metadata in order keeps it simple):

```go
// Delete removes the row for external_id AND its session metadata. Deleting a
// missing row is not an error.
func (s *Store) Delete(ctx context.Context, externalID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE external_id = ?`, externalID); err != nil {
		return fmt.Errorf("delete %q: %w", externalID, err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM session_metadata WHERE external_id = ?`, externalID); err != nil {
		return fmt.Errorf("delete meta for %q: %w", externalID, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./internal/store/ -v`
Expected: PASS — `TestDelete_cascadesMetadata` green; existing `TestDelete_byExternalID` still green.

- [ ] **Step 5: Commit**

```bash
git add internal/store/ops.go internal/store/meta_test.go
git commit -m "feat(ccpool): Delete cascades session metadata"
```

---

### Task 5: `ccpool meta` subcommand (set/get/list/rm)

**Files:**

- Create: `packages/ccpool/cmd/ccpool/meta.go`
- Modify: `packages/ccpool/cmd/ccpool/main.go:16-33` (known-set), `:88-125` (switch)
- Test: `packages/ccpool/cmd/ccpool/meta_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/ccpool/meta_test.go` (a pure unit test on the arg-parsing/dispatch helper so it stays hermetic — mirrors `args_test.go` / `new_test.go` style; the full store-backed path is exercised by the contract test in Task 7):

```go
package main

import (
	"reflect"
	"testing"
)

func TestParseMetaArgs_setWithValue(t *testing.T) {
	verb, ext, key, val, err := parseMetaArgs([]string{"set", "zr-abc", "role", "worker"})
	if err != nil {
		t.Fatalf("parseMetaArgs: %v", err)
	}
	if verb != "set" || ext != "zr-abc" || key != "role" || val != "worker" {
		t.Errorf("got (%q,%q,%q,%q)", verb, ext, key, val)
	}
}

func TestParseMetaArgs_setBareTagDefaultsEmptyValue(t *testing.T) {
	verb, ext, key, val, err := parseMetaArgs([]string{"set", "zr-abc", "pinned"})
	if err != nil {
		t.Fatalf("parseMetaArgs: %v", err)
	}
	if verb != "set" || ext != "zr-abc" || key != "pinned" || val != "" {
		t.Errorf("bare tag got (%q,%q,%q,%q), want set/zr-abc/pinned/\"\"", verb, ext, key, val)
	}
}

func TestParseMetaArgs_getNeedsKey(t *testing.T) {
	if _, _, _, _, err := parseMetaArgs([]string{"get", "zr-abc"}); err == nil {
		t.Fatal("get without key must error")
	}
}

func TestParseMetaArgs_listNeedsOnlyExternalID(t *testing.T) {
	verb, ext, _, _, err := parseMetaArgs([]string{"list", "zr-abc"})
	if err != nil || verb != "list" || ext != "zr-abc" {
		t.Fatalf("list parse got verb=%q ext=%q err=%v", verb, ext, err)
	}
}

func TestParseMetaArgs_unknownVerb(t *testing.T) {
	if _, _, _, _, err := parseMetaArgs([]string{"frobnicate", "zr-abc"}); err == nil {
		t.Fatal("unknown verb must error")
	}
}

func TestParseMetaArgs_noArgs(t *testing.T) {
	if _, _, _, _, err := parseMetaArgs(nil); err == nil {
		t.Fatal("no args must error")
	}
}

func TestRenderMetaList_sortedKeyValueLines(t *testing.T) {
	got := renderMetaList(map[string]string{"role": "worker", "bead": "zr-1"})
	want := "bead=zr-1\nrole=worker\n"
	if got != want {
		t.Errorf("renderMetaList = %q, want %q", got, want)
	}
}

func TestRenderMetaListJSON_object(t *testing.T) {
	got, err := renderMetaListJSON(map[string]string{"role": "worker"})
	if err != nil {
		t.Fatalf("renderMetaListJSON: %v", err)
	}
	if !reflect.DeepEqual(got, `{"role":"worker"}`) {
		t.Errorf("renderMetaListJSON = %s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run 'Meta' -v`
Expected: FAIL — undefined: `parseMetaArgs` / `renderMetaList` / `renderMetaListJSON` (compile error).

- [ ] **Step 3: Write the command**

Create `cmd/ccpool/meta.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// parseMetaArgs validates `meta <verb> <external_id> [key] [value]`. set takes a
// key and an OPTIONAL value (default ""); get/rm take a key; list takes neither.
// Pure (no I/O) so it is unit-testable.
func parseMetaArgs(args []string) (verb, externalID, key, value string, err error) {
	if len(args) < 2 {
		return "", "", "", "", fmt.Errorf("usage: ccpool meta <set|get|list|rm> <external_id> [key] [value]")
	}
	verb, externalID = args[0], args[1]
	rest := args[2:]
	switch verb {
	case "set":
		if len(rest) < 1 {
			return "", "", "", "", fmt.Errorf("usage: ccpool meta set <external_id> <key> [value]")
		}
		key = rest[0]
		if len(rest) >= 2 {
			value = strings.Join(rest[1:], " ")
		}
	case "get", "rm":
		if len(rest) != 1 {
			return "", "", "", "", fmt.Errorf("usage: ccpool meta %s <external_id> <key>", verb)
		}
		key = rest[0]
	case "list":
		if len(rest) != 0 {
			return "", "", "", "", fmt.Errorf("usage: ccpool meta list <external_id> [--json]")
		}
	default:
		return "", "", "", "", fmt.Errorf("ccpool meta: unknown verb %q (want set|get|list|rm)", verb)
	}
	return verb, externalID, key, value, nil
}

// renderMetaList renders metadata as sorted "key=value\n" lines. Pure.
func renderMetaList(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, m[k])
	}
	return b.String()
}

// renderMetaListJSON marshals metadata as a JSON object (deterministic: Go marshals
// map string keys sorted). Pure.
func renderMetaListJSON(m map[string]string) (string, error) {
	if m == nil {
		m = map[string]string{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func runMeta(args []string) int {
	// Pull a trailing --json (only meaningful for `list`) before positional parse.
	jsonOut := false
	var pos []string
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
			continue
		}
		pos = append(pos, a)
	}
	verb, externalID, key, value, err := parseMetaArgs(pos)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()

	switch verb {
	case "set":
		if err := st.SetMeta(ctx, externalID, key, value); err != nil {
			fmt.Fprintln(os.Stderr, "meta set:", err)
			return 1
		}
	case "get":
		v, ok, err := st.GetMeta(ctx, externalID, key)
		if err != nil {
			fmt.Fprintln(os.Stderr, "meta get:", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "no such metadata key")
			return 1
		}
		fmt.Println(v)
	case "rm":
		if err := st.DeleteMeta(ctx, externalID, key); err != nil {
			fmt.Fprintln(os.Stderr, "meta rm:", err)
			return 1
		}
	case "list":
		m, err := st.Meta(ctx, externalID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "meta list:", err)
			return 1
		}
		if jsonOut {
			out, err := renderMetaListJSON(m)
			if err != nil {
				fmt.Fprintln(os.Stderr, "meta list:", err)
				return 1
			}
			fmt.Println(out)
		} else {
			fmt.Print(renderMetaList(m))
		}
	}
	return 0
}

var _ = flag.ContinueOnError // meta uses manual positional parsing; keep flag imported for symmetry
```

Remove the unused `flag` import line and the trailing `var _ = flag...` if `go vet` flags it — `meta` parses positionally, so `flag` is not actually needed. (Drop both the `"flag"` import and that final line; they are listed only to mirror sibling files — delete them so the package compiles clean.)

- [ ] **Step 4: Register the subcommand in `main.go`**

In `cmd/ccpool/main.go`, add `"meta": true,` to the `known` map (keep alphabetical — between `"list"` and `"new"`):

```go
		"list":     true,
		"meta":     true,
		"new":      true,
```

And add a case to the dispatch switch (between `case "list":` and `case "new":`):

```go
	case "meta":
		os.Exit(runMeta(rest))
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run 'Meta' -v && go vet ./cmd/ccpool/`
Expected: PASS — all `parseMetaArgs`/`renderMeta*` tests green; vet clean (no unused import).

- [ ] **Step 6: Commit**

```bash
git add cmd/ccpool/meta.go cmd/ccpool/main.go cmd/ccpool/meta_test.go
git commit -m "feat(ccpool): add 'ccpool meta set/get/list/rm' subcommand"
```

---

### Task 6: `ccpool list --filter k=v` + `meta` in `--json`

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/list.go:19-64` (`runList`), `:129-193` (`listJSON` struct + `renderListJSON`)
- Test: `packages/ccpool/cmd/ccpool/list_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/ccpool/list_test.go` (the renderers are pure; we add a `meta` map parameter threaded in, plus a filter-applied test). First, a filter helper test and a JSON `meta` test:

```go
func TestFilterRowsByExternalIDSet(t *testing.T) {
	rows := []store.Session{
		{ExternalID: "ext-a", State: store.Idle},
		{ExternalID: "ext-b", State: store.Idle},
		{ExternalID: "ext-c", State: store.Idle},
	}
	got := filterRowsByExternalIDSet(rows, map[string]bool{"ext-a": true, "ext-c": true})
	if len(got) != 2 || got[0].ExternalID != "ext-a" || got[1].ExternalID != "ext-c" {
		t.Fatalf("filtered = %+v, want ext-a,ext-c", got)
	}
}

func TestParseFilters_keyValuePairs(t *testing.T) {
	got, err := parseFilters([]string{"role=worker", "pool=pr-pool"})
	if err != nil {
		t.Fatalf("parseFilters: %v", err)
	}
	want := map[string]string{"role": "worker", "pool": "pr-pool"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFilters_rejectsMissingEquals(t *testing.T) {
	if _, err := parseFilters([]string{"role"}); err == nil {
		t.Fatal("filter without '=' must error")
	}
}

func TestRenderListJSON_includesMetaObject(t *testing.T) {
	rows := []store.Session{{ExternalID: "ext-a", State: store.Idle, CWD: "/w"}}
	liveFn := func(_, _ string) bool { return false }
	metaFn := func(ext string) map[string]string { return map[string]string{"role": "worker"} }
	out, err := renderListJSON(rows, true, "", liveFn, nil, nil, metaFn, "ccpool",
		time.Unix(2000, 0), time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("renderListJSON: %v", err)
	}
	if !strings.Contains(out, `"meta":{"role":"worker"}`) {
		t.Errorf("meta not in JSON: %s", out)
	}
}
```

Add the imports `reflect` and (if not present) `strings` to `list_test.go`'s import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run 'Filter|ParseFilters|MetaObject' -v`
Expected: FAIL — undefined `filterRowsByExternalIDSet` / `parseFilters`; `renderListJSON` arity mismatch (no `metaFn` param yet).

- [ ] **Step 3: Add filter parsing + the metaFn parameter + meta field**

In `cmd/ccpool/list.go`:

(a) Add a `filterFlag` type and `parseFilters` near the top of the file (after the imports):

```go
// filterFlag collects repeated `--filter key=value` into a map (mirrors new.go's
// envFlag). Implements flag.Value so `ccpool list` can take --filter repeatedly.
type filterFlag map[string]string

func (f filterFlag) String() string { return "" }

func (f filterFlag) Set(kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("invalid --filter %q, want key=value", kv)
	}
	f[k] = v
	return nil
}

// parseFilters validates raw "key=value" strings into a map (used by tests and as
// the flag-free parse path). Pure.
func parseFilters(raw []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range raw {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("invalid filter %q, want key=value", kv)
		}
		out[k] = v
	}
	return out, nil
}

// filterRowsByExternalIDSet keeps only rows whose ExternalID is in keep,
// preserving input order. Pure.
func filterRowsByExternalIDSet(rows []store.Session, keep map[string]bool) []store.Session {
	var out []store.Session
	for _, r := range rows {
		if keep[r.ExternalID] {
			out = append(out, r)
		}
	}
	return out
}
```

(b) Add the `meta` field to `listJSON` (after `Branch`):

```go
	Branch          *string           `json:"branch,omitempty"`
	Meta            map[string]string `json:"meta,omitempty"`
```

(c) Add a `metaFn func(externalID string) map[string]string` parameter to `renderListJSON` (right after `gitFn`), and populate `item.Meta` inside the loop:

```go
func renderListJSON(rows []store.Session, all bool, stateFilter string,
	liveFn func(socket, target string) bool,
	pathFn func(socket, target string) (string, error),
	gitFn func(cwd string) gitfacet.Facets,
	metaFn func(externalID string) map[string]string,
	socket string,
	now time.Time, doneTTL, failedTTL time.Duration) (string, error) {
	// ... existing loop; after building `item` (before the git block or after it):
	if metaFn != nil {
		if m := metaFn(r.ExternalID); len(m) > 0 {
			item.Meta = m
		}
	}
```

- [ ] **Step 4: Wire the flag + filter resolution + metaFn into `runList`**

In `cmd/ccpool/list.go` `runList`, add the flag and the resolution:

```go
	filters := filterFlag{}
	fs.Var(filters, "filter", "only show sessions whose metadata matches key=value (repeatable, AND-combined)")
```

After `rows, err := st.List(...)` and before rendering, resolve + apply filters:

```go
	if len(filters) > 0 {
		ids, err := st.ListExternalIDsByMeta(context.Background(), filters)
		if err != nil {
			fmt.Fprintln(os.Stderr, "list filter:", err)
			return 1
		}
		keep := make(map[string]bool, len(ids))
		for _, id := range ids {
			keep[id] = true
		}
		rows = filterRowsByExternalIDSet(rows, keep)
	}
```

Define a `metaFn` that reads each row's metadata from the store, and pass it into `renderListJSON`:

```go
	metaFn := func(externalID string) map[string]string {
		m, err := st.Meta(context.Background(), externalID)
		if err != nil {
			return nil
		}
		return m
	}
	// in the *jsonOut branch, update the call site to pass metaFn:
	out, err := renderListJSON(rows, *all, *stateFilter,
		tmux.HasSession, tmux.PaneCurrentPath, gitfacet.Resolve, metaFn, cfg.Tmux.Socket,
		time.Now(), time.Duration(cfg.List.DoneTTL), time.Duration(cfg.List.FailedTTL))
```

(The text `renderList` path does not show metadata — keep it unchanged; metadata is a JSON/`meta` surface.)

- [ ] **Step 5: Fix the existing `renderListJSON` call sites in tests**

The existing `list_test.go` tests call `renderListJSON(... gitFn, "ccpool", ...)`. Insert a `nil` metaFn arg after the git arg in each existing call (there are several — `TestRenderListJSON_fieldsAndLiveness`, `_gitFacetsNullWhenNotInRepo`, `_notLiveFallsBackToLaunchDir`, `_liveButPathQueryFails`, `_allBypassesRetention` (two calls), `_transcriptPathEmptyStillPresentAndEmptyIsArray` (two calls)). Each becomes `..., gitFn, nil, "ccpool", ...` (or `nilGit, nil, ...`). This is a mechanical edit; run the build to find every site.

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -v`
Expected: PASS — new filter/meta tests green; all pre-existing `renderListJSON` tests green with the inserted `nil` metaFn arg.

- [ ] **Step 7: Commit**

```bash
git add cmd/ccpool/list.go cmd/ccpool/list_test.go
git commit -m "feat(ccpool): ccpool list --filter key=value + meta object in --json"
```

---

### Task 7: End-to-end contract test (store-backed CLI roundtrip)

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/contract_test.go` (add a metadata roundtrip using the existing `sandbox` harness)

- [ ] **Step 1: Inspect the harness**

Read `cmd/ccpool/contract_test.go` around the `sandbox` type and `listRow` helper (`:379`) to learn how it builds an isolated pool/store + runs subcommands in-process. Reuse it; do not invent a new harness.

- [ ] **Step 2: Write the failing test**

Add to `cmd/ccpool/contract_test.go` (adapt the exact sandbox constructor/run-helper names to what the file already uses — e.g. if the harness exposes `sb.run("meta", "set", ...)` returning exit code, use that; if it calls `runMeta`/`runList` directly with a configured `CCPOOL_POOL` env, use that):

```go
func TestMeta_cliRoundTripAndListFilter(t *testing.T) {
	sb := newSandbox(t) // existing constructor in this file

	// Seed a session row so list has something to show (use the harness's
	// existing seed/insert helper or `ccpool new` fake-claude path already used
	// by other contract tests).
	sb.seedSession(t, "zr-abc") // adapt to the harness's actual seed helper

	// set metadata via the CLI dispatch.
	if rc := runMeta([]string{"set", "zr-abc", "role", "worker"}); rc != 0 {
		t.Fatalf("meta set rc=%d", rc)
	}
	if rc := runMeta([]string{"set", "zr-abc", "pool", "pr-pool"}); rc != 0 {
		t.Fatalf("meta set pool rc=%d", rc)
	}

	// list --filter must include the row in its --json output with the meta object.
	row, ok := sb.listRow("zr-abc") // existing helper unmarshals listJSON
	if !ok {
		t.Fatal("zr-abc not in list output")
	}
	if row.Meta["role"] != "worker" || row.Meta["pool"] != "pr-pool" {
		t.Errorf("meta object = %v, want role=worker pool=pr-pool", row.Meta)
	}
}
```

If `sb.listRow` does not pass `--filter`, add a sibling helper or assert by running `runList([]string{"--all", "--json", "--filter", "role=worker"})` and capturing stdout (the harness already captures stdout for other commands — reuse that mechanism). The assertion that matters: a row stamped `role=worker` appears under `--filter role=worker`, and a row without it does NOT.

- [ ] **Step 3: Run test to verify it fails, then passes**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run 'Meta_cliRoundTrip' -v`
Expected: first FAIL if the harness helper names differ (adapt them), then PASS once wired to the real sandbox API. The behavior under test is real (store-backed), not mocked.

- [ ] **Step 4: Commit**

```bash
git add cmd/ccpool/contract_test.go
git commit -m "test(ccpool): contract test for meta set + list --filter roundtrip"
```

---

### Task 8: Public `sessionmeta` package + cross-process concurrency test

**Files:**

- Create: `packages/ccpool/sessionmeta/sessionmeta.go`
- Test: `packages/ccpool/sessionmeta/sessionmeta_test.go`

This is the Option-2 library surface: a thin exported facade over `internal/store`'s metadata methods, importable by pr-pool. `internal/store` stays private; only `sessionmeta` is exported.

- [ ] **Step 1: Write the failing test (API roundtrip on a real on-disk DB)**

Create `sessionmeta/sessionmeta_test.go`:

```go
package sessionmeta_test

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/phillipgreenii/ccpool/sessionmeta"
)

func TestOpenSetGet_roundTripsOnDisk(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	s, err := sessionmeta.Open(db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Set(ctx, "zr-abc", "role", "worker"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := s.Get(ctx, "zr-abc", "role")
	if err != nil || !ok || got != "worker" {
		t.Fatalf("Get = (%q,%v,%v), want worker,true,nil", got, ok, err)
	}
}

func TestListByMeta_andFilter(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	s, _ := sessionmeta.Open(db)
	defer s.Close()
	ctx := context.Background()
	_ = s.Set(ctx, "ext-a", "role", "worker")
	_ = s.Set(ctx, "ext-a", "pool", "pr-pool")
	_ = s.Set(ctx, "ext-b", "role", "worker")
	got, err := s.ListByMeta(ctx, map[string]string{"role": "worker", "pool": "pr-pool"})
	if err != nil {
		t.Fatalf("ListByMeta: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"ext-a"}) {
		t.Errorf("ListByMeta = %v, want [ext-a]", got)
	}
}

func TestMeta_nonNilEmpty(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	s, _ := sessionmeta.Open(db)
	defer s.Close()
	m, err := s.Meta(context.Background(), "none")
	if err != nil || m == nil || len(m) != 0 {
		t.Fatalf("Meta = (%v,%v), want non-nil empty map, nil err", m, err)
	}
}

// TestConcurrentWriters_twoHandlesSameDB simulates ccpool + pr-pool both holding
// a handle to the SAME on-disk pool DB and writing metadata concurrently. With
// WAL + busy_timeout and single-statement autocommit writes, no write errors and
// the final reads are consistent (last-writer-wins per key, no lost rows).
func TestConcurrentWriters_twoHandlesSameDB(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	a, err := sessionmeta.Open(db) // "ccpool" writer
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	defer a.Close()
	b, err := sessionmeta.Open(db) // "pr-pool" writer
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	defer b.Close()

	ctx := context.Background()
	const n = 50
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := a.Set(ctx, "ext-a", "k", "from-a"); err != nil {
				t.Errorf("a.Set #%d: %v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := b.Set(ctx, "ext-b", "k", "from-b"); err != nil {
				t.Errorf("b.Set #%d: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	// Disjoint keys: both sessions present with their writer's value.
	va, oka, _ := a.Get(ctx, "ext-a", "k")
	vb, okb, _ := b.Get(ctx, "ext-b", "k")
	if !oka || va != "from-a" {
		t.Errorf("ext-a/k = (%q,%v), want from-a,true", va, oka)
	}
	if !okb || vb != "from-b" {
		t.Errorf("ext-b/k = (%q,%v), want from-b,true", vb, okb)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./sessionmeta/ -v`
Expected: FAIL — package `sessionmeta` does not exist / undefined `sessionmeta.Open` (compile error).

- [ ] **Step 3: Write the package**

Create `sessionmeta/sessionmeta.go`:

```go
// Package sessionmeta is ccpool's PUBLIC, importable surface for attaching and
// querying arbitrary key/value metadata on a ccpool session (pg2-01ys, Option 2).
// It is the ONLY exported ccpool Go API; the rest of ccpool stays internal. It is
// a thin facade over internal/store's metadata methods.
//
// Concurrency: a Store wraps the same SQLite DB the ccpool binary uses, opened
// WAL + busy_timeout (see internal/store.Open). Two processes (e.g. ccpool and
// pr-pool) may hold Stores on the same DB; every write is a single autocommit
// statement, so cross-process contention is covered by busy_timeout. Concurrent
// writes to the same (externalID,key) are last-writer-wins.
package sessionmeta

import (
	"context"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// Store is a handle to a ccpool pool's session metadata.
type Store struct{ st *store.Store }

// Open opens the metadata store for the ccpool pool whose SQLite DB is at dbPath
// (use DBPathForPool to resolve it from a pool root). Migrations run on open
// (idempotent). Close when done.
func Open(dbPath string) (*Store, error) {
	st, err := store.Open(dbPath, clock.Real{})
	if err != nil {
		return nil, err
	}
	return &Store{st: st}, nil
}

// OpenPool opens the metadata store for the pool rooted at poolRoot (empty =
// default XDG pool), resolving the DB path the way the ccpool CLI does.
func OpenPool(poolRoot string) (*Store, error) {
	dbPath, err := DBPathForPool(poolRoot)
	if err != nil {
		return nil, err
	}
	return Open(dbPath)
}

// DBPathForPool resolves the SQLite DB path for the pool rooted at poolRoot
// (empty = default XDG pool), matching ccpool's own resolution.
func DBPathForPool(poolRoot string) (string, error) {
	cfg, err := config.LoadForPool(poolRoot)
	if err != nil {
		return "", err
	}
	return cfg.DBPath, nil
}

func (s *Store) Close() error { return s.st.Close() }

// Set upserts value for (externalID, key). Empty key errors; empty value is a
// valid bare tag. Replaces any existing value.
func (s *Store) Set(ctx context.Context, externalID, key, value string) error {
	return s.st.SetMeta(ctx, externalID, key, value)
}

// Get returns the value for (externalID, key). ok=false (no error) when unset.
func (s *Store) Get(ctx context.Context, externalID, key string) (string, bool, error) {
	return s.st.GetMeta(ctx, externalID, key)
}

// Meta returns all metadata for externalID (non-nil empty map when none).
func (s *Store) Meta(ctx context.Context, externalID string) (map[string]string, error) {
	return s.st.Meta(ctx, externalID)
}

// Delete removes (externalID, key). Removing an absent key is not an error.
func (s *Store) Delete(ctx context.Context, externalID, key string) error {
	return s.st.DeleteMeta(ctx, externalID, key)
}

// ListByMeta returns external_ids whose metadata matches ALL filters (AND across
// keys; exact value per key), sorted ascending. Empty filters returns every
// external_id that has any metadata row.
func (s *Store) ListByMeta(ctx context.Context, filters map[string]string) ([]string, error) {
	return s.st.ListExternalIDsByMeta(ctx, filters)
}
```

NOTE on `config.LoadForPool`: it validates/loads the pool's `config.toml` over defaults and stamps `Config.DBPath` (`internal/config/config.go:148`). It does NOT create/register the dir, so opening a metadata store does not mutate pool registration — correct for an external consumer. If `LoadForPool` is found to require an existing dir during implementation, fall back to `Open(dbPath)` with the caller supplying the path (the test uses `Open` directly, so this does not block the package).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./sessionmeta/ -v -race`
Expected: PASS — all four tests green, including `TestConcurrentWriters_twoHandlesSameDB` under `-race` (no data race, no SQLITE_BUSY error).

- [ ] **Step 5: Commit**

```bash
git add sessionmeta/sessionmeta.go sessionmeta/sessionmeta_test.go
git commit -m "feat(ccpool): public sessionmeta package (Option 2 library surface)"
```

---

### Task 9: pr-pool depends on ccpool (go.mod require + sibling replace)

**Files:**

- Modify: `packages/pr-pool/go.mod`
- Modify: `packages/pr-pool/go.sum` (regenerated)
- Create: `packages/pr-pool/internal/sessionmeta_smoke_test.go` (a minimal import + build proof; the real orchestrator wiring is a separate pr-pool bead)

- [ ] **Step 1: Add the require + replace to pr-pool's go.mod**

This mirrors the EXISTING `claude-transcript` sibling pattern already in `pr-pool/go.mod`. Edit `packages/pr-pool/go.mod`:

```
require (
	github.com/phillipgreenii/ccpool v0.0.0
	github.com/phillipgreenii/claude-transcript v0.0.0
)

require github.com/BurntSushi/toml v1.6.0

replace github.com/phillipgreenii/ccpool => ../ccpool
replace github.com/phillipgreenii/claude-transcript => ../claude-transcript
```

(Keep whatever `require`/`replace` grouping `gofmt`/`go mod tidy` produces; the load-bearing facts are the `ccpool v0.0.0` require and the `=> ../ccpool` replace.)

- [ ] **Step 2: Write the minimal import-proof test**

Create `packages/pr-pool/internal/sessionmeta_smoke_test.go`:

```go
package internal_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/ccpool/sessionmeta"
)

// TestSessionmeta_importable proves pr-pool can build against and call ccpool's
// public sessionmeta package in-process (Option 2). The real orchestrator wiring
// is a separate pr-pool bead; this only guards the dependency edge.
func TestSessionmeta_importable(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	s, err := sessionmeta.Open(db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if err := s.Set(context.Background(), "zr-x", "pool", "pr-pool"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ids, err := s.ListByMeta(context.Background(), map[string]string{"pool": "pr-pool"})
	if err != nil || len(ids) != 1 || ids[0] != "zr-x" {
		t.Fatalf("ListByMeta = (%v,%v), want [zr-x]", ids, err)
	}
}
```

(If pr-pool has no `internal/` package suitable for the test, place it in the package that will own the wiring, or in a new `packages/pr-pool/internal/ccpool/` sibling test file — pick an existing test package in pr-pool. The point is a real cross-module import + call.)

- [ ] **Step 3: Tidy + verify the dependency edge resolves**

Run:

```bash
cd packages/pr-pool && go mod tidy && go test ./... -run Sessionmeta_importable -v
```

Expected: `go mod tidy` resolves `../ccpool` via the replace (and transitively `../claude-transcript`, which pr-pool already replaces); the smoke test PASSES.

- [ ] **Step 4: Commit**

```bash
git add packages/pr-pool/go.mod packages/pr-pool/go.sum packages/pr-pool/internal/sessionmeta_smoke_test.go
git commit -m "feat(pr-pool): depend on ccpool, import sessionmeta (Option 2)"
```

---

### Task 10: pr-pool nix build — gomod2nix Pattern B includes ../ccpool

**Files:**

- Modify: `packages/pr-pool/default.nix` (src fileset + modRoot)
- Modify: `packages/pr-pool/gomod2nix.toml` (regenerated)

Per agent-support CLAUDE.md "Go packages" + repo-base ADR 0008 Pattern B: a local `replace => ../sibling` is resolved natively via a rooted fileset + `modRoot`. pr-pool already does this for `../claude-transcript` (see `packages/ccpool/default.nix` as the model — it unions `./.` + `../claude-transcript`). pr-pool's build tree must now ALSO include `../ccpool`, and because ccpool itself replaces `../claude-transcript`, that dir must be in the union too (it already is for pr-pool's own use).

- [ ] **Step 1: Inspect pr-pool's current default.nix**

Run: `cd packages/pr-pool && sed -n '1,40p' default.nix`
Expected: a `mkGoApp` with `src = lib.fileset.toSource { root = ./..; fileset = unions [ ./. ../claude-transcript ]; }`, `modRoot = "pr-pool"`, `gomod2nixToml = ./gomod2nix.toml`.

- [ ] **Step 2: Add `../ccpool` to the src fileset**

In `packages/pr-pool/default.nix`, extend the fileset union to include `../ccpool` (keeping `../claude-transcript`):

```nix
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../ccpool
      ../claude-transcript
    ];
  };
  modRoot = "pr-pool";
```

Update the comment to note BOTH siblings are present because pr-pool replaces `../ccpool` (which itself replaces `../claude-transcript`).

- [ ] **Step 3: Regenerate gomod2nix.toml**

Run (from `packages/pr-pool`):

```bash
go mod tidy
nix run github:nix-community/gomod2nix -- generate
```

Expected: `gomod2nix.toml` updated. Per ADR 0008 Case B, first-party local-replace modules (`ccpool`, `claude-transcript`) are symlinked from source by `buildGoApplication` and are intentionally ABSENT from the toml (only third-party deps are tracked). Confirm no `ccpool`/`claude-transcript` entry appears in the regenerated toml.

- [ ] **Step 4: Build pr-pool via nix**

Run (from repo root `phillipgreenii-nix-agent-support`):

```bash
nix build .#pr-pool 2>&1 | tail -20
```

Expected: builds clean (the sandbox now contains `pr-pool` + `ccpool` + `claude-transcript` at their relative positions; ccpool's `../claude-transcript` replace resolves).

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/default.nix packages/pr-pool/gomod2nix.toml
git commit -m "build(pr-pool): gomod2nix Pattern B includes ../ccpool sibling"
```

---

### Task 11: Docs — `ccpool --help`/README metadata surface

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/main.go` usage/help text IF it enumerates subcommands; and `packages/ccpool/README.md` (or the nearest ccpool doc) — confirm which exists first.

- [ ] **Step 1: Find the doc surface**

Run: `cd packages/ccpool && ls README* docs/ 2>/dev/null; grep -rn "ccpool list\|ccpool state\|Subcommands\|## Commands" README* 2>/dev/null | head`
Expected: locate the command reference (README section or a `--help` string). If neither exists, skip the README edit and only update inline usage strings.

- [ ] **Step 2: Document the new commands**

Add, in whichever doc enumerates commands, the metadata surface (keep wording terse, matching the existing entries):

```
ccpool meta set <id> <key> [value]   attach KV metadata to a session
ccpool meta get <id> <key>           print a metadata value
ccpool meta list <id> [--json]       list a session's metadata
ccpool meta rm <id> <key>            remove a metadata key
ccpool list --filter key=value       filter sessions by metadata (repeatable, AND)
```

Also document the Go library surface (one line, pointing at the package): orchestrators consume metadata in-process via `github.com/phillipgreenii/ccpool/sessionmeta` (`Open`/`OpenPool` → `Set`/`Get`/`Meta`/`Delete`/`ListByMeta`) — the public Option-2 API; pr-pool imports it as a sibling replace.

- [ ] **Step 3: Commit**

```bash
git add README.md cmd/ccpool/*.go   # whichever changed
git commit -m "docs(ccpool): document session metadata commands + sessionmeta library"
```

---

### Task 12: Full verification + repo checks + bead close

- [ ] **Step 1: Full Go test + vet (both modules)**

Run: `cd packages/ccpool && go test ./... -race && go vet ./...`
Then: `cd packages/pr-pool && go test ./... && go vet ./...`
Expected: all PASS (ccpool incl. `sessionmeta` race test; pr-pool incl. the sessionmeta import smoke test).

- [ ] **Step 2: Manual smoke**

Run: `cd packages/ccpool && go build ./cmd/ccpool && ./ccpool meta 2>&1 | grep -i usage`
Expected: prints the `meta` usage line (exit 2 with no args).
Then: `./ccpool list --filter 2>&1 | grep -i "key=value"` — the flag is registered (errors on missing value, which is correct).

- [ ] **Step 3: gomod2nix check — ccpool unchanged, pr-pool changed**

- ccpool adds NO new Go module dependencies (only stdlib + existing `modernc.org/sqlite`); the new `sessionmeta` package imports only in-module packages. Confirm ccpool's dep files are unchanged:

  Run: `cd packages/ccpool && git diff --name-only | grep -E 'go.(mod|sum)|gomod2nix.toml'`
  Expected: NO output.

- pr-pool DID change its deps (added the ccpool require + replace). Confirm `packages/pr-pool/go.mod`, `go.sum`, and `gomod2nix.toml` were updated (Tasks 9-10) and that the regenerated `gomod2nix.toml` does NOT contain a `ccpool`/`claude-transcript` entry (first-party local-replace modules are symlinked, ADR 0008 Case B):

  Run: `cd packages/pr-pool && grep -E 'ccpool|claude-transcript' gomod2nix.toml || echo "OK: no first-party entries in toml"`
  Expected: `OK: no first-party entries in toml`.

- [ ] **Step 4: Repo checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `phillipgreenii-nix-agent-support`):

```bash
prek run --all-files || pre-commit run --all-files
nix build .#ccpool .#pr-pool
nix flake check
```

Expected: all PASS. (`nix flake check` builds both packages via gomod2nix; pr-pool's build tree now unions `../ccpool` + `../claude-transcript` per Task 10.)

- [ ] **Step 5: Close the bead**

```bash
bd update pg2-01ys --claim     # if not already claimed
bd comment pg2-01ys "Implemented sub-feature (2) session metadata + search (D1/Option 2 = REAL in-process Go library): session_metadata table (migration 006); internal/store SetMeta/GetMeta/Meta/DeleteMeta/ListExternalIDsByMeta + Delete cascade; NEW public package github.com/phillipgreenii/ccpool/sessionmeta (Open/OpenPool/Set/Get/Meta/Delete/ListByMeta) as the importable Option-2 surface (internal/store stays private); CLI 'ccpool meta set/get/list/rm' and 'ccpool list --filter key=value'. pr-pool now requires ccpool via sibling 'replace => ../ccpool' (gomod2nix Pattern B, fileset unions ../ccpool) and imports sessionmeta in-process. Cross-process DB concurrency covered (WAL + busy_timeout + single-statement autocommit writes; concurrent-writer test under -race). Sub-feature (1) state-query confirmed subsumed by 'ccpool state'; not built. pr-pool orchestrator wiring is a separate pr-pool follow-up bead."
bd close pg2-01ys
```

---

## Self-review checklist (done while writing)

- **Spec coverage:** D1-rescoped AC (sub-feature 2 only) is fully covered — attach arbitrary KV metadata (Task 2 `internal/store.SetMeta`; Task 5 `ccpool meta set`; Task 8 `sessionmeta.Set`), query/filter by it (Task 3 `ListExternalIDsByMeta`; Task 6 `ccpool list --filter`; Task 8 `sessionmeta.ListByMeta`). Sub-feature (1) explicitly NOT built (goal + bead-close). Option 2 = REAL Go library: public `sessionmeta` package (Task 8), `internal/store` stays private; pr-pool requires ccpool via sibling replace (Task 9) + gomod2nix Pattern B (Task 10). Concurrency addressed in DESIGN + verified (Task 8 concurrent-writer test under `-race`). CLI kept as convenience (Tasks 5-6).
- **Placeholder scan:** every code step shows real code. Two adaptive steps are flagged, not placeholders: Task 7 names the exact contract-harness helpers to adapt (`newSandbox`/`sb.listRow`/`listJSON`); Task 9 Step 2 says which pr-pool test package to place the import-proof in. Task 8's `config.LoadForPool` note gives a concrete fallback if the resolver requires an existing dir.
- **Type consistency:** internal store methods `SetMeta`/`GetMeta`/`Meta`/`DeleteMeta`/`ListExternalIDsByMeta` named identically across Tasks 2-8; public facade `sessionmeta.Store` methods `Set`/`Get`/`Meta`/`Delete`/`ListByMeta` consistent Task 8↔9 and DESIGN; CLI helpers `parseMetaArgs`/`renderMetaList`/`renderMetaListJSON`/`runMeta` consistent Task 5↔7; `filterFlag`/`parseFilters`/`filterRowsByExternalIDSet` consistent Task 6; JSON field `Meta map[string]string`/`json:"meta,omitempty"` consistent Task 6↔7; migration version 006 consistent Task 1↔12; module path `github.com/phillipgreenii/ccpool/sessionmeta` consistent DESIGN↔Tasks 8-11. `renderListJSON` gains exactly one new param (`metaFn`) inserted after `gitFn`; Task 6 Step 5 fixes every existing call site.

---

## BLOCKING CONCERNS

1. **Cross-process SQLite write contention (NEW — flagged per coordinator request).** Option 2 means ccpool (daemon/hooks/CLI) and pr-pool now write the SAME pool DB from separate OS processes. The DESIGN argues this is safe (WAL + `busy_timeout(5000)` + single-statement autocommit metadata writes, verified by Task 8's concurrent-writer `-race` test). The **residual risk**: SQLite still serializes ALL writers on one DB-level write lock, and `busy_timeout` is 5s. If ccpool ever holds a LONG write transaction elsewhere (it currently does not — transitions/turns are short single-statement or tiny txns), a pr-pool `Set` could in principle exhaust the 5s timeout and return `SQLITE_BUSY`. **Decision/awareness needed:** is in-process shared-DB write from a second process acceptable as the Option-2 contract, or should pr-pool's writes instead go through a ccpool-owned path (e.g. a future `ccpool meta` IPC/daemon call) to keep a single writer? This plan ships the shared-DB approach (matches the decision "real in-process Go library") and the test proves the common case; the single-writer alternative would be a larger redesign. Recommend: ship shared-DB now, and if `SQLITE_BUSY` is ever observed, add a thin retry/backoff in `sessionmeta` (NOT in scope here).

2. **`internal/config` coupling of `sessionmeta`.** `sessionmeta.OpenPool`/`DBPathForPool` call `internal/config.LoadForPool` to resolve a pool's DB path. That keeps pool-path logic in one place but means the public package's pool-resolution semantics are pinned to ccpool's config internals. Alternative: keep `sessionmeta` purely path-based (`Open(dbPath)` only) and make pr-pool resolve the pool root itself. Recommend keeping `OpenPool` (ergonomic for pr-pool) since `LoadForPool` is already a stable internal seam; flagged in case the team wants the public package to have zero `internal/config` dependency.

## DEFERRED (non-blocking) decisions

3. **Key namespace / reserved keys.** Should ccpool reserve or validate any metadata keys (e.g. namespace pr-pool's keys like `prpool.role`)? This plan treats keys as fully free-form. If a convention is wanted (e.g. reserved `bead`/`role`/`pool`), decide before pr-pool's orchestrator-wiring bead adopts it, to avoid a later migration.

4. **Text `ccpool list` metadata visibility.** Metadata is surfaced only in `--json`, via `ccpool meta list`, and the Go API (the text table is unchanged to preserve column layout). A trailing `META` column in the human table is an easy add but a deferred UX decision.
