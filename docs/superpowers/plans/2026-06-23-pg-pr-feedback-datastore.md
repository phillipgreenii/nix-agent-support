# pg-pr Feedback Datastore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move PR-feedback storage out of beads into a pg-pr-owned SQLite store, with an in-process event system + transactional outbox, relocating bead-creation into a `beads` handler — beads then hold only actionable work.

**Architecture:** Library-first (`internal/store` owns SQLite; CLI and daemon both import it; daemon is a scheduler around the same ops). Mutations write state + an outbox row in one transaction; after commit an outbox runner dispatches each event to an in-process `[]Handler` (isolated failures, at-least-once). The `beads` handler projects the PR + process-feedback beads; the reply-poster posts upstream. Durable reply delivery is a reconcile re-scan of `feedback` rows keyed by `response_id`.

**Tech Stack:** Go 1.25, `modernc.org/sqlite` (pure-Go, no cgo), cobra, gomod2nix. Module path: `github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr`.

**Spec:** `docs/superpowers/specs/2026-06-23-pg-pr-feedback-datastore-design.md`. Read it before starting.

---

## Conventions (read once)

- **Working dir for all commands:** `phillipgreenii-nix-agent-support/packages/pg-pr` (the Go module root). All `go` commands run from there.
- **Test a single test:** `go test ./internal/store/... -run TestName -v`
- **Test a package:** `go test ./internal/store/...`
- **Build everything:** `go build ./...`
- **Vet:** `go vet ./...`
- **Commit style:** conventional commits (`feat:`, `test:`, `refactor:`, `chore:`), imperative, no LLM attribution. One logical change per commit. Branch: simple name (e.g. `pg-pr-feedback-store`) — this is a personal/nix repo, not the ZR monorepo (no `phillipg.TICKET` convention here).
- **Before declaring a phase done:** `nix flake check` MUST pass at the repo root, and if `.pre-commit-config.yaml` exists, `prek run --all-files` (or `pre-commit run --all-files`) MUST pass.
- **TDD:** every behavior gets a failing test first. Run it red, implement minimally, run it green, commit.
- **DI pattern in this codebase:** dependencies are injected via interfaces (see `pkg/beads.Runner`, `internal/sync.Deps`). Follow it — every new component takes its collaborators as interface fields so tests inject fakes.

---

## File Structure

**New files**

- `internal/store/store.go` — `DB` wrapper: `Open`, connection setup (WAL, busy_timeout), `Close`, transaction helper.
- `internal/store/migrate.go` — `user_version`-based migrations under `BEGIN EXCLUSIVE`; schema DDL.
- `internal/store/pull_request.go` — `PullRequest` type + `UpsertPR`/`GetPR`/`ListOpenPRs`/`ClosePR`.
- `internal/store/feedback.go` — `Feedback`/`Message` types + `UpsertFeedback` (dedup), `ListFeedback`, `GetFeedback`, `SetDisposition`, `MarkReplied`, `ResolveFeedback`, `ListPendingReplies`, `ReconcileStaleness`.
- `internal/store/outbox.go` — `OutboxRow`, `enqueueTx`, `RunOutbox`.
- `internal/store/event.go` — `Event` type + event-type constants.
- `internal/event/dispatcher.go` — `Handler` func type + `Dispatcher` (`Register`, `Dispatch` with per-handler isolation).
- `internal/beadsbridge/bridge.go` — the relocated bead-creation code as an event `Handler`.
- `internal/feedbackclassify/classify.go` — author classification (`author_kind`/`agent_name`/`is_ours`), severity parse, fingerprint policy.
- `internal/marker/htmlmarker.go` — HTML marker constant + dual-match detection (extends existing `internal/marker`).
- `internal/replyposter/poster.go` — reply-poster `Handler` + reconcile re-scan.
- `cmd/pg-pr/feedback.go` — `pg-pr feedback list/show/disposition` cobra commands.

**Modified files**

- `go.mod` / `gomod2nix.toml` — add `modernc.org/sqlite`.
- `internal/agentregistry/registry.go` — extend `Entry` (agent_name, body_marker, policy) + matchers.
- `internal/config/config.go` — already has `Agents []agentregistry.Entry`; no change beyond the registry type growing.
- `pkg/provider/vcs/github/enrich.go` — add `isOutdated`, `isMinimized`/`minimizedReason`, per-comment `originalCommit { oid }` to the enriched query; surface on the parsed types.
- `pkg/provider/vcs/github/github.go` — add a `MinimizeComment` mutation method.
- `internal/sync/sync.go` — ingestion writes `feedback` rows via the store; stop creating `feedback` beads; relocate bead-creation calls to event emission.
- `internal/sync/daemon.go` — wire the dispatcher + store + reconcile re-scan into the daemon loop.

---

## PHASE 1 — Store: driver, schema, migrations, core types, outbox

### Task 1.1: Add the SQLite driver dependency

**Files:**

- Modify: `go.mod`, `gomod2nix.toml`

- [ ] **Step 1: Add the import to a throwaway anchor so `go mod tidy` keeps it**

Create `internal/store/store.go` with the minimal driver import (expanded in 1.2):

```go
// Package store is pg-pr's SQLite-backed datastore: the system of record for
// PR identity and PR feedback. Both the CLI and the daemon import it.
package store

import (
	"database/sql"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// DB wraps the sql.DB handle plus pg-pr store operations.
type DB struct {
	sql *sql.DB
}
```

- [ ] **Step 2: Tidy and regenerate the nix lock**

Run (from the module root):

```
go get modernc.org/sqlite@latest
```

```
go mod tidy
```

```
nix run github:nix-community/gomod2nix -- generate
```

Expected: `go.mod` gains `modernc.org/sqlite`, `gomod2nix.toml` updates. Do **not** add `vendorHash`/`buildGoModule` (per repo CLAUDE.md, this family uses gomod2nix).

- [ ] **Step 3: Build to confirm the driver compiles**

Run: `go build ./internal/store/...`
Expected: success (empty package builds).

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum gomod2nix.toml internal/store/store.go
git commit -m "chore: add modernc.org/sqlite driver for pg-pr store"
```

### Task 1.2: Open the DB with WAL + busy_timeout

**Files:**

- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"path/filepath"
	"testing"
)

func TestOpenAppliesPragmas(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var journalMode string
	if err := db.sql.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := db.sql.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout < 5000 {
		t.Fatalf("busy_timeout = %d, want >= 5000", busyTimeout)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/... -run TestOpenAppliesPragmas -v`
Expected: FAIL — `Open`/`Close` undefined.

- [ ] **Step 3: Implement `Open` and `Close`**

Append to `internal/store/store.go`:

```go
import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open opens (creating if absent) the SQLite database at path, applies the
// connection pragmas, and runs migrations. The modernc driver name is
// "sqlite". WAL + a 5s busy_timeout let an ad-hoc CLI invocation and a running
// daemon serialize writes without "database is locked" errors.
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// modernc serializes writes correctly with a single connection; cap the
	// pool so WAL writers don't contend within one process.
	sqlDB.SetMaxOpenConns(1)
	db := &DB{sql: sqlDB}
	if err := migrate(db); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", path, err)
	}
	return db, nil
}

// Close closes the underlying handle.
func (db *DB) Close() error { return db.sql.Close() }
```

Add a temporary `func migrate(db *DB) error { return nil }` at the bottom of `store.go` so it compiles until Task 1.3 replaces it.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/... -run TestOpenAppliesPragmas -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: open pg-pr store with WAL + busy_timeout pragmas"
```

### Task 1.3: Versioned migrations under BEGIN EXCLUSIVE

**Files:**

- Create: `internal/store/migrate.go` (move `migrate` out of `store.go`)
- Test: `internal/store/migrate_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"path/filepath"
	"testing"
)

func TestMigrateSetsUserVersionAndIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}

	// Re-running migrate is a no-op (idempotent).
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	// The four tables exist.
	for _, table := range []string{"pull_request", "feedback", "code_comment_message", "outbox"} {
		var name string
		err := db.sql.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "newer.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Simulate a DB written by a newer binary.
	if _, err := db.sql.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("expected error migrating a newer schema, got nil")
	}
	_ = db.Close()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/... -run TestMigrate -v`
Expected: FAIL — `schemaVersion` undefined, `migrate` is the no-op stub.

- [ ] **Step 3: Implement migrations**

Delete the stub `migrate` from `store.go`. Create `internal/store/migrate.go`:

```go
package store

import "fmt"

// schemaVersion is the current schema. Bump it and append a migration step
// whenever the DDL changes. Stored in SQLite's user_version pragma.
const schemaVersion = 1

// migrations is the ordered list of DDL applied to reach schemaVersion. Index i
// migrates user_version i -> i+1.
var migrations = []string{
	// v0 -> v1: initial schema.
	`
CREATE TABLE pull_request (
    id             INTEGER PRIMARY KEY,
    repo           TEXT NOT NULL,
    number         INTEGER NOT NULL,
    ownership      TEXT NOT NULL CHECK (ownership IN ('mine','team')),
    author         TEXT,
    state          TEXT NOT NULL,
    branch         TEXT,
    base           TEXT,
    url            TEXT,
    head_sha       TEXT,
    last_synced_at TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE (repo, number)
);

CREATE TABLE feedback (
    id                 INTEGER PRIMARY KEY,
    pr_id              INTEGER NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
    kind               TEXT NOT NULL CHECK (kind IN
                         ('code-comment-thread','pr-comments','ci-failure','review-request','jira-link')),
    external_id        TEXT,
    fingerprint        TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'new' CHECK (status IN
                         ('new','presented','dispositioned','replied','resolved','superseded')),
    title              TEXT,
    body               TEXT,

    subject_sha        TEXT,
    first_seen_head_sha TEXT,
    is_outdated        INTEGER NOT NULL DEFAULT 0,
    is_minimized       INTEGER NOT NULL DEFAULT 0,
    minimized_reason   TEXT,

    author_login       TEXT,
    author_kind        TEXT CHECK (author_kind IN ('human','agent')),
    agent_name         TEXT,
    is_ours            INTEGER NOT NULL DEFAULT 0,
    author_role        TEXT,

    disposition_action TEXT CHECK (disposition_action IS NULL OR disposition_action IN
                         ('will-fix','wont-fix','no-action')),
    disposition_note   TEXT,
    reply_body         TEXT,
    response_id        TEXT,
    severity           TEXT,
    managed_upstream   INTEGER NOT NULL DEFAULT 0,

    -- type-specific (nullable; enforced per-kind below)
    file               TEXT,
    line               INTEGER,
    thread_resolved    INTEGER,
    comment_node_id    TEXT,
    run_id             TEXT,
    check_name         TEXT,
    conclusion         TEXT,
    related            INTEGER,
    retry_count        INTEGER,
    link               TEXT,

    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    resolved_at        TEXT,

    UNIQUE (pr_id, fingerprint),
    CHECK (kind <> 'code-comment-thread' OR file IS NOT NULL)
);
CREATE INDEX idx_feedback_pr ON feedback(pr_id);

CREATE TABLE code_comment_message (
    id           INTEGER PRIMARY KEY,
    feedback_id  INTEGER NOT NULL REFERENCES feedback(id) ON DELETE CASCADE,
    external_id  TEXT NOT NULL,
    author_login TEXT,
    author_kind  TEXT,
    agent_name   TEXT,
    is_ours      INTEGER NOT NULL DEFAULT 0,
    author_role  TEXT,
    body         TEXT,
    posted_at    TEXT,
    UNIQUE (feedback_id, external_id)
);

CREATE TABLE outbox (
    id           INTEGER PRIMARY KEY,
    type         TEXT NOT NULL,
    payload      TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','complete')),
    created_at   TEXT NOT NULL,
    completed_at TEXT
);
CREATE INDEX idx_outbox_pending ON outbox(id) WHERE status = 'pending';
`,
}

// migrate brings the DB up to schemaVersion. It runs each pending migration in
// its own EXCLUSIVE transaction. If the DB is newer than this binary it
// refuses (returns an error) rather than writing against a schema it doesn't
// understand.
func migrate(db *DB) error {
	var current int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("db schema version %d is newer than this binary (%d); upgrade pg-pr", current, schemaVersion)
	}
	for current < schemaVersion {
		stmt := migrations[current]
		if err := applyMigration(db, current+1, stmt); err != nil {
			return err
		}
		current++
	}
	return nil
}

func applyMigration(db *DB, toVersion int, ddl string) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin migration to v%d: %w", toVersion, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(ddl); err != nil {
		return fmt.Errorf("apply migration to v%d: %w", toVersion, err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", toVersion)); err != nil {
		return fmt.Errorf("set user_version=%d: %w", toVersion, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration to v%d: %w", toVersion, err)
	}
	return nil
}
```

> Serialization note (review N1): `SetMaxOpenConns(1)` from Task 1.2 + the `user_version` gate are the real guards. A redundant `BEGIN EXCLUSIVE` inside a `database/sql` tx is dropped — it's dead/ineffective on modernc's single-conn pool.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/... -run TestMigrate -v`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrate.go internal/store/store.go internal/store/migrate_test.go
git commit -m "feat: versioned pg-pr store schema + migrations"
```

### Task 1.4: `PullRequest` type + `UpsertPR`/`GetPR`

**Files:**

- Create: `internal/store/pull_request.go`
- Test: `internal/store/pull_request_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestUpsertPRInsertsThenUpdates(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	pr := PullRequest{
		Repo: "owner/repo", Number: 42, Ownership: "mine",
		Author: "phillipg", State: "open", HeadSHA: "abc123",
	}
	id, err := db.UpsertPR(ctx, pr)
	if err != nil {
		t.Fatalf("UpsertPR insert: %v", err)
	}
	if id == 0 {
		t.Fatal("UpsertPR returned id 0")
	}

	// Upsert again with a new head_sha -> same id, updated field.
	pr.HeadSHA = "def456"
	id2, err := db.UpsertPR(ctx, pr)
	if err != nil {
		t.Fatalf("UpsertPR update: %v", err)
	}
	if id2 != id {
		t.Fatalf("upsert created a new row: id=%d id2=%d", id, id2)
	}

	got, err := db.GetPR(ctx, "owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got == nil || got.HeadSHA != "def456" {
		t.Fatalf("GetPR = %+v, want head_sha def456", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/... -run TestUpsertPR -v`
Expected: FAIL — `PullRequest`, `UpsertPR`, `GetPR` undefined.

- [ ] **Step 3: Implement**

Create `internal/store/pull_request.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PullRequest is the authoritative PR row.
type PullRequest struct {
	ID           int64
	Repo         string
	Number       int
	Ownership    string // "mine" | "team"
	Author       string
	State        string
	Branch       string
	Base         string
	URL          string
	HeadSHA      string
	LastSyncedAt string
}

// nowRFC3339 is the clock; overridable in tests.
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339) }

// UpsertPR inserts or updates a PR by (repo, number) and returns its id.
func (db *DB) UpsertPR(ctx context.Context, pr PullRequest) (int64, error) {
	if pr.Repo == "" || pr.Number == 0 {
		return 0, errors.New("store: UpsertPR requires repo and number")
	}
	now := nowRFC3339()
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO pull_request
  (repo, number, ownership, author, state, branch, base, url, head_sha, last_synced_at, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(repo, number) DO UPDATE SET
  ownership=excluded.ownership, author=excluded.author, state=excluded.state,
  branch=excluded.branch, base=excluded.base, url=excluded.url,
  head_sha=excluded.head_sha, last_synced_at=excluded.last_synced_at,
  updated_at=excluded.updated_at`,
		pr.Repo, pr.Number, pr.Ownership, pr.Author, pr.State, pr.Branch, pr.Base,
		pr.URL, pr.HeadSHA, pr.LastSyncedAt, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("store: upsert pr %s#%d: %w", pr.Repo, pr.Number, err)
	}
	got, err := db.GetPR(ctx, pr.Repo, pr.Number)
	if err != nil {
		return 0, err
	}
	return got.ID, nil
}

// GetPR returns the PR by (repo, number), or nil if not found.
func (db *DB) GetPR(ctx context.Context, repo string, number int) (*PullRequest, error) {
	row := db.sql.QueryRowContext(ctx, `
SELECT id, repo, number, ownership, author, state, branch, base, url, head_sha, last_synced_at
FROM pull_request WHERE repo=? AND number=?`, repo, number)
	var pr PullRequest
	err := row.Scan(&pr.ID, &pr.Repo, &pr.Number, &pr.Ownership, &pr.Author,
		&pr.State, &pr.Branch, &pr.Base, &pr.URL, &pr.HeadSHA, &pr.LastSyncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get pr %s#%d: %w", repo, number, err)
	}
	return &pr, nil
}
```

> Implementer note: scanning nullable TEXT columns into `string` works on modernc (NULL → ""). If a column can be NULL and you need to distinguish, use `sql.NullString`. The fields above are written by `UpsertPR` so they're never NULL in practice.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/... -run TestUpsertPR -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/pull_request.go internal/store/pull_request_test.go
git commit -m "feat: pull_request upsert/get in pg-pr store"
```

### Task 1.5: `Feedback`/`Message` types + `UpsertFeedback` (dedup) + `GetFeedback`/`ListFeedback`

**Files:**

- Create: `internal/store/feedback.go`
- Test: `internal/store/feedback_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"context"
	"testing"
)

func TestUpsertFeedbackDedupsByFingerprint(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})

	fb := Feedback{
		PRID: prID, Kind: "pr-comments", Fingerprint: "fp-1",
		Body: "first", AuthorKind: "human", AuthorRole: "team_member",
	}
	id, err := db.UpsertFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("UpsertFeedback: %v", err)
	}

	// Same (pr_id, fingerprint) -> update, not a new row.
	fb.Body = "second"
	id2, err := db.UpsertFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("UpsertFeedback 2: %v", err)
	}
	if id2 != id {
		t.Fatalf("dedup failed: id=%d id2=%d", id, id2)
	}

	got, err := db.GetFeedback(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("GetFeedback: %v / %v", got, err)
	}
	if got.Body != "second" {
		t.Fatalf("Body = %q, want second", got.Body)
	}

	list, err := db.ListFeedback(ctx, prID, ListFilter{})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListFeedback len = %d, want 1", len(list))
	}
}

func TestCodeCommentThreadRequiresFile(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "team", State: "open"})

	// kind=code-comment-thread with no file violates the CHECK constraint.
	_, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "code-comment-thread", Fingerprint: "fp-x",
	})
	if err == nil {
		t.Fatal("expected CHECK violation for code-comment-thread without file")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/... -run 'TestUpsertFeedback|TestCodeComment' -v`
Expected: FAIL — types/methods undefined.

- [ ] **Step 3: Implement**

Create `internal/store/feedback.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Feedback is one feedback item (any kind). The type-specific tail is nullable;
// the schema's CHECK constraints enforce required fields per kind.
type Feedback struct {
	ID          int64
	PRID        int64
	Kind        string
	ExternalID  string
	Fingerprint string
	Status      string
	Title       string
	Body        string

	SubjectSHA       string
	FirstSeenHeadSHA string
	IsOutdated       bool
	IsMinimized      bool
	MinimizedReason  string

	AuthorLogin string
	AuthorKind  string
	AgentName   string
	IsOurs      bool
	AuthorRole  string

	DispositionAction string
	DispositionNote   string
	ReplyBody         string
	ResponseID        string
	Severity          string
	ManagedUpstream   bool

	File           string
	Line           int
	ThreadResolved bool
	CommentNodeID  string
	RunID          string
	CheckName      string
	Conclusion     string
	Related        bool
	RetryCount     int
	Link           string
}

// Message is one comment within a code-comment-thread feedback item.
type Message struct {
	ID          int64
	FeedbackID  int64
	ExternalID  string
	AuthorLogin string
	AuthorKind  string
	AgentName   string
	IsOurs      bool
	AuthorRole  string
	Body        string
	PostedAt    string
}

// ListFilter narrows ListFeedback.
type ListFilter struct {
	// ActiveOnly drops outdated/minimized/superseded items.
	ActiveOnly bool
	// Kind, when non-empty, restricts to one kind.
	Kind string
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpsertFeedback inserts or updates a feedback row by (pr_id, fingerprint),
// returning its id. Preserves disposition/reply columns on update (those are
// owned by the agent, not the ingester) by only overwriting upstream-sourced
// fields.
func (db *DB) UpsertFeedback(ctx context.Context, f Feedback) (int64, error) {
	if f.PRID == 0 || f.Kind == "" || f.Fingerprint == "" {
		return 0, errors.New("store: UpsertFeedback requires pr_id, kind, fingerprint")
	}
	if f.Status == "" {
		f.Status = "new"
	}
	now := nowRFC3339()
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO feedback
  (pr_id, kind, external_id, fingerprint, status, title, body,
   subject_sha, first_seen_head_sha, is_outdated, is_minimized, minimized_reason,
   author_login, author_kind, agent_name, is_ours, author_role,
   severity, managed_upstream,
   file, line, thread_resolved, comment_node_id, run_id, check_name, conclusion, related, retry_count, link,
   created_at, updated_at)
VALUES (?,?,?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?, ?,?,?,?,?,?,?,?,?,?, ?,?)
ON CONFLICT(pr_id, fingerprint) DO UPDATE SET
  external_id=excluded.external_id, title=excluded.title, body=excluded.body,
  subject_sha=excluded.subject_sha, is_outdated=excluded.is_outdated,
  is_minimized=excluded.is_minimized, minimized_reason=excluded.minimized_reason,
  author_login=excluded.author_login, author_kind=excluded.author_kind,
  agent_name=excluded.agent_name, is_ours=excluded.is_ours, author_role=excluded.author_role,
  severity=excluded.severity, managed_upstream=excluded.managed_upstream,
  file=excluded.file, line=excluded.line, thread_resolved=excluded.thread_resolved,
  comment_node_id=excluded.comment_node_id, run_id=excluded.run_id, check_name=excluded.check_name,
  conclusion=excluded.conclusion, related=excluded.related, retry_count=excluded.retry_count,
  link=excluded.link, updated_at=excluded.updated_at`,
		f.PRID, f.Kind, f.ExternalID, f.Fingerprint, f.Status, f.Title, f.Body,
		f.SubjectSHA, f.FirstSeenHeadSHA, b2i(f.IsOutdated), b2i(f.IsMinimized), f.MinimizedReason,
		f.AuthorLogin, f.AuthorKind, f.AgentName, b2i(f.IsOurs), f.AuthorRole,
		f.Severity, b2i(f.ManagedUpstream),
		f.File, f.Line, b2i(f.ThreadResolved), f.CommentNodeID, f.RunID, f.CheckName, f.Conclusion, b2i(f.Related), f.RetryCount, f.Link,
		now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("store: upsert feedback (pr=%d fp=%s): %w", f.PRID, f.Fingerprint, err)
	}
	var id int64
	err = db.sql.QueryRowContext(ctx,
		"SELECT id FROM feedback WHERE pr_id=? AND fingerprint=?", f.PRID, f.Fingerprint).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: read back feedback id: %w", err)
	}
	return id, nil
}

const feedbackCols = `id, pr_id, kind, external_id, fingerprint, status, title, body,
  subject_sha, first_seen_head_sha, is_outdated, is_minimized, minimized_reason,
  author_login, author_kind, agent_name, is_ours, author_role,
  disposition_action, disposition_note, reply_body, response_id, severity, managed_upstream,
  file, line, thread_resolved, comment_node_id, run_id, check_name, conclusion, related, retry_count, link`

func scanFeedback(s interface{ Scan(...any) error }) (*Feedback, error) {
	var f Feedback
	var (
		isOutdated, isMin, isOurs, threadResolved, related int
		managed                                            int
		dispAction, dispNote, replyBody, responseID        sql.NullString
		minReason, subjectSHA, firstSeen                   sql.NullString
		file, commentNode, runID, checkName, concl, link   sql.NullString
		severity                                           sql.NullString
		line, retry                                        sql.NullInt64
	)
	err := s.Scan(&f.ID, &f.PRID, &f.Kind, &f.ExternalID, &f.Fingerprint, &f.Status, &f.Title, &f.Body,
		&subjectSHA, &firstSeen, &isOutdated, &isMin, &minReason,
		&f.AuthorLogin, &f.AuthorKind, &f.AgentName, &isOurs, &f.AuthorRole,
		&dispAction, &dispNote, &replyBody, &responseID, &severity, &managed,
		&file, &line, &threadResolved, &commentNode, &runID, &checkName, &concl, &related, &retry, &link)
	if err != nil {
		return nil, err
	}
	f.SubjectSHA, f.FirstSeenHeadSHA, f.MinimizedReason = subjectSHA.String, firstSeen.String, minReason.String
	f.IsOutdated, f.IsMinimized, f.IsOurs = isOutdated == 1, isMin == 1, isOurs == 1
	f.DispositionAction, f.DispositionNote = dispAction.String, dispNote.String
	f.ReplyBody, f.ResponseID, f.Severity = replyBody.String, responseID.String, severity.String
	f.ManagedUpstream, f.ThreadResolved, f.Related = managed == 1, threadResolved == 1, related == 1
	f.File, f.CommentNodeID, f.RunID = file.String, commentNode.String, runID.String
	f.CheckName, f.Conclusion, f.Link = checkName.String, concl.String, link.String
	f.Line, f.RetryCount = int(line.Int64), int(retry.Int64)
	return &f, nil
}

// GetFeedback returns one item by id, or nil.
func (db *DB) GetFeedback(ctx context.Context, id int64) (*Feedback, error) {
	row := db.sql.QueryRowContext(ctx, "SELECT "+feedbackCols+" FROM feedback WHERE id=?", id)
	f, err := scanFeedback(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get feedback %d: %w", id, err)
	}
	return f, nil
}

// ListFeedback returns a PR's feedback, oldest first.
func (db *DB) ListFeedback(ctx context.Context, prID int64, filter ListFilter) ([]Feedback, error) {
	q := "SELECT " + feedbackCols + " FROM feedback WHERE pr_id=?"
	args := []any{prID}
	if filter.Kind != "" {
		q += " AND kind=?"
		args = append(args, filter.Kind)
	}
	if filter.ActiveOnly {
		q += " AND is_outdated=0 AND is_minimized=0 AND status NOT IN ('superseded','resolved')"
	}
	q += " ORDER BY id"
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list feedback pr=%d: %w", prID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/... -run 'TestUpsertFeedback|TestCodeComment' -v`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
git add internal/store/feedback.go internal/store/feedback_test.go
git commit -m "feat: feedback upsert/dedup/list in pg-pr store"
```

### Task 1.6: Disposition + reply + staleness mutators

**Files:**

- Modify: `internal/store/feedback.go`
- Test: `internal/store/feedback_disposition_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"context"
	"testing"
)

func TestSetDispositionAndListPendingReplies(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})
	id, _ := db.UpsertFeedback(ctx, Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "fp"})

	if err := db.SetDisposition(ctx, id, "will-fix", "addressing", "queued reply"); err != nil {
		t.Fatalf("SetDisposition: %v", err)
	}
	got, _ := db.GetFeedback(ctx, id)
	if got.DispositionAction != "will-fix" || got.ReplyBody != "queued reply" || got.Status != "dispositioned" {
		t.Fatalf("disposition not applied: %+v", got)
	}

	pending, err := db.ListPendingReplies(ctx)
	if err != nil {
		t.Fatalf("ListPendingReplies: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("pending = %+v, want the one item", pending)
	}

	if err := db.MarkReplied(ctx, id, "resp-123"); err != nil {
		t.Fatalf("MarkReplied: %v", err)
	}
	pending, _ = db.ListPendingReplies(ctx)
	if len(pending) != 0 {
		t.Fatalf("after MarkReplied pending = %d, want 0", len(pending))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/... -run TestSetDisposition -v`
Expected: FAIL — `SetDisposition`/`ListPendingReplies`/`MarkReplied` undefined.

- [ ] **Step 3: Implement (append to `feedback.go`)**

```go
// SetDisposition records the agent's decision and (optionally) a queued reply.
// Moves status to "dispositioned".
func (db *DB) SetDisposition(ctx context.Context, id int64, action, note, reply string) error {
	now := nowRFC3339()
	res, err := db.sql.ExecContext(ctx, `
UPDATE feedback SET disposition_action=?, disposition_note=?, reply_body=?, status='dispositioned', updated_at=?
WHERE id=?`, action, note, reply, now, id)
	if err != nil {
		return fmt.Errorf("store: set disposition %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: feedback %d not found", id)
	}
	return nil
}

// MarkReplied records the upstream response id and moves status to "replied".
func (db *DB) MarkReplied(ctx context.Context, id int64, responseID string) error {
	now := nowRFC3339()
	_, err := db.sql.ExecContext(ctx,
		"UPDATE feedback SET response_id=?, status='replied', updated_at=? WHERE id=?",
		responseID, now, id)
	if err != nil {
		return fmt.Errorf("store: mark replied %d: %w", id, err)
	}
	return nil
}

// ListPendingReplies returns items with a queued reply_body but no response_id
// yet — the durable reply-delivery work list (re-scanned each reconcile).
func (db *DB) ListPendingReplies(ctx context.Context) ([]Feedback, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT "+feedbackCols+
		" FROM feedback WHERE reply_body IS NOT NULL AND reply_body <> '' AND (response_id IS NULL OR response_id='') ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("store: list pending replies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// ReconcileStaleness marks ci-failure rows whose subject_sha != the PR head as
// superseded. Code-thread is_outdated comes from the provider (set on upsert),
// so it is NOT touched here.
func (db *DB) ReconcileStaleness(ctx context.Context, prID int64, headSHA string) error {
	_, err := db.sql.ExecContext(ctx, `
UPDATE feedback SET status='superseded', updated_at=?
WHERE pr_id=? AND kind='ci-failure' AND subject_sha IS NOT NULL AND subject_sha <> ?
  AND status NOT IN ('superseded','resolved')`,
		nowRFC3339(), prID, headSHA)
	if err != nil {
		return fmt.Errorf("store: reconcile staleness pr=%d: %w", prID, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/... -run TestSetDisposition -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/feedback.go internal/store/feedback_disposition_test.go
git commit -m "feat: feedback disposition/reply/staleness mutators"
```

### Task 1.7: Outbox enqueue + run (transactional)

**Files:**

- Create: `internal/store/event.go`, `internal/store/outbox.go`
- Test: `internal/store/outbox_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOutboxRollbackThenCommitDispatch(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Rolled-back txn must NOT leave an outbox row.
	err := db.InTx(ctx, func(tx *Tx) error {
		if err := tx.EnqueueEvent(EventPROpened, json.RawMessage(`{"pr":1}`)); err != nil {
			return err
		}
		return errForceRollback
	})
	if err == nil {
		t.Fatal("expected rollback error")
	}
	var n int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox").Scan(&n)
	if n != 0 {
		t.Fatalf("rolled-back txn left %d outbox rows, want 0", n)
	}

	// Committed txn leaves a pending row; RunOutbox dispatches it and completes it.
	_ = db.InTx(ctx, func(tx *Tx) error {
		return tx.EnqueueEvent(EventPROpened, json.RawMessage(`{"pr":2}`))
	})
	var dispatched []Event
	if err := db.RunOutbox(ctx, func(ctx context.Context, e Event) error {
		dispatched = append(dispatched, e)
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	if len(dispatched) != 1 || dispatched[0].Type != EventPROpened {
		t.Fatalf("dispatched = %+v", dispatched)
	}
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox WHERE status='pending'").Scan(&n)
	if n != 0 {
		t.Fatalf("pending rows after run = %d, want 0", n)
	}
}

func TestOutboxCompletesEvenWhenDispatchErrors(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	_ = db.InTx(ctx, func(tx *Tx) error {
		return tx.EnqueueEvent(EventFeedbackCreated, json.RawMessage(`{}`))
	})
	// Dispatch returns an error; the row must still be marked complete (fire-once).
	_ = db.RunOutbox(ctx, func(ctx context.Context, e Event) error { return errForceRollback })
	var n int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox WHERE status='pending'").Scan(&n)
	if n != 0 {
		t.Fatalf("pending after erroring dispatch = %d, want 0 (fire-once)", n)
	}
}

var errForceRollback = errTest("forced")

type errTest string

func (e errTest) Error() string { return string(e) }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/... -run TestOutbox -v`
Expected: FAIL — `InTx`/`Tx`/`EnqueueEvent`/`RunOutbox`/`Event*` undefined.

- [ ] **Step 3: Implement `event.go`**

```go
package store

import "encoding/json"

// Event is an in-process domain event carried across the commit boundary by the
// outbox. Payload is opaque JSON the handler decodes.
type Event struct {
	Type    string
	Payload json.RawMessage
}

// Event type constants.
const (
	EventPROpened        = "pr.opened"
	EventPRUpdated       = "pr.updated"
	EventPRClosed        = "pr.closed"
	EventPRMerged        = "pr.merged"
	EventFeedbackCreated = "feedback.created"
	EventFeedbackDisposed = "feedback.disposed"
	EventFeedbackResolved = "feedback.resolved"
)
```

- [ ] **Step 4: Implement `outbox.go` (Tx wrapper + enqueue + runner)**

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Tx is a store transaction with helpers to mutate state AND enqueue the event
// that resulted, so both commit or roll back together.
type Tx struct {
	tx  *sql.Tx
	ctx context.Context
}

// InTx runs fn in a transaction. If fn returns an error the tx rolls back (and
// any enqueued outbox rows vanish). On nil it commits.
func (db *DB) InTx(ctx context.Context, fn func(*Tx) error) error {
	sqlTx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	t := &Tx{tx: sqlTx, ctx: ctx}
	if err := fn(t); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}

// EnqueueEvent writes a pending outbox row inside the transaction.
func (t *Tx) EnqueueEvent(eventType string, payload json.RawMessage) error {
	_, err := t.tx.ExecContext(t.ctx,
		"INSERT INTO outbox (type, payload, status, created_at) VALUES (?,?, 'pending', ?)",
		eventType, string(payload), nowRFC3339())
	if err != nil {
		return fmt.Errorf("store: enqueue %s: %w", eventType, err)
	}
	return nil
}

// Exec runs a statement inside the transaction (used by store methods that take
// a *Tx; see the *Tx mutator variants added in Phase 2 wiring).
func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(t.ctx, query, args...)
}

// DispatchFunc handles one event. Returning an error is logged by RunOutbox but
// does NOT prevent the row from completing (fire-once, at-least-once).
type DispatchFunc func(ctx context.Context, e Event) error

// RunOutbox pulls each pending row, dispatches it, then marks it complete
// regardless of the dispatch outcome. Returns the first I/O error (not handler
// errors).
func (db *DB) RunOutbox(ctx context.Context, dispatch DispatchFunc) error {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT id, type, payload FROM outbox WHERE status='pending' ORDER BY id")
	if err != nil {
		return fmt.Errorf("store: select pending outbox: %w", err)
	}
	type pend struct {
		id int64
		e  Event
	}
	var pending []pend
	for rows.Next() {
		var p pend
		var payload string
		if err := rows.Scan(&p.id, &p.e.Type, &payload); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan outbox: %w", err)
		}
		p.e.Payload = json.RawMessage(payload)
		pending = append(pending, p)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range pending {
		// Fire-once: dispatch errors are the dispatcher's concern (it logs
		// per-handler). We mark complete regardless.
		_ = dispatch(ctx, p.e)
		if _, err := db.sql.ExecContext(ctx,
			"UPDATE outbox SET status='complete', completed_at=? WHERE id=?",
			nowRFC3339(), p.id); err != nil {
			return fmt.Errorf("store: complete outbox %d: %w", p.id, err)
		}
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/store/... -run TestOutbox -v`
Expected: PASS (both).

- [ ] **Step 6: Commit**

```bash
git add internal/store/event.go internal/store/outbox.go internal/store/outbox_test.go
git commit -m "feat: transactional outbox enqueue + fire-once runner"
```

### Task 1.8: `*Tx` mutator variants — atomic mutate + enqueue (resolves review B1)

The transactional-outbox guarantee requires the state mutation AND the outbox enqueue to share **one** transaction (rollback → neither survives). As coded, `UpsertPR`/`UpsertFeedback`/`SetDisposition` run their own statements on `db.sql`, so calling them inside an `InTx` block alongside `tx.EnqueueEvent` is **not** atomic (and risks serializing oddly on the 1-conn pool). This task adds `*Tx`-receiver variants; the `db.*` methods become thin `InTx` wrappers. Tasks 5.3 and 7.1 then mutate + enqueue on the same `*Tx`.

**Files:**

- Modify: `internal/store/outbox.go` (add `Tx.QueryRow`), `internal/store/pull_request.go`, `internal/store/feedback.go`
- Test: `internal/store/tx_mutators_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"context"
	"testing"
)

func TestTxUpsertAndEnqueueAreAtomic(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})

	// Rollback: neither the feedback row nor the outbox row survives.
	_ = db.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.UpsertFeedback(Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "f1"}); err != nil {
			return err
		}
		if err := tx.EnqueueEvent(EventFeedbackCreated, []byte(`{}`)); err != nil {
			return err
		}
		return errForceRollback // defined in outbox_test.go
	})
	var fn, on int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback").Scan(&fn)
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox").Scan(&on)
	if fn != 0 || on != 0 {
		t.Fatalf("rollback left feedback=%d outbox=%d, want 0/0", fn, on)
	}

	// Commit: both land.
	_ = db.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.UpsertFeedback(Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "f2"}); err != nil {
			return err
		}
		return tx.EnqueueEvent(EventFeedbackCreated, []byte(`{}`))
	})
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback").Scan(&fn)
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox WHERE status='pending'").Scan(&on)
	if fn != 1 || on != 1 {
		t.Fatalf("commit left feedback=%d pending-outbox=%d, want 1/1", fn, on)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/... -run TestTxUpsertAndEnqueue -v`
Expected: FAIL — `(*Tx).UpsertFeedback` undefined.

- [ ] **Step 3: Implement** — add to `outbox.go`:

```go
// QueryRow runs a single-row query inside the transaction.
func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(t.ctx, query, args...)
}
```

Refactor the existing `db.UpsertPR`/`db.UpsertFeedback`/`db.SetDisposition` (Tasks 1.4–1.6) so the SQL body moves to a `*Tx` receiver and the `db.*` method wraps it in `InTx`. Pattern (apply to all three):

```go
// feedback.go
func (db *DB) UpsertFeedback(ctx context.Context, f Feedback) (int64, error) {
	var id int64
	err := db.InTx(ctx, func(tx *Tx) error {
		var e error
		id, e = tx.UpsertFeedback(f)
		return e
	})
	return id, err
}

// UpsertFeedback runs the INSERT ... ON CONFLICT + id read-back on the tx.
// Body is exactly what db.UpsertFeedback held before, with t.Exec(...) and
// t.QueryRow(...) replacing db.sql.ExecContext/QueryRowContext.
func (t *Tx) UpsertFeedback(f Feedback) (int64, error) {
	// ... validation + INSERT ... ON CONFLICT via t.Exec(...) ...
	// ... id := via t.QueryRow("SELECT id FROM feedback WHERE pr_id=? AND fingerprint=?", ...).Scan(&id) ...
}
```

Do the same for `(t *Tx) UpsertPR(PullRequest) (int64, error)` and `(t *Tx) SetDisposition(id int64, action, note, reply string) error`. The earlier Task 1.4–1.6 tests keep passing unchanged (they call the `db.*` wrappers).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/... -v`
Expected: PASS (new test + all earlier store tests still green).

- [ ] **Step 5: Commit**

```bash
git add internal/store/outbox.go internal/store/pull_request.go internal/store/feedback.go internal/store/tx_mutators_test.go
git commit -m "feat: *Tx mutator variants for atomic mutate+enqueue"
```

---

## PHASE 2 — Event dispatcher + beads handler relocation

### Task 2.1: In-process dispatcher with handler isolation

**Files:**

- Create: `internal/event/dispatcher.go`
- Test: `internal/event/dispatcher_test.go`

- [ ] **Step 1: Write the failing test**

```go
package event

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

func TestDispatchIsolatesHandlerFailures(t *testing.T) {
	var calls []string
	d := New()
	d.Register(func(ctx context.Context, e store.Event) error {
		calls = append(calls, "A")
		return errors.New("boom") // must not stop B
	})
	d.Register(func(ctx context.Context, e store.Event) error {
		calls = append(calls, "B")
		return nil
	})
	d.Register(func(ctx context.Context, e store.Event) error {
		panic("kaboom") // must be recovered, must not stop the others
	})
	d.Register(func(ctx context.Context, e store.Event) error {
		calls = append(calls, "D")
		return nil
	})

	d.Dispatch(context.Background(), store.Event{Type: store.EventPROpened})

	want := []string{"A", "B", "D"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want all non-panicking handlers to run %v", calls, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/event/... -run TestDispatch -v`
Expected: FAIL — package/`New`/`Register`/`Dispatch` undefined.

- [ ] **Step 3: Implement**

```go
// Package event is pg-pr's in-process event dispatcher. Handlers are called in
// registration order; a failure or panic in one is isolated so the rest run.
package event

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// Handler reacts to one event. Returning an error is logged; it never blocks
// sibling handlers.
type Handler func(ctx context.Context, e store.Event) error

// Dispatcher fans an event out to registered handlers.
type Dispatcher struct {
	handlers []Handler
	log      *slog.Logger
}

// New returns an empty dispatcher logging to slog.Default.
func New() *Dispatcher { return &Dispatcher{log: slog.Default()} }

// WithLogger sets the logger (chainable).
func (d *Dispatcher) WithLogger(l *slog.Logger) *Dispatcher { d.log = l; return d }

// Register appends a handler.
func (d *Dispatcher) Register(h Handler) { d.handlers = append(d.handlers, h) }

// Dispatch calls every handler, recovering panics and logging errors. Safe to
// pass as store.DispatchFunc.
func (d *Dispatcher) Dispatch(ctx context.Context, e store.Event) error {
	for i, h := range d.handlers {
		d.callOne(ctx, i, h, e)
	}
	return nil
}

func (d *Dispatcher) callOne(ctx context.Context, idx int, h Handler, e store.Event) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("event handler panicked", "handler", idx, "type", e.Type, "panic", fmt.Sprint(r))
		}
	}()
	if err := h(ctx, e); err != nil {
		d.log.Warn("event handler error", "handler", idx, "type", e.Type, "err", err.Error())
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/event/... -run TestDispatch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/event/dispatcher.go internal/event/dispatcher_test.go
git commit -m "feat: in-process event dispatcher with handler isolation"
```

### Task 2.2: Relocate bead-creation into a `beads` handler

**Files:**

- Create: `internal/beadsbridge/bridge.go`
- Test: `internal/beadsbridge/bridge_test.go`

The handler reacts to events and calls the existing `pkg/beads.Client` wrappers — it does **not** reimplement them. It must be idempotent (at-least-once outbox) and preserve `FindOpenProcessingCycle`'s error-propagation.

- [ ] **Step 1: Write the failing test (fake beads client via the Runner interface)**

```go
package beadsbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// fakeRunner records bd invocations and returns canned ids.
type fakeRunner struct{ calls [][]string }

func (f *fakeRunner) Run(ctx context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) > 0 && args[0] == "create" {
		return "bead-1", nil
	}
	return "[]", nil
}

func TestPROpenedCreatesPRBead(t *testing.T) {
	fr := &fakeRunner{}
	client := beads.NewClientWithRunner(fr)
	h := New(client)

	payload, _ := json.Marshal(PRPayload{Repo: "o/r", Number: 7, Title: "https://x/7"})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var sawCreate bool
	for _, c := range fr.calls {
		if len(c) > 0 && c[0] == "create" && strings.Contains(strings.Join(c, " "), "merge-request") {
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Fatalf("expected a merge-request create, got calls %v", fr.calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/beadsbridge/... -run TestPROpened -v`
Expected: FAIL — package/`New`/`PRPayload`/`Handle` undefined.

- [ ] **Step 3: Implement the bridge**

Move the bead-orchestration logic currently inlined in `internal/sync/sync.go` (the calls to `EnsureMergeRequest`, `CreateProcessingCycle`/`FindOpenProcessingCycle`, `CloseMergeRequest` + `cascadeClose`) into this handler. The handler dispatches by event type:

```go
// Package beadsbridge is the event handler that projects pg-pr's PR + process-
// feedback beads. It relocates the bead-orchestration that used to live inline
// in internal/sync. It creates the PR (merge-request) bead and the process-
// feedback bead, and cascade-closes on PR close. It does NOT create feedback
// beads — feedback now lives in internal/store.
package beadsbridge

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// BeadClient is the subset of *beads.Client the bridge needs (kept narrow for
// tests). The real *beads.Client satisfies it.
type BeadClient interface {
	EnsureMergeRequest(ctx context.Context, title string, fields beads.MergeRequestFields) (string, bool, error)
	FindByRepoAndNumber(ctx context.Context, repo string, number int) (*beads.MergeRequest, error)
	CloseMergeRequest(ctx context.Context, id, reason string) error
	ListChildrenOfPR(ctx context.Context, prBeadID string) ([]string, error)
	CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error)
	FindOpenProcessingCycle(ctx context.Context, prBeadID string) (string, bool, error)
	CloseProcessingCycle(ctx context.Context, id, reason string) error
	CloseFeedback(ctx context.Context, id, reason string) error
}

// Handler is the beads event handler.
type Handler struct{ client BeadClient }

// New constructs the handler.
func New(client BeadClient) *Handler { return &Handler{client: client} }

// PRPayload is the JSON payload for pr.* events.
type PRPayload struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Ownership string `json:"ownership"`
	Merged   bool   `json:"merged"`
}

// FeedbackPayload is the JSON payload for feedback.created events.
type FeedbackPayload struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Mine   bool   `json:"mine"`
}

// Handle implements event.Handler. Idempotent: re-dispatch under the
// at-least-once outbox must not duplicate beads.
func (h *Handler) Handle(ctx context.Context, e store.Event) error {
	switch e.Type {
	case store.EventPROpened, store.EventPRUpdated:
		var p PRPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode pr payload: %w", err)
		}
		_, _, err := h.client.EnsureMergeRequest(ctx, p.Title, beads.MergeRequestFields{
			Repo: p.Repo, PRNumber: p.Number,
		})
		return err
	case store.EventFeedbackCreated:
		var p FeedbackPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode feedback payload: %w", err)
		}
		return h.ensureProcessFeedbackBead(ctx, p)
	case store.EventPRClosed, store.EventPRMerged:
		var p PRPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode pr payload: %w", err)
		}
		return h.cascadeClose(ctx, p)
	}
	return nil
}

// ensureProcessFeedbackBead upserts the open process-feedback bead for a PR.
// Reuses FindOpenProcessingCycle whose error MUST propagate (swallowing it as
// "none open" is the documented duplicate-cycle bug).
func (h *Handler) ensureProcessFeedbackBead(ctx context.Context, p FeedbackPayload) error {
	mr, err := h.client.FindByRepoAndNumber(ctx, p.Repo, p.Number)
	if err != nil {
		return err
	}
	if mr == nil {
		return fmt.Errorf("beadsbridge: no merge-request bead for %s#%d", p.Repo, p.Number)
	}
	_, open, err := h.client.FindOpenProcessingCycle(ctx, mr.ID)
	if err != nil {
		return err // propagate — do NOT treat as "no open cycle"
	}
	if open {
		return nil
	}
	_, err = h.client.CreateProcessingCycle(ctx, mr.ID, fmt.Sprintf("%s#%d", p.Repo, p.Number), p.Mine)
	return err
}

// cascadeClose closes the PR bead and its descendants.
func (h *Handler) cascadeClose(ctx context.Context, p PRPayload) error {
	mr, err := h.client.FindByRepoAndNumber(ctx, p.Repo, p.Number)
	if err != nil || mr == nil {
		return err
	}
	reason := "pr-closed"
	if p.Merged {
		reason = "upstream-merged"
	}
	children, err := h.client.ListChildrenOfPR(ctx, mr.ID)
	if err != nil {
		return err
	}
	for _, child := range children {
		_ = h.client.CloseProcessingCycle(ctx, child, reason)
	}
	return h.client.CloseMergeRequest(ctx, mr.ID, reason)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/beadsbridge/... -run TestPROpened -v`
Expected: PASS.

- [ ] **Step 5: Add a regression test for the duplicate-cycle guard**

```go
func TestEnsureProcessFeedbackPropagatesFindError(t *testing.T) {
	client := &errFindClient{}
	h := New(client)
	payload, _ := json.Marshal(FeedbackPayload{Repo: "o/r", Number: 1})
	err := h.Handle(context.Background(), store.Event{Type: store.EventFeedbackCreated, Payload: payload})
	if err == nil {
		t.Fatal("expected FindOpenProcessingCycle error to propagate, got nil (would re-create cycles)")
	}
}

// errFindClient returns an error from FindOpenProcessingCycle; all other methods
// are no-ops returning zero values. (Implement the BeadClient interface inline.)
```

Implement `errFindClient` satisfying `BeadClient` with `FindByRepoAndNumber` returning a stub `*beads.MergeRequest{ID: "x"}` and `FindOpenProcessingCycle` returning an error. Run:

Run: `go test ./internal/beadsbridge/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/beadsbridge/
git commit -m "feat: relocate bead orchestration into beadsbridge event handler"
```

### Task 2.3: Wire dispatcher + store + outbox flush into the daemon and one-shot paths

**Files:**

- Modify: `internal/sync/daemon.go`, `internal/sync/sync.go`
- Test: `internal/sync/wiring_test.go`

- [ ] **Step 1: Write the failing test (the engine flushes the outbox after a sync tick)**

```go
package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

func TestEngineFlushesOutboxAfterTick(t *testing.T) {
	db := store.OpenForTest(t) // helper added below
	dispatched := 0
	disp := func(ctx context.Context, e store.Event) error { dispatched++; return nil }

	// Enqueue an event as if a sync mutation produced one.
	_ = db.InTx(context.Background(), func(tx *store.Tx) error {
		return tx.EnqueueEvent(store.EventPROpened, json.RawMessage(`{}`))
	})

	flushOutbox(context.Background(), db, disp) // helper added below
	if dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1", dispatched)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sync/... -run TestEngineFlushesOutbox -v`
Expected: FAIL — `store.OpenForTest` / `flushOutbox` undefined.

- [ ] **Step 3: Implement the helpers + wiring**

Add to `internal/store/store.go`:

```go
// OpenForTest opens an in-temp-dir store for tests, registering cleanup.
func OpenForTest(t interface {
	TempDir() string
	Cleanup(func())
	Fatalf(string, ...any)
}) *DB {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
```

Add to `internal/sync/sync.go` a thin flush helper and a store handle on `Deps`:

```go
// flushOutbox drains the store's outbox through the dispatcher. Called at the
// end of each one-shot Sync and each daemon tick.
func flushOutbox(ctx context.Context, db *store.DB, dispatch store.DispatchFunc) {
	if db == nil {
		return
	}
	if err := db.RunOutbox(ctx, dispatch); err != nil {
		// I/O error draining the outbox; pending rows are retried next run.
		// (logged by the caller in daemon mode)
		_ = err
	}
}
```

Extend `Deps` (`sync.go:128` — currently `Cfg/VCS/CICD/Beads/StateDir/Now/Snapshot/AgentRegistry/SyncInterval`) with `Store *store.DB` and `Dispatch store.DispatchFunc` (both optional; `flushOutbox`'s nil-guard tolerates zero values). Construct them in the CLI wiring (`cmd/pg-pr/sync.go`), register the `beadsbridge` handler on the dispatcher.

**Correct flush call sites (review S2):** the daemon loop at `daemon.go:218-219` only runs `fingerprintTick` (the cheap change-detector) — it never mutates the store, so do NOT flush there. Flush after the code that actually enqueues events:

- end of `Engine.Sync` (before the return, ~`sync.go:578`, after the `Snapshot` block),
- end of `refreshPR` / `SyncPR` (the per-PR work the daemon's worker goroutines drain),
- in `maintenanceCycle` (`daemon.go:343`).

**Expect test fallout (review S3):** the `internal/sync` suite (large `sync_test.go`) constructs `Deps` literals; adding two optional fields keeps them compiling, but `go build ./... && go test ./internal/sync/...` is the gate — fix any constructor call sites the compiler flags.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sync/... -run TestEngineFlushesOutbox -v`
Expected: PASS.

- [ ] **Step 5: Build the whole tree + commit**

```bash
go build ./...
git add internal/sync/ internal/store/store.go cmd/pg-pr/
git commit -m "feat: wire store + dispatcher + outbox flush into sync/daemon"
```

---

## PHASE 3 — Provider extensions (net-new GitHub fields + minimize)

> From design-review B1: these fields are NOT fetched today. This phase adds them.

### Task 3.1: Add `isOutdated` / `isMinimized` / `minimizedReason` / `originalCommit.oid` to the enriched query

> Review S1: `originalCommit { oid }` is a field on `PullRequestReviewComment` (per **comment**), not on the thread. A code thread's `subject_sha` derives from its comments' `originalCommit.oid` (e.g. the first comment's). Confirm the node-count budget in `enrich.go` still holds with one extra leaf per comment (spec open question) — make it Step 4a below.

**Files:**

- Modify: `pkg/provider/vcs/github/enrich.go`
- Test: `pkg/provider/vcs/github/enrich_test.go`

- [ ] **Step 1: Read the current query** — open `enrich.go`, find `enrichedPRsQuery` (the `reviewThreads(first: ...)` block) and the structs the JSON unmarshals into.

- [ ] **Step 2: Write the failing test** — feed a canned GraphQL JSON response (containing `isOutdated: true`, a comment with `isMinimized: true`/`minimizedReason: "OUTDATED"`, and `originalCommit { oid }`) through the existing response-parsing function and assert the new fields land on the parsed type.

```go
func TestParseEnrichedSurfacesStalenessFields(t *testing.T) {
	const resp = `{"data":{"repository":{"pullRequest":{
	  "reviewThreads":{"nodes":[{"id":"t1","isResolved":false,"isOutdated":true,
	    "comments":{"nodes":[{"id":"c1","databaseId":11,"isMinimized":true,
	      "minimizedReason":"OUTDATED","originalCommit":{"oid":"deadbeef"}}]}}]}}}}}`
	got, err := parseEnrichedResponse([]byte(resp)) // existing or newly-extracted parser
	if err != nil { t.Fatalf("parse: %v", err) }
	thread := got.ReviewThreads[0]
	if !thread.IsOutdated { t.Fatal("IsOutdated not parsed") }
	if !thread.Comments[0].IsMinimized || thread.Comments[0].MinimizedReason != "OUTDATED" {
		t.Fatal("minimize fields not parsed")
	}
	if thread.Comments[0].OriginalCommitOID != "deadbeef" {
		t.Fatal("originalCommit.oid not parsed")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/provider/vcs/github/... -run TestParseEnrichedSurfacesStaleness -v`
Expected: FAIL — new fields/parser absent.

- [ ] **Step 4: Implement** — add `isOutdated` to the `reviewThreads` node selection, add `isMinimized minimizedReason originalCommit { oid }` to the comment selection, and add the matching Go struct fields (`IsOutdated bool`, `IsMinimized bool`, `MinimizedReason string`, `OriginalCommitOID string` via `originalCommit{oid}`). If the response parsing is inline, extract a `parseEnrichedResponse([]byte) (..., error)` so it's unit-testable. Verify the node-count budget note in `enrich.go` still holds (per-thread `originalCommit{oid}` is one extra leaf per comment).

- [ ] **Step 5: Run test + full provider tests**

Run: `go test ./pkg/provider/vcs/github/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/provider/vcs/github/enrich.go pkg/provider/vcs/github/enrich_test.go
git commit -m "feat: fetch isOutdated/isMinimized/originalCommit in enriched PR query"
```

### Task 3.2: Add a `MinimizeComment` mutation to the github provider

**Files:**

- Modify: `pkg/provider/vcs/github/github.go`
- Test: `pkg/provider/vcs/github/github_test.go`

- [ ] **Step 1: Find the existing `resolveReviewThreadMutation`** in `github.go` (around line 622) — mirror its structure.

- [ ] **Step 2: Write the failing test** — assert `MinimizeComment(ctx, repo, nodeID, "OUTDATED")` issues a `minimizeComment` mutation (use the package's existing GraphQL-client test harness/fake).

- [ ] **Step 3: Implement**

```go
// minimizeCommentMutation hides a comment with the given classifier
// (OUTDATED|RESOLVED|...). Mirrors resolveReviewThreadMutation.
const minimizeCommentMutation = `
mutation($id: ID!, $classifier: ReportedContentClassifiers!) {
  minimizeComment(input: {subjectId: $id, classifier: $classifier}) {
    minimizedComment { isMinimized }
  }
}`

// MinimizeComment marks a comment minimized with the given classifier.
func (p *Provider) MinimizeComment(ctx context.Context, nodeID, classifier string) error {
	// follow the same gh-api graphql invocation pattern resolveReviewThread uses
	// (variables: id=nodeID, classifier=classifier), return its error.
	...
}
```

- [ ] **Step 4: Run test + commit**

Run: `go test ./pkg/provider/vcs/github/... -run TestMinimize -v`

```bash
git add pkg/provider/vcs/github/github.go pkg/provider/vcs/github/github_test.go
git commit -m "feat: add minimizeComment mutation to github provider"
```

---

## PHASE 4 — Marker (HTML + stamp-everywhere + dual-match)

> From review B2/B3: must land before the Phase 5 `is_ours` ingest skip.

### Task 4.1: HTML marker + dual-match detection

**Files:**

- Create: `internal/marker/htmlmarker.go`
- Modify: `internal/marker/marker.go` (keep the legacy `🤖` for dual-match)
- Test: `internal/marker/htmlmarker_test.go`

- [ ] **Step 1: Write the failing test**

```go
package marker

import "testing"

func TestIsOursMatchesNewAndLegacyMarkers(t *testing.T) {
	if !IsOurs(Stamp("hello")) {
		t.Fatal("new HTML marker not recognized")
	}
	// Legacy emoji-marked bodies still recognized during transition.
	if !IsOurs("🤖 BEEP BOOP, an old pg-pr reply") {
		t.Fatal("legacy emoji marker not recognized")
	}
	if IsOurs("a human comment with no marker") {
		t.Fatal("false positive on unmarked body")
	}
	if Stamp("x") == "x" {
		t.Fatal("Stamp did not add the marker")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/marker/... -run TestIsOurs -v`
Expected: FAIL — `Stamp`/`IsOurs` undefined.

- [ ] **Step 3: Implement**

```go
// htmlmarker.go
package marker

import "strings"

// HTMLMarker is the invisible marker stamped on every body pg-pr/agents post.
// Invisible in the GitHub UI and unlikely to collide with anything a human types.
const HTMLMarker = "<!-- pg-pr -->"

// legacyGlyph is the pre-migration emoji marker (see marker.go). Recognized by
// IsOurs during the transition window so pre-switch pg-pr comments aren't
// re-ingested as human feedback.
const legacyGlyph = "🤖"

// Stamp prefixes body with the HTML marker (idempotent — won't double-stamp).
func Stamp(body string) string {
	if strings.Contains(body, HTMLMarker) {
		return body
	}
	return HTMLMarker + "\n" + body
}

// IsOurs reports whether body was produced by pg-pr/an agent — matching the new
// HTML marker OR the legacy emoji glyph.
func IsOurs(body string) bool {
	return strings.Contains(body, HTMLMarker) || strings.Contains(body, legacyGlyph)
}
```

- [ ] **Step 4: Run test + commit**

Run: `go test ./internal/marker/... -v`

```bash
git add internal/marker/
git commit -m "feat: HTML marker + dual-match IsOurs detection"
```

### Task 4.2: Stamp the marker on every posted body (reply path)

**Files:**

- Modify: `pkg/provider/vcs/github/github.go` (or wherever `ReplyToThread` builds the body) and `internal/sync/sync.go`'s reply path / `internal/replyposter` (Phase 6).

- [ ] **Step 1: Write a failing test** asserting that the reply-posting path wraps the body with `marker.Stamp` before calling the provider. (If the body is assembled in `processReplyDrafts`, test that function with a fake `ThreadReplier` capturing the posted body and assert `marker.IsOurs(posted)`.)

- [ ] **Step 2..4:** implement by calling `marker.Stamp(body)` wherever a body is finalized before posting. **Review N3: there are ~5 sites, not one** — switch the 4 `marker.Markerify` calls in `cmd/pg-pr/review.go` (lines ~143/147/255/342) to `marker.Stamp`, AND add stamping on the reply path (`processReplyDrafts` / the Phase-6 reply-poster, wherever the body is built before `ReplyToThread`). Grep to confirm no `Markerify` call remains on a posting path. Run the test green; commit. (`marker.IsOurs` still recognizes the legacy `🤖` for already-posted comments — see Task 4.1.)

```bash
git commit -m "feat: stamp pg-pr marker on every posted reply body"
```

---

## PHASE 5 — Ingestion → store

### Task 5.1: Author classifier (`author_kind`/`agent_name`/`is_ours`)

**Files:**

- Create: `internal/feedbackclassify/classify.go`
- Test: `internal/feedbackclassify/classify_test.go`

- [ ] **Step 1: Write the failing test**

```go
package feedbackclassify

import "testing"

func TestClassifyAuthor(t *testing.T) {
	reg := Registry{
		bots: map[string]BotPolicy{
			"coderabbitai[bot]": {AgentName: "coderabbit", DefaultSeverity: "warning"},
		},
	}
	cases := []struct {
		name      string
		login     string
		typename  string
		body      string
		self      string
		wantKind  string
		wantAgent string
		wantOurs  bool
	}{
		{"third-party bot", "coderabbitai[bot]", "Bot", "x", "phillipg", "agent", "coderabbit", false},
		{"self manual note", "phillipg", "User", "just a note", "phillipg", "human", "", false},
		{"our reply as self", "phillipg", "User", "<!-- pg-pr --> fixed", "phillipg", "agent", "pg-pr", true},
		{"teammate", "alice", "User", "lgtm?", "phillipg", "human", "", false},
		{"unknown bot fallback", "newbot[bot]", "Bot", "y", "phillipg", "agent", "other", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reg.Classify(c.login, c.typename, c.body, c.self)
			if got.Kind != c.wantKind || got.AgentName != c.wantAgent || got.IsOurs != c.wantOurs {
				t.Fatalf("Classify(%s) = %+v, want kind=%s agent=%s ours=%v",
					c.login, got, c.wantKind, c.wantAgent, c.wantOurs)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/feedbackclassify/... -run TestClassifyAuthor -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// Package feedbackclassify classifies comment authors (human vs agent, which
// agent, whether it's ours) and derives severity/fingerprint inputs from the
// config-driven agent registry.
package feedbackclassify

import (
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
)

// BotPolicy is the per-agent config (subset; full policy in agentregistry).
type BotPolicy struct {
	AgentName       string
	BodyMarker      string // optional regex/substring to disambiguate shared logins
	ManagedUpstream bool
	DefaultSeverity string
}

// Registry maps known agent logins to policy. Built from config.
type Registry struct {
	bots map[string]BotPolicy
}

// NewRegistry builds a classifier registry.
func NewRegistry(bots map[string]BotPolicy) Registry { return Registry{bots: bots} }

// Author is the classification result.
type Author struct {
	Kind            string // human | agent
	AgentName       string
	IsOurs          bool
	ManagedUpstream bool
	DefaultSeverity string
}

// Classify decides the author classification. self is the configured SelfLogin.
func (r Registry) Classify(login, typename, body, self string) Author {
	// Ours (pg-pr/agent-posted) is marker-detected, NOT login-based — pg-pr posts
	// under the user's own login.
	if marker.IsOurs(body) {
		return Author{Kind: "agent", AgentName: "pg-pr", IsOurs: true}
	}
	// Known configured bot?
	if pol, ok := r.bots[login]; ok {
		if pol.BodyMarker == "" || strings.Contains(body, pol.BodyMarker) {
			return Author{Kind: "agent", AgentName: pol.AgentName,
				ManagedUpstream: pol.ManagedUpstream, DefaultSeverity: pol.DefaultSeverity}
		}
	}
	// Bot/Mannequin typename → agent fallback.
	if typename == "Bot" || typename == "Mannequin" {
		return Author{Kind: "agent", AgentName: "other"}
	}
	// Otherwise human (self note included — author_role set by caller).
	return Author{Kind: "human"}
}
```

- [ ] **Step 4: Run test + commit**

Run: `go test ./internal/feedbackclassify/... -v`

```bash
git add internal/feedbackclassify/
git commit -m "feat: author classifier (human/agent/is_ours, marker-based)"
```

### Task 5.2: Fingerprint policy per kind

**Files:**

- Modify: `internal/feedbackclassify/classify.go`
- Test: `internal/feedbackclassify/fingerprint_test.go`

- [ ] **Step 1: Write the failing test** — `Fingerprint(kind, parts)` is revision-stable for `code-comment-thread`/`pr-comments` (same output across head SHAs) and revision-scoped for `ci-failure` (`check_name`+`subject_sha`).

```go
func TestFingerprintPolicy(t *testing.T) {
	// CI: check+sha → distinct per revision.
	a := Fingerprint("ci-failure", FPParts{CheckName: "build", SubjectSHA: "aaa"})
	b := Fingerprint("ci-failure", FPParts{CheckName: "build", SubjectSHA: "bbb"})
	if a == b { t.Fatal("ci-failure fingerprint must differ across SHAs") }

	// Code thread: stable across SHAs (path + normalized body).
	c1 := Fingerprint("code-comment-thread", FPParts{File: "x.go", Body: "fix this", SubjectSHA: "aaa"})
	c2 := Fingerprint("code-comment-thread", FPParts{File: "x.go", Body: "fix this", SubjectSHA: "bbb"})
	if c1 != c2 { t.Fatal("code-comment-thread fingerprint must be revision-stable") }
}
```

- [ ] **Step 2-4:** implement `Fingerprint` (sha256 over the kind-appropriate parts; CI includes `SubjectSHA`, code/pr exclude it; normalize body by trimming/space-collapsing), run green, commit.

```bash
git commit -m "feat: per-kind feedback fingerprint policy"
```

### Task 5.3a: Ingest upstream surfaces into the store (additive — store writes alongside existing beads)

Additive only: this writes `feedback` rows + enqueues events. It does NOT yet delete the bead-feedback paths (that's 5.3b, paired with the Phase-6 reply path) so the tree keeps compiling and the change stays small.

**Files:**

- Modify: `internal/sync/sync.go` (`processFeedback` + the comment/CI mapping; `processFeedback` is at `sync.go:1313`, `commentEvent` at `sync.go:1787`)
- Test: `internal/sync/ingest_test.go`

- [ ] **Step 1: Write the failing test** — drive `processFeedback` (or a new `ingestFeedback`) with a fake enriched PR (one human comment, one CodeRabbit comment, one of pg-pr's own marker-stamped replies, one CI failure) against an `internal/store` DB, then assert:
  - 3 feedback rows (our own reply skipped via `is_ours`),
  - CodeRabbit row: `author_kind=agent`, `agent_name=coderabbit`,
  - human row: `author_kind=human`,
  - CI row: `kind=ci-failure`, `subject_sha` set,
  - one `pending` outbox row of type `feedback.created`.

- [ ] **Step 2: Run red.**

- [ ] **Step 3: Implement** — in the feedback loop, per upstream item, in **one** `store.InTx`:
  - `classify.Classify(login, typename, body, selfLogin)`; **skip** items where `.IsOurs`,
  - `tx.UpsertPR(...)` then `tx.UpsertFeedback(...)` (classifier → `author_kind`/`agent_name`/`managed_upstream`/severity; `classify.Fingerprint(kind, parts)`; `subject_sha`/`is_outdated`/`is_minimized` from the Phase-3 provider fields),
  - `tx.EnqueueEvent(store.EventFeedbackCreated, payload)` — **same tx** as the upserts (uses the Task 1.8 `*Tx` variants; this is what makes rollback safe),
  - after the loop, `store.ReconcileStaleness(prID, headSHA)`.
    The pre-existing `c.CreateFeedback(...)` bead calls remain for now (removed in 5.3b).

- [ ] **Step 4: Run green; `go build ./... && go test ./internal/sync/...`.**

- [ ] **Step 5: Commit**

```bash
git commit -m "feat: ingest PR feedback into store (additive, skips is_ours)"
```

### Task 5.3b: Remove the bead-feedback + reply-draft paths (deletion; do AFTER Task 6.1)

> **Sequencing:** land Task 6.1 (store-backed reply path) first, then this deletion, so the reply pipeline is never broken in between. Review S4: this is a wide compile-fallout step — isolate it.

**Files:**

- Modify: `internal/sync/sync.go` (remove `processFeedback`'s `CreateFeedback`/`FindFeedbackByFingerprint`/`MarkFeedbackResolvedUpstream` calls; remove `processReplyDrafts` at `sync.go:1624` and its call sites `sync.go:490` and `sync.go:964`), the `BeadClient` interface (`sync.go:115-124` — drop `CreateFeedback`, `MarkFeedbackResolvedUpstream`, `ListFeedback`, `FindFeedbackByFingerprint`, `CloseFeedback`, `ListFeedbackPendingReply`, `SetResponseID`, `FindMergeRequestForFeedback`), and `daemon.go`'s `drainReplies`/`maintenanceCycle` (`daemon.go:343/351`).
- Optionally delete now-unused `pkg/beads/feedback.go` wrappers (or leave for the `migrate-feedback` backfill in 5.4 — keep `ListFeedback`/`CloseFeedback` until 5.4 runs, then remove).

- [ ] **Step 1: Delete the feedback-bead create/resolve calls in `processFeedback`** (kept in 5.3a). Build; fix references.
- [ ] **Step 2: Delete `processReplyDrafts` + its two call sites**; the Phase-6 reply-poster reconcile (Task 6.1) is now the only reply path. Build.
- [ ] **Step 3: Prune the `BeadClient` interface** of the dropped methods; build; fix the fakes in `internal/sync/*_test.go`.
- [ ] **Step 4: Run `go build ./... && go test ./...`;** fix fallout.
- [ ] **Step 5: Commit**

```bash
git commit -m "refactor: remove bead-stored feedback + reply-draft paths"
```

### Task 5.4: Backfill existing feedback beads + kind remap

**Files:**

- Create: `internal/store/backfill.go` + a `pg-pr migrate-feedback` one-shot command in `cmd/pg-pr/`
- Test: `internal/store/backfill_test.go`

- [ ] **Step 1: Write the failing test** — given a fake bead `Runner` returning a `feedback` bead of **each** live `beads.FeedbackKind` (`comment-thread`, `review-thread`, `ci-failure`, `review-request`, `jira-link` — `pkg/beads/types.go:20`), `Backfill` creates `feedback` rows with kinds remapped per this **complete** table and closes each bead with reason `migrated-to-store`:

  | bead kind        | store kind            |
  | ---------------- | --------------------- |
  | `comment-thread` | `pr-comments`         |
  | `review-thread`  | `code-comment-thread` |
  | `ci-failure`     | `ci-failure`          |
  | `review-request` | `review-request`      |
  | `jira-link`      | `jira-link`           |

  Review S5: an **unmapped** bead kind must be a hard error (not a silent default) — a bad `kind` would violate the `feedback.kind` CHECK at insert time. Add a test asserting an unknown kind returns an error.

- [ ] **Step 2-4:** implement `Backfill(ctx, beadsClient, store)` using the explicit remap (error on unknown kind), wire a hidden `pg-pr migrate-feedback` command, run green, commit.

```bash
git commit -m "feat: backfill feedback beads into store with kind remap"
```

---

## PHASE 6 — Reply path (reply-poster handler + reconcile re-scan)

### Task 6.1: Reply-poster reconcile

**Files:**

- Create: `internal/replyposter/poster.go`
- Test: `internal/replyposter/poster_test.go`

- [ ] **Step 1: Write the failing test** — with a store holding one `pr-comments` feedback that has `reply_body` set + `response_id` empty, and a fake `ThreadReplier` that returns a comment id, `Reconcile(ctx)` posts the (marker-stamped) reply and calls `MarkReplied`. A second `Reconcile` posts nothing (idempotent via `response_id`). A `managed_upstream` item is skipped.

```go
func TestReconcilePostsPendingReplyOnce(t *testing.T) {
	db := store.OpenForTest(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})
	id, _ := db.UpsertFeedback(ctx, store.Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "f", ExternalID: "c1"})
	_ = db.SetDisposition(ctx, id, "wont-fix", "intentional", "Not acting — intentional.")

	fake := &fakeReplier{id: "resp-9"}
	p := New(db, fake)
	if err := p.Reconcile(ctx); err != nil { t.Fatalf("Reconcile: %v", err) }
	if fake.posts != 1 { t.Fatalf("posts = %d, want 1", fake.posts) }
	if !marker.IsOurs(fake.lastBody) { t.Fatal("posted body not marker-stamped") }

	_ = p.Reconcile(ctx) // idempotent
	if fake.posts != 1 { t.Fatalf("second reconcile posted again: posts = %d", fake.posts) }
}
```

- [ ] **Step 2: Run red.**

- [ ] **Step 3: Implement** — `Reconcile` calls `db.ListPendingReplies`, skips `ManagedUpstream`, stamps via `marker.Stamp`, posts via the `ThreadReplier` (thread `external_id` for thread kinds; comment for `pr-comments`), and on success calls `db.MarkReplied(id, responseID)`. Best-effort: log + continue on a post failure (the row stays pending → retried next reconcile).

- [ ] **Step 4: Run green.**

- [ ] **Step 5: Wire `Reconcile` into the daemon's `maintenanceCycle`** (replacing the old `processReplyDrafts`/`drainReplies` bead path in `daemon.go:343`) and into one-shot `SyncPR`.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat: reply-poster reconcile over store (replaces bead reply pipeline)"
```

---

## PHASE 7 — Agent CLI

### Task 7.1: `pg-pr feedback list/show/disposition`

**Files:**

- Create: `cmd/pg-pr/feedback.go`
- Test: `cmd/pg-pr/feedback_test.go`

- [ ] **Step 1: Write the failing test** — using cobra's `rootCmd` with output captured, `pg-pr feedback list <repo> <pr> --json` against a seeded store prints the feedback rows as JSON including `author_kind`/`agent_name`; `pg-pr feedback show <id> --json` includes the thread messages; `pg-pr feedback disposition <id> --action=will-fix --note=x --reply=y` updates the store + enqueues + flushes.

- [ ] **Step 2: Run red.**

- [ ] **Step 3: Implement** — a `feedbackCmd` with `list`/`show`/`disposition` subcommands registered in `init()` (mirroring how other `cmd/pg-pr/*.go` commands self-register on `rootCmd`). `disposition` opens the store and, in **one** `store.InTx`, calls `tx.SetDisposition(...)` then `tx.EnqueueEvent(store.EventFeedbackDisposed, ...)` (the Task 1.8 `*Tx` variants — atomic), then inline-flushes the outbox via the dispatcher (with `--no-flush` to skip). Each command resolves the store path from config/XDG (same resolution as `internal/config`).

```go
func init() { rootCmd.AddCommand(newFeedbackCmd()) }
```

- [ ] **Step 4: Run green; build; commit.**

```bash
go build ./...
git commit -m "feat: pg-pr feedback list/show/disposition CLI"
```

### Task 7.2: Update the feedback-processing instructions

**Files:**

- Modify: the feedback-processing prompt/skill the process-feedback bead points agents at (locate via `rg -l 'process-feedback' ~/.claude` and the repo's skills).

- [ ] **Step 1:** change the instructions from "read the feedback beads under this PR" to "run `pg-pr feedback list <repo> <pr> --json`, work each item, and `pg-pr feedback disposition <id> --action=… --note=…` for each." Keep the "spawn work beads" step unchanged.
- [ ] **Step 2:** commit.

```bash
git commit -m "docs: feedback-processing pulls from pg-pr, marks each item"
```

---

## PHASE 8 — Config-driven agent registry

### Task 8.1: Extend `agentregistry.Entry` with agent_name + body_marker + policy

**Files:**

- Modify: `internal/agentregistry/registry.go`
- Test: `internal/agentregistry/registry_test.go`

- [ ] **Step 1: Write the failing test** — an `Entry` with `AgentName`, `BodyMarker`, and a `Policy{ManagedUpstream, Reply, Resolve, Minimize, Ingest, DefaultSeverity}` round-trips from YAML, and `Registry.PolicyFor(login)`/`AgentName(login, body)` return the configured values; an unknown `[bot]` login returns the conservative fallback (`agent_name=other`, ingest=true).

- [ ] **Step 2: Run red.**

- [ ] **Step 3: Implement** — extend `Entry`:

```go
type Entry struct {
	Login         string `yaml:"login" json:"login"`
	AgentName     string `yaml:"agent_name" json:"agent_name"`
	BodyMarker    string `yaml:"body_marker,omitempty" json:"body_marker,omitempty"`
	ApprovalRegex string `yaml:"approval_regex,omitempty" json:"approval_regex,omitempty"`
	Policy        Policy `yaml:"policy" json:"policy"`
}

type Policy struct {
	Ingest          bool   `yaml:"ingest" json:"ingest"`
	ManagedUpstream bool   `yaml:"managed_upstream" json:"managed_upstream"`
	Reply           bool   `yaml:"reply" json:"reply"`
	Resolve         bool   `yaml:"resolve" json:"resolve"`
	Minimize        bool   `yaml:"minimize" json:"minimize"`
	DefaultSeverity string `yaml:"default_severity,omitempty" json:"default_severity,omitempty"`
}
```

Add `PolicyFor(login string) (Policy, bool)` and a `ToClassifyRegistry()` adapter that builds the `feedbackclassify.Registry` (`map[login]BotPolicy`) so the classifier is config-driven. Keep `IsAgent`/`MatchApproval` working (the dashboard uses them). Preserve back-compat: an `Entry` with only `login`+`approval_regex` (today's shape) still loads (new fields default).

- [ ] **Step 4: Run green; run `internal/config` tests** (`go test ./internal/config/...`) since they parse `Entry`; fix any fixtures.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat: agent registry carries agent_name + body_marker + response policy"
```

### Task 8.2: Generate ZR bot entries from the `pg-pr-zr` nix module

**Files:**

- Modify (in the **`phillipg-nix-ziprecruiter`** repo): `modules/pg-pr-zr/default.nix` (the config.yaml generator)

- [ ] **Step 1:** add an `agents:` list to the generated `config.yaml` with entries for `coderabbitai[bot]` (agent_name coderabbit), `copilot-pull-request-reviewer[bot]` (copilot), and `github-actions[bot]` + `body_marker: "<!-- claude-pr-review -->"` (agent_name claude-review, `managed_upstream: true`). These ZR-specific identities live ONLY here, never in `phillipgreenii-nix-agent-support` source.
- [ ] **Step 2:** `nix flake check` in `phillipg-nix-ziprecruiter`; confirm the generated `config.yaml` validates with `pg-pr config validate`.
- [ ] **Step 3: Commit** (in that repo).

```bash
git commit -m "feat: generate pg-pr agent registry entries for ZR bots"
```

---

## Self-Review

**Spec coverage** (each spec section → task):

- SQLite store / `pull_request` / `feedback` / `code_comment_message` / `outbox` → Tasks 1.1–1.7.
- Library-first, daemon-optional; outbox flush both paths → Task 2.3.
- In-process handlers, isolated failures → Task 2.1.
- Transactional outbox (rollback → no dispatch; fire-once) → Task 1.7; atomic mutate+enqueue (`*Tx` variants) → Task 1.8.
- beads handler relocation; no feedback beads; preserve `FindOpenProcessingCycle` error-propagation → Task 2.2.
- Provider net-new fields (`isOutdated`/`isMinimized`/`minimizedReason`/`originalCommit.oid`) + `minimizeComment` → Tasks 3.1–3.2.
- Marker: HTML + stamp-everywhere + dual-match, sequenced before is_ours skip → Tasks 4.1–4.2 (precede 5.3).
- Author classification (`author_kind`/`agent_name`/`is_ours` marker-based) → Task 5.1.
- Per-kind fingerprint policy → Task 5.2.
- Ingestion → store; staleness reconcile; skip is_ours → Task 5.3a; bead-feedback/reply-draft removal → Task 5.3b (after Task 6.1).
- Migration backfill + kind remap → Task 5.4.
- Reply path: reconcile re-scan, idempotent via response_id, managed_upstream skip → Task 6.1.
- Agent CLI; updated instructions → Tasks 7.1–7.2.
- Config-driven agent registry; ZR generation; standalone fallback → Tasks 8.1–8.2.

**Deferred (not in this plan, per spec):** draft-review generation, enrichment, mine/teammate review split, attention signals, full `revision` table, optional `ci_run` child table.

**Known coarse tasks** (acknowledged, not placeholders): Tasks 3.1/3.2/4.2/5.3a/5.3b/5.4/6.1/7.1 modify existing files whose exact line anchors the implementer must read first (the plan names the function + the change + test). These touch code shapes (`enrich.go` query string, `processFeedback` body, `ReplyToThread` signature) that the implementer should read in-file before editing; the test in each task pins the observable behavior.

**Execution order (dependency, not strict phase order):** the deletion in **Task 5.3b must run after Task 6.1** (the store reply path must exist before the bead reply path is removed). Recommended sequence: 1.1→1.8, 2.1→2.3, 3.1→3.2, 4.1→4.2, 5.1→5.2→5.3a, 6.1, 5.3b, 5.4, 7.1→7.2, 8.1→8.2.

**Plan-review resolutions (2026-06-23, folded in):**

- **B1 (blocking):** added Task 1.8 `*Tx` mutator variants so ingestion (5.3a) and disposition (7.1) write state + enqueue in one transaction (rollback safety). Wired into 5.3a/7.1.
- **S1:** `originalCommit { oid }` is per-**comment**; node-budget confirmation added to Task 3.1.
- **S2:** flush after `refreshPR`/`SyncPR`/`maintenanceCycle`, NOT after `fingerprintTick` (which never mutates). `Engine.Sync` returns ~`sync.go:578`.
- **S3:** `Deps` grows two optional fields; expect `internal/sync` test-construction fallout (build gate).
- **S4:** split Task 5.3 → 5.3a (additive write+enqueue) / 5.3b (delete bead-feedback + reply-draft paths, after 6.1).
- **S5:** backfill enumerates the full live kind set; unknown kind is a hard error.
- **N1:** dropped the dead `errAlreadyInTx`/`BEGIN EXCLUSIVE` from `applyMigration`.
- **N2/N3:** `b2i` reformatted; Task 4.2 switches all ~5 `Markerify` sites (incl. `review.go`) to `Stamp`.
