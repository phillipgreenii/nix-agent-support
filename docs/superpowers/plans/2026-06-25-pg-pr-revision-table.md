# pg-pr Revision Table Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-PR append-only `pr_revision` timeline to pg-pr's store (head/base SHA + compact CI rollup + my-submitted-review marker), exposed via the store API for #1 (pg2-4c5i.12) and #3 (pg2-4c5i.13).

**Architecture:** One new SQLite table `pr_revision` (child of `pull_request`, `ON DELETE CASCADE`), written only by the sync refresh path. A new `internal/store/revision.go` holds the model + append-by-observation logic + read methods, mirroring the existing `Tx`/`InTx` pattern. Sync gains three touch-points after its existing `UpsertPR`: capture `base_sha`, record the revision, attach CI, mark my submitted review.

**Tech Stack:** Go, `modernc.org/sqlite` (pure-Go, no cgo), `database/sql` with `user_version` migrations.

**Spec:** `docs/superpowers/specs/2026-06-25-pg-pr-revision-table-design.md`

## Global Constraints

- SQLite via `modernc.org/sqlite`; single-conn pool; migrations are ordered DDL strings in `internal/store/migrate.go` with `schemaVersion` bumped and `user_version` set per step (one `Tx` per migration).
- Time is `nowRFC3339()` (`internal/store/pull_request.go`) — overridable in tests; never call `time.Now()` directly in store code.
- Store methods take `context.Context` first; transactional work goes through `db.InTx(ctx, func(*Tx) error)` (`internal/store/outbox.go:19`); `Tx` exposes `Exec` and `QueryRow`.
- TDD: every task writes a failing test first, runs it red, implements minimally, runs it green, commits.
- Run tests with `go test ./...` from `packages/pg-pr`; the per-source digest versioning means no `vendorHash` work.
- Completion gate for the repo: `nix flake check` (and the workspace `pn workspace build`) must pass before the feature is done.

---

### Task 1: Schema migration v2 → v3 (`pr_revision` table)

**Files:**

- Modify: `internal/store/migrate.go` (bump `schemaVersion`; append migration string)
- Test: `internal/store/migrate_test.go`

**Interfaces:**

- Produces: the `pr_revision` table + `idx_pr_revision_pr` index; `schemaVersion == 3`.

- [ ] **Step 1: Write the failing test** — append to `internal/store/migrate_test.go`:

```go
func TestMigrate_V3RevisionTable(t *testing.T) {
	db := OpenForTest(t)

	// Table exists.
	var name string
	if err := db.sql.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='pr_revision'",
	).Scan(&name); err != nil {
		t.Fatalf("pr_revision table missing: %v", err)
	}

	// CHECK on ci_state is enforced.
	_, err := db.sql.Exec(`INSERT INTO pull_request
		(repo,number,ownership,state,head_sha,created_at,updated_at)
		VALUES ('o/r',1,'mine','open','sha1','t','t')`)
	if err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	var prID int64
	_ = db.sql.QueryRow("SELECT id FROM pull_request WHERE repo='o/r' AND number=1").Scan(&prID)
	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,observed_at,last_seen_at,ci_state)
		VALUES (?,?,?,?,?,?)`, prID, 1, "sha1", "t", "t", "bogus"); err == nil {
		t.Fatal("expected ci_state CHECK to reject 'bogus'")
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestMigrate_V3RevisionTable -v`
Expected: FAIL — `pr_revision table missing`.

- [ ] **Step 3: Implement the migration** — in `internal/store/migrate.go`, change `const schemaVersion = 2` to `= 3` and append this string as a new element of `migrations` (after the v1→v2 enrichment block):

```go
	// v2 -> v3: per-PR revision timeline (head/base SHA + compact CI rollup +
	// my-submitted-review marker). Append-only; one writer (sync).
	`
CREATE TABLE pr_revision (
    id              INTEGER PRIMARY KEY,
    pr_id           INTEGER NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    head_sha        TEXT NOT NULL,
    base_sha        TEXT,
    observed_at     TEXT NOT NULL,
    last_seen_at    TEXT NOT NULL,
    ci_state        TEXT NOT NULL DEFAULT 'none'
                      CHECK (ci_state IN ('none','pending','success','failure','error')),
    ci_passed       INTEGER NOT NULL DEFAULT 0,
    ci_failed       INTEGER NOT NULL DEFAULT 0,
    ci_pending      INTEGER NOT NULL DEFAULT 0,
    ci_captured_at  TEXT,
    reviewed_at     TEXT,
    my_review_state TEXT CHECK (my_review_state IS NULL OR
                      my_review_state IN ('approved','changes-requested','commented')),
    UNIQUE (pr_id, seq)
);
CREATE INDEX idx_pr_revision_pr ON pr_revision(pr_id);
`,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run 'TestMigrate' -v`
Expected: PASS (incl. the existing `TestMigrateSetsUserVersionAndIsIdempotent`, which asserts `user_version == schemaVersion == 3`).

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrate.go internal/store/migrate_test.go
git commit -m "feat(pg-pr): add pr_revision table (schema v3)"
```

---

### Task 2: `Revision` model + `RecordRevision` append rule

**Files:**

- Create: `internal/store/revision.go`
- Test: `internal/store/revision_test.go`

**Interfaces:**

- Consumes: `pr_revision` table (Task 1); `db.InTx`, `Tx.Exec`, `Tx.QueryRow`; `nowRFC3339`.
- Produces:
  - `type Revision struct { ID, PRID int64; Seq int; HeadSHA, BaseSHA, ObservedAt, LastSeenAt, CIState string; CIPassed, CIFailed, CIPending int; CICapturedAt, ReviewedAt, MyReviewState string }`
  - `func (db *DB) RecordRevision(ctx context.Context, prID int64, headSHA, baseSHA string) (Revision, bool, error)` — appends a new revision when `(headSHA, baseSHA)` differs from the latest (NULL base falls back to head-only compare), else touches `last_seen_at`; returns the resulting latest revision + whether a row was appended.

- [ ] **Step 1: Write the failing test** — create `internal/store/revision_test.go`:

```go
package store

import (
	"context"
	"testing"
)

func seedPR(t *testing.T, db *DB) int64 {
	t.Helper()
	id, err := db.UpsertPR(context.Background(), PullRequest{
		Repo: "o/r", Number: 1, Ownership: "mine", State: "open", HeadSHA: "h1",
	})
	if err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	return id
}

func TestRecordRevision_AppendsAndTouches(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	// First observation seeds seq=1.
	r, appended, err := db.RecordRevision(ctx, prID, "h1", "b1")
	if err != nil || !appended || r.Seq != 1 {
		t.Fatalf("seed: r=%+v appended=%v err=%v", r, appended, err)
	}

	// Identical pair -> touch, no append.
	r2, appended2, err := db.RecordRevision(ctx, prID, "h1", "b1")
	if err != nil || appended2 || r2.Seq != 1 {
		t.Fatalf("touch: r=%+v appended=%v err=%v", r2, appended2, err)
	}

	// Head change -> append seq=2.
	r3, appended3, _ := db.RecordRevision(ctx, prID, "h2", "b1")
	if !appended3 || r3.Seq != 2 {
		t.Fatalf("head change: r=%+v appended=%v", r3, appended3)
	}

	// Base change under same head -> append seq=3.
	r4, appended4, _ := db.RecordRevision(ctx, prID, "h2", "b2")
	if !appended4 || r4.Seq != 3 {
		t.Fatalf("base change: r=%+v appended=%v", r4, appended4)
	}

	// Force-push back to an earlier pair -> NEW row (re-introduction), seq=4.
	r5, appended5, _ := db.RecordRevision(ctx, prID, "h1", "b1")
	if !appended5 || r5.Seq != 4 {
		t.Fatalf("re-introduction: r=%+v appended=%v", r5, appended5)
	}
}

func TestRecordRevision_NullBaseFallsBackToHead(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if _, appended, _ := db.RecordRevision(ctx, prID, "h1", ""); !appended {
		t.Fatal("first seed should append")
	}
	// Same head, still no base -> touch only (no spurious append).
	if _, appended, _ := db.RecordRevision(ctx, prID, "h1", ""); appended {
		t.Fatal("null base + same head must not append")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestRecordRevision -v`
Expected: FAIL — `db.RecordRevision undefined`.

- [ ] **Step 3: Implement `internal/store/revision.go`:**

```go
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Revision is one observed (head_sha, base_sha) of a PR in time order.
type Revision struct {
	ID            int64
	PRID          int64
	Seq           int
	HeadSHA       string
	BaseSHA       string // "" when NULL
	ObservedAt    string
	LastSeenAt    string
	CIState       string
	CIPassed      int
	CIFailed      int
	CIPending     int
	CICapturedAt  string // "" when NULL
	ReviewedAt    string // "" when NULL
	MyReviewState string // "" when NULL
}

const revisionColumns = `id, pr_id, seq, head_sha, COALESCE(base_sha,''),
	observed_at, last_seen_at, ci_state, ci_passed, ci_failed, ci_pending,
	COALESCE(ci_captured_at,''), COALESCE(reviewed_at,''), COALESCE(my_review_state,'')`

func scanRevision(s rowScanner) (Revision, error) {
	var r Revision
	err := s.Scan(&r.ID, &r.PRID, &r.Seq, &r.HeadSHA, &r.BaseSHA,
		&r.ObservedAt, &r.LastSeenAt, &r.CIState, &r.CIPassed, &r.CIFailed,
		&r.CIPending, &r.CICapturedAt, &r.ReviewedAt, &r.MyReviewState)
	return r, err
}

// samePair reports whether two (head, base) pairs are the same revision. A NULL
// (empty) base on either side degrades to a head-only comparison.
func samePair(aHead, aBase, bHead, bBase string) bool {
	if aHead != bHead {
		return false
	}
	if aBase == "" || bBase == "" {
		return true
	}
	return aBase == bBase
}

// RecordRevision appends a new revision when (headSHA, baseSHA) differs from the
// PR's latest revision, otherwise bumps the latest revision's last_seen_at. It
// returns the resulting latest revision and whether a new row was appended.
func (db *DB) RecordRevision(ctx context.Context, prID int64, headSHA, baseSHA string) (Revision, bool, error) {
	var result Revision
	var appended bool
	err := db.InTx(ctx, func(tx *Tx) error {
		now := nowRFC3339()
		latest, err := tx.latestRevision(prID)
		if err != nil {
			return err
		}
		if latest != nil && samePair(latest.HeadSHA, latest.BaseSHA, headSHA, baseSHA) {
			if _, err := tx.Exec(`UPDATE pr_revision SET last_seen_at=? WHERE id=?`, now, latest.ID); err != nil {
				return fmt.Errorf("store: touch revision: %w", err)
			}
			latest.LastSeenAt = now
			result = *latest
			return nil
		}
		seq := 1
		if latest != nil {
			seq = latest.Seq + 1
		}
		var base any
		if baseSHA != "" {
			base = baseSHA
		}
		res, err := tx.Exec(`INSERT INTO pr_revision
			(pr_id, seq, head_sha, base_sha, observed_at, last_seen_at)
			VALUES (?,?,?,?,?,?)`, prID, seq, headSHA, base, now, now)
		if err != nil {
			return fmt.Errorf("store: append revision: %w", err)
		}
		id, _ := res.LastInsertId()
		appended = true
		result = Revision{ID: id, PRID: prID, Seq: seq, HeadSHA: headSHA,
			BaseSHA: baseSHA, ObservedAt: now, LastSeenAt: now, CIState: "none"}
		return nil
	})
	return result, appended, err
}

// latestRevision returns the highest-seq revision for prID, or nil if none.
func (t *Tx) latestRevision(prID int64) (*Revision, error) {
	row := t.QueryRow(`SELECT `+revisionColumns+`
		FROM pr_revision WHERE pr_id=? ORDER BY seq DESC LIMIT 1`, prID)
	r, err := scanRevision(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest revision: %w", err)
	}
	return &r, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestRecordRevision -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/revision.go internal/store/revision_test.go
git commit -m "feat(pg-pr): RecordRevision append-by-observation timeline"
```

---

### Task 3: CI rollup + read methods (`SetRevisionCI`, `ListRevisions`, `LatestRevision`)

**Files:**

- Modify: `internal/store/revision.go`
- Test: `internal/store/revision_test.go`

**Interfaces:**

- Consumes: `Revision`, `revisionColumns`, `scanRevision`, `Tx.latestRevision` (Task 2).
- Produces:
  - `type CIRollup struct { State string; Passed, Failed, Pending int; CapturedAt string }`
  - `func (db *DB) SetRevisionCI(ctx context.Context, revisionID int64, r CIRollup) error`
  - `func (db *DB) ListRevisions(ctx context.Context, prID int64) ([]Revision, error)` — ascending `seq`.
  - `func (db *DB) LatestRevision(ctx context.Context, prID int64) (*Revision, error)`

- [ ] **Step 1: Write the failing test** — append to `internal/store/revision_test.go`:

```go
func TestSetRevisionCIAndReads(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)
	r1, _, _ := db.RecordRevision(ctx, prID, "h1", "b1")
	_, _, _ = db.RecordRevision(ctx, prID, "h2", "b1")

	if err := db.SetRevisionCI(ctx, r1.ID, CIRollup{
		State: "failure", Passed: 3, Failed: 1, Pending: 0, CapturedAt: "t",
	}); err != nil {
		t.Fatalf("SetRevisionCI: %v", err)
	}

	revs, err := db.ListRevisions(ctx, prID)
	if err != nil || len(revs) != 2 {
		t.Fatalf("ListRevisions: n=%d err=%v", len(revs), err)
	}
	if revs[0].Seq != 1 || revs[1].Seq != 2 {
		t.Fatalf("not ascending: %d,%d", revs[0].Seq, revs[1].Seq)
	}
	if revs[0].CIState != "failure" || revs[0].CIFailed != 1 {
		t.Fatalf("CI not stored: %+v", revs[0])
	}

	latest, _ := db.LatestRevision(ctx, prID)
	if latest == nil || latest.Seq != 2 {
		t.Fatalf("LatestRevision: %+v", latest)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSetRevisionCIAndReads -v`
Expected: FAIL — `db.SetRevisionCI undefined`.

- [ ] **Step 3: Implement** — append to `internal/store/revision.go`:

```go
// CIRollup is the compact CI summary captured for a revision's head SHA.
type CIRollup struct {
	State      string // none|pending|success|failure|error
	Passed     int
	Failed     int
	Pending    int
	CapturedAt string
}

// SetRevisionCI overwrites the CI rollup on a revision (idempotent).
func (db *DB) SetRevisionCI(ctx context.Context, revisionID int64, r CIRollup) error {
	state := r.State
	if state == "" {
		state = "none"
	}
	var capturedAt any
	if r.CapturedAt != "" {
		capturedAt = r.CapturedAt
	}
	_, err := db.sql.ExecContext(ctx, `UPDATE pr_revision
		SET ci_state=?, ci_passed=?, ci_failed=?, ci_pending=?, ci_captured_at=?
		WHERE id=?`, state, r.Passed, r.Failed, r.Pending, capturedAt, revisionID)
	if err != nil {
		return fmt.Errorf("store: set revision ci %d: %w", revisionID, err)
	}
	return nil
}

// ListRevisions returns a PR's revisions in ascending seq order.
func (db *DB) ListRevisions(ctx context.Context, prID int64) ([]Revision, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+revisionColumns+`
		FROM pr_revision WHERE pr_id=? ORDER BY seq ASC`, prID)
	if err != nil {
		return nil, fmt.Errorf("store: list revisions %d: %w", prID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Revision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan revision: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestRevision returns the highest-seq revision for a PR, or nil if none.
func (db *DB) LatestRevision(ctx context.Context, prID int64) (*Revision, error) {
	row := db.sql.QueryRowContext(ctx, `SELECT `+revisionColumns+`
		FROM pr_revision WHERE pr_id=? ORDER BY seq DESC LIMIT 1`, prID)
	r, err := scanRevision(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest revision %d: %w", prID, err)
	}
	return &r, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run 'TestSetRevisionCIAndReads|TestRecordRevision' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/revision.go internal/store/revision_test.go
git commit -m "feat(pg-pr): revision CI rollup + ListRevisions/LatestRevision"
```

---

### Task 4: `MarkRevisionReviewed` (submitted-review marker)

**Files:**

- Modify: `internal/store/revision.go`
- Test: `internal/store/revision_test.go`

**Interfaces:**

- Consumes: `Revision`, `Tx` (Task 2).
- Produces: `func (db *DB) MarkRevisionReviewed(ctx context.Context, prID int64, headSHA, state, reviewedAt string) error` — sets `reviewed_at`/`my_review_state` on the **latest (max seq)** revision whose `head_sha` matches; no-op if none matches.

- [ ] **Step 1: Write the failing test** — append to `internal/store/revision_test.go`:

```go
func TestMarkRevisionReviewed_LatestMatchingSHA(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)
	_, _, _ = db.RecordRevision(ctx, prID, "h1", "b1") // seq 1
	_, _, _ = db.RecordRevision(ctx, prID, "h2", "b1") // seq 2
	_, _, _ = db.RecordRevision(ctx, prID, "h1", "b1") // seq 3 (re-introduced h1)

	if err := db.MarkRevisionReviewed(ctx, prID, "h1", "approved", "t"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	revs, _ := db.ListRevisions(ctx, prID)
	// seq 3 (latest h1) is marked; seq 1 (older h1) is not.
	if revs[2].MyReviewState != "approved" || revs[2].ReviewedAt != "t" {
		t.Fatalf("latest h1 not marked: %+v", revs[2])
	}
	if revs[0].MyReviewState != "" {
		t.Fatalf("older h1 should be untouched: %+v", revs[0])
	}

	// No matching SHA -> no-op (no error).
	if err := db.MarkRevisionReviewed(ctx, prID, "nope", "approved", "t"); err != nil {
		t.Fatalf("no-match should be no-op: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestMarkRevisionReviewed -v`
Expected: FAIL — `db.MarkRevisionReviewed undefined`.

- [ ] **Step 3: Implement** — append to `internal/store/revision.go`:

```go
// MarkRevisionReviewed records my submitted review at headSHA on the latest
// revision whose head_sha matches (a head SHA can recur after a force-push; #3
// cares about the most recent occurrence). No-op if no revision matches.
func (db *DB) MarkRevisionReviewed(ctx context.Context, prID int64, headSHA, state, reviewedAt string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE pr_revision
		SET reviewed_at=?, my_review_state=?
		WHERE id = (SELECT id FROM pr_revision
		            WHERE pr_id=? AND head_sha=? ORDER BY seq DESC LIMIT 1)`,
		reviewedAt, state, prID, headSHA)
	if err != nil {
		return fmt.Errorf("store: mark revision reviewed %d %s: %w", prID, headSHA, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -v`
Expected: PASS (whole store package).

- [ ] **Step 5: Commit**

```bash
git add internal/store/revision.go internal/store/revision_test.go
git commit -m "feat(pg-pr): MarkRevisionReviewed (latest matching head SHA)"
```

---

### Task 5: Sync integration (capture base_sha + record/CI/review wiring)

**Files:**

- Modify: `pkg/provider/vcs/github/enrich.go` (GraphQL query + node struct + payload: add `baseRefOid` → a `BaseSHA` field)
- Modify: `internal/sync/sync.go` (the per-PR refresh loop, after `UpsertPR` ~line 1154)
- Test: `internal/sync/sync_test.go` (or a new `internal/sync/revision_test.go`)

**Interfaces:**

- Consumes: `store.RecordRevision`, `store.SetRevisionCI`, `store.MarkRevisionReviewed`, `store.CIRollup` (Tasks 2–4); the existing CI source sync already uses for `ci-failure` feedback (the dedicated CICD provider, NOT GraphQL `statusCheckRollup` — see `sync.go:764`); `api.Review` (`reviewsFromGHNode`, `enrich.go:499`).
- Produces: revisions populated during normal sync.

> **Discovery step (do first, before coding):** read `pkg/provider/vcs/github/enrich.go` around the GraphQL query (`:119`), the node struct (`:271`), `reviewsFromGHNode`/`api.Review` (`:396`,`:499`), and `ciRunsFromGHNode` (`:559`); and `internal/sync/sync.go` around the per-PR loop (`:1136`–`:1160`). Confirm: (a) where `head_sha` for the current PR is available post-`UpsertPR` (it's on the `PullRequest` row / enrich payload); (b) the field on `api.Review` giving the reviewer login, the submitted state, and the **commit SHA** the review targeted — if the commit SHA is absent, add `commit { oid }` to the reviews sub-query in the GraphQL and surface it on `api.Review`; (c) the configured "my login" used elsewhere to classify `is_ours` (reuse it, do not hardcode).

- [ ] **Step 1: Write the failing test** — a sync-level test that drives a fake provider returning a PR with a head SHA + base SHA + CI + one of my submitted reviews, and asserts a revision row is recorded with CI and the review marker. Model it on the existing sync test fakes (see `internal/sync/sync_test.go` and `ci_truncation_test.go` for the provider-fake + store-assert pattern):

```go
func TestSync_RecordsRevisionWithCIAndReview(t *testing.T) {
	// Arrange: fake provider yields PR o/r#1 head=h1 base=b1, CI {pass:2,fail:1},
	// and one submitted review by ME (state APPROVED) at commit h1.
	// (Use the same fake-provider + store harness as the existing sync tests.)
	env, db := newSyncTestEnv(t /* … existing helper … */)
	// … configure the fake to return the PR/CI/review …

	if err := env.RunSync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	prID := mustPRID(t, db, "o/r", 1)
	revs, _ := db.ListRevisions(context.Background(), prID)
	if len(revs) != 1 || revs[0].HeadSHA != "h1" || revs[0].BaseSHA != "b1" {
		t.Fatalf("revision not recorded: %+v", revs)
	}
	if revs[0].CIState != "failure" || revs[0].CIFailed != 1 {
		t.Fatalf("CI not attached: %+v", revs[0])
	}
	if revs[0].MyReviewState != "approved" {
		t.Fatalf("review marker not set: %+v", revs[0])
	}
}
```

(Fill the fake-provider wiring to match the helpers actually present in `internal/sync/*_test.go`; the discovery step identifies them. Do not invent helper names — use the ones the package already has.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sync/ -run TestSync_RecordsRevisionWithCIAndReview -v`
Expected: FAIL (no revision recorded yet).

- [ ] **Step 3a: Capture `base_sha`** — in `pkg/provider/vcs/github/enrich.go`, add `baseRefOid` to the PR GraphQL selection, add the corresponding field to the response node struct, and surface it on the enrich payload / `api` PR type as `BaseSHA`. Map it through to the `store.PullRequest` row builder (`prToStoreRow`, `internal/sync/ingest.go:59` area) so the value reaches sync (the `pull_request` table need not persist it — the revision row does).

- [ ] **Step 3b: Wire the three store calls** — in `internal/sync/sync.go`, in the per-PR refresh after the existing `UpsertPR` returns `prID`, add:

```go
// Record the revision timeline (append-or-touch) and attach CI for this head.
rev, _, err := deps.Store.RecordRevision(ctx, prID, pr.HeadSHA, pr.BaseSHA)
if err != nil {
	return fmt.Errorf("sync: record revision %s#%d: %w", pr.Repo, pr.Number, err)
}
if err := deps.Store.SetRevisionCI(ctx, rev.ID, ciRollupFromSync(ci)); err != nil {
	return fmt.Errorf("sync: set revision ci %s#%d: %w", pr.Repo, pr.Number, err)
}
// Mark my submitted reviews against the revision they targeted.
for _, rv := range mySubmittedReviews(reviews, myLogin) {
	if err := deps.Store.MarkRevisionReviewed(ctx, prID, rv.CommitSHA, rv.State, rv.SubmittedAt); err != nil {
		return fmt.Errorf("sync: mark reviewed %s#%d: %w", pr.Repo, pr.Number, err)
	}
}
```

Add two small local helpers in `internal/sync`:

- `ciRollupFromSync(ci …) store.CIRollup` — maps the CI source's per-check conclusions to `{State, Passed, Failed, Pending, CapturedAt}` (state: any failure → `"failure"`; else any pending/queued → `"pending"`; else all success & ≥1 check → `"success"`; no checks → `"none"`; provider/internal error → `"error"`). Reuse the conclusion classification already used to build `ci-failure` feedback rather than re-deriving it.
- `mySubmittedReviews(reviews []api.Review, myLogin string) […]` — filters to reviews authored by `myLogin` with a submitted state, mapping GitHub review state (`APPROVED`/`CHANGES_REQUESTED`/`COMMENTED`) to the store enum (`approved`/`changes-requested`/`commented`) and carrying the targeted commit SHA + submittedAt.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/sync/ ./internal/store/ -v`
Expected: PASS. Then `go test ./...` for the whole module.

- [ ] **Step 5: Commit**

```bash
git add pkg/provider/vcs/github/enrich.go internal/sync/
git commit -m "feat(pg-pr): record revisions + CI + review marker during sync"
```

---

## Final validation (not a task — the completion gate)

- [ ] `go test ./...` green in `packages/pg-pr`.
- [ ] `nix flake check` green in `phillipgreenii-nix-agent-support`.
- [ ] `pn workspace build` green from the workspace root (consumer-side check).
- [ ] Update `pg2-4c5i.11` to closed; note that #1 (`pg2-4c5i.12`) and #3 (`pg2-4c5i.13`) can now consume `ListRevisions`/`LatestRevision`.

## Self-review notes (author)

- **Spec coverage:** schema (Task 1), append rule incl. base-change + force-push + NULL-base fallback (Task 2), compact CI rollup (Task 3), submitted-review marker incl. recurring-SHA semantics (Task 4), sync capture/record/CI/review + no-backfill-next-sync-seeds (Task 5). Out-of-scope (#1/#3 logic, dashboard) excluded.
- **Open implementer decision (flagged, not a placeholder):** the exact `api.Review` commit-SHA field and the CI source's conclusion enum are confirmed in Task 5's discovery step before coding, because they depend on current provider code; the mapping rules are specified, only the field names are to be read from source.
