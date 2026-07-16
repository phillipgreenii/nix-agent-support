# pg-pr Three-Way Ownership (co-owned) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a third PR ownership state `co-owned` (a teammate-authored PR I have pushed commits onto) that behaves like `mine` for review-routing/replies/attention/dashboard but is never auto-promoted out of draft.

**Architecture:** One pure classifier (`internal/ownership`) over an extensible `Engagement` fact struct is the single source of truth, fed by GraphQL commit-author data. The `ownership` string (3-way) drives store row + events + downstream consumers; the draft-promote write-gate stays authorship-only, so classification and that one upstream write deliberately diverge.

**Tech Stack:** Go 1.25, SQLite (`internal/store`), GitHub GraphQL via `gh` (`pkg/provider/vcs/github`), bd/beads CLI (`pkg/beads`).

## Global Constraints

- Package under test: `packages/pg-pr` (run all `go` commands from that dir).
- `co-owned` acts like `mine` EXCEPT `maybePromoteDraft → SetDraft(false)`, which stays authorship-only.
- Ownership precedence: authored-by-self ⇒ `mine` (always wins); else self-authored commit ⇒ `co-owned`; else `team`. Empty `SelfLogin` ⇒ `team`.
- "My commit" = commit whose GitHub `author.user.login == cfg().SelfLogin`. NOT committer; NOT `Co-authored-by:`.
- Stateless: ownership is re-derived every tick from observed commits (reverts naturally). No sticky flag.
- Degradation: when enriched commit data is absent for a tick, classify authorship-only (`mine|team`).
- Spec: `docs/superpowers/specs/2026-07-16-pg-pr-ownership-and-conflict-urgency-design.md`.
- Commit style: `<type>(pg-pr): <subject> (pg2-aag72)`. Pre-commit (`treefmt`) may reformat; re-stage and re-commit if so.

---

### Task 1: `internal/ownership` classifier package

**Files:**

- Create: `internal/ownership/ownership.go`
- Test: `internal/ownership/ownership_test.go`

**Interfaces:**

- Produces: `ownership.Ownership` (string type, consts `Mine`/`CoOwned`/`Team`); `ownership.Engagement{Self, PRAuthor string; CommitAuthors []string}`; `func Classify(Engagement) Ownership`; `func (Ownership) ActsAsMine() bool`; `func (Ownership) String() string`.

- [ ] **Step 1: Write the failing test**

```go
package ownership

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		e    Engagement
		want Ownership
	}{
		{"authored-by-me => mine", Engagement{Self: "me", PRAuthor: "me"}, Mine},
		{"mine wins even with others' commits", Engagement{Self: "me", PRAuthor: "me", CommitAuthors: []string{"you"}}, Mine},
		{"teammate + my commit => co-owned", Engagement{Self: "me", PRAuthor: "you", CommitAuthors: []string{"you", "me"}}, CoOwned},
		{"teammate + no commit of mine => team", Engagement{Self: "me", PRAuthor: "you", CommitAuthors: []string{"you"}}, Team},
		{"empty self => team", Engagement{Self: "", PRAuthor: "me", CommitAuthors: []string{"me"}}, Team},
		{"nil commits (degrade) teammate => team", Engagement{Self: "me", PRAuthor: "you"}, Team},
		{"nil commits (degrade) mine => mine", Engagement{Self: "me", PRAuthor: "me"}, Mine},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.e); got != tt.want {
				t.Errorf("Classify(%+v) = %q, want %q", tt.e, got, tt.want)
			}
		})
	}
}

func TestActsAsMine(t *testing.T) {
	if !Mine.ActsAsMine() || !CoOwned.ActsAsMine() || Team.ActsAsMine() {
		t.Errorf("ActsAsMine: Mine=%v CoOwned=%v Team=%v; want true true false",
			Mine.ActsAsMine(), CoOwned.ActsAsMine(), Team.ActsAsMine())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ownership/`
Expected: FAIL — package/`Classify` undefined.

- [ ] **Step 3: Write the implementation**

```go
// Package ownership classifies a tracked PR on the single "can I act on this
// PR?" axis. Ownership is a CLOSED 3-value set — it is deliberately NOT where
// engagement/tier signals (why a PR is in my set, how urgent) live; pg2-4dz88
// layers those as a separate classifier over the same Engagement facts.
package ownership

// Ownership is the "can I act?" axis for a tracked PR.
type Ownership string

const (
	Mine    Ownership = "mine"
	CoOwned Ownership = "co-owned"
	Team    Ownership = "team"
)

// Engagement is the set of PR facts Classify reads. It is the growth point:
// pg2-4dz88 adds signals (MyReviewSubmitted, AssignedToMe, ICommented, …) here
// without changing call sites. CommitAuthors is the per-commit author logins
// observed this tick; nil/empty (enrichment absent) degrades to authorship-only.
type Engagement struct {
	Self          string
	PRAuthor      string
	CommitAuthors []string
}

// Classify applies precedence: authored-by-self => Mine (always wins); else a
// self-authored commit => CoOwned; else Team. Empty Self => Team.
func Classify(e Engagement) Ownership {
	if e.Self == "" {
		return Team
	}
	if e.PRAuthor == e.Self {
		return Mine
	}
	for _, a := range e.CommitAuthors {
		if a != "" && a == e.Self {
			return CoOwned
		}
	}
	return Team
}

// ActsAsMine reports whether store consumers should treat this PR like my own
// (dashboard Mine panel, reply-posting, mine-style review, no team attention).
// True for Mine and CoOwned; false for Team.
func (o Ownership) ActsAsMine() bool { return o == Mine || o == CoOwned }

// String returns the store/payload string value.
func (o Ownership) String() string { return string(o) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ownership/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ownership/
git commit -m "feat(pg-pr): add ownership classifier package (mine/co-owned/team) (pg2-aag72)"
```

---

### Task 2: Capture commit-author logins in GraphQL enrich

**Files:**

- Modify: `pkg/provider/vcs/iface.go` (add `CommitAuthors []string` to `EnrichedPR`)
- Modify: `pkg/provider/vcs/github/enrich.go` (GraphQL selection, `ghPRNode.Commits` node struct, `enrichedPRFromNode`)
- Test: `pkg/provider/vcs/github/enrich_test.go`

**Interfaces:**

- Consumes: nothing new.
- Produces: `vcs.EnrichedPR.CommitAuthors []string` — deduped-optional per-commit `author.user.login`; `""`/nil `user` entries dropped.

- [ ] **Step 1: Write the failing test** (append to `enrich_test.go`)

```go
// TestParseEnrichedPRs_CommitAuthors verifies commit author logins map onto
// vcs.EnrichedPR.CommitAuthors, dropping commits whose author has no linked user.
func TestParseEnrichedPRs_CommitAuthors(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":44,"title":"x","author":{"__typename":"User","login":"alice"},
	   "headRefName":"f","baseRefName":"main","url":"https://gh/44","isDraft":false,
	   "state":"OPEN","merged":false,"additions":1,"deletions":0,"changedFiles":1,
	   "repository":{"nameWithOwner":"x/y"},
	   "reviews":{"nodes":[]},"comments":{"nodes":[]},"reviewThreads":{"nodes":[]},
	   "body":"","labels":{"nodes":[]},"files":{"nodes":[]},
	   "commits":{"nodes":[
	     {"commit":{"oid":"a","message":"m1","author":{"user":{"login":"alice"}},"statusCheckRollup":null}},
	     {"commit":{"oid":"b","message":"m2","author":{"user":{"login":"bob"}},"statusCheckRollup":null}},
	     {"commit":{"oid":"c","message":"m3","author":{"user":null},"statusCheckRollup":null}}
	   ]}}
	]}}}`

	got, err := parseEnrichedPRs([]byte(resp), "x/y")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	want := []string{"alice", "bob"}
	if !sliceEq(got[0].CommitAuthors, want) {
		t.Errorf("CommitAuthors = %v, want %v", got[0].CommitAuthors, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/provider/vcs/github/ -run TestParseEnrichedPRs_CommitAuthors`
Expected: FAIL — `CommitAuthors` field undefined.

- [ ] **Step 3a: Add the field to `EnrichedPR`** (`pkg/provider/vcs/iface.go`, after the `Commits []string` field)

```go
	// CommitAuthors are the PR's per-commit GitHub author logins
	// (author.user.login). Commits with no linked user contribute no entry.
	// Empty on the REST fallback path. Used to classify co-owned ownership.
	CommitAuthors []string
```

- [ ] **Step 3b: Extend the GraphQL commit selection** (`enrich.go`, inside `prNodeSelection`, the `commits(last: 20)` block — add `author` after `message`)

```graphql
        commits(last: 20) {
          totalCount
          pageInfo { hasNextPage }
          nodes {
            commit {
              oid
              message
              author { user { login } }
              statusCheckRollup {
```

- [ ] **Step 3c: Extend the `ghPRNode.Commits` node struct** (`enrich.go`, the `Commits` field's `Nodes[].Commit` struct — add `Author`)

```go
			Commit struct {
				OID     string `json:"oid"`
				Message string `json:"message"`
				Author  *struct {
					User *struct {
						Login string `json:"login"`
					} `json:"user"`
				} `json:"author"`
				StatusCheckRollup *struct {
					State    string         `json:"state"`
					Contexts ghContextsConn `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
```

- [ ] **Step 3d: Populate `CommitAuthors`** (`enrich.go`, `enrichedPRFromNode`, alongside the existing `ep.Commits` loop)

```go
	for _, c := range n.Commits.Nodes {
		ep.Commits = append(ep.Commits, c.Commit.Message)
		if c.Commit.Author != nil && c.Commit.Author.User != nil && c.Commit.Author.User.Login != "" {
			ep.CommitAuthors = append(ep.CommitAuthors, c.Commit.Author.User.Login)
		}
	}
```

(Replace the existing single-line `ep.Commits` append loop with this combined loop.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/provider/vcs/github/ -run TestParseEnrichedPRs_CommitAuthors`
Expected: PASS. Also run the full github package: `go test ./pkg/provider/vcs/github/`.

- [ ] **Step 5: Commit**

```bash
git add pkg/provider/vcs/iface.go pkg/provider/vcs/github/enrich.go pkg/provider/vcs/github/enrich_test.go
git commit -m "feat(pg-pr): capture per-commit author logins in GraphQL enrich (pg2-aag72)"
```

---

### Task 3: Store migration v7→v8 — `ownership` CHECK adds `co-owned`

**Files:**

- Modify: `internal/store/migrate.go` (bump `schemaVersion`; append migration)
- Modify: `internal/store/pull_request.go:17` (doc comment only)
- Test: `internal/store/migrate_test.go`

**Interfaces:**

- Produces: `pull_request.ownership` accepts `'mine' | 'co-owned' | 'team'`.

- [ ] **Step 1: Write the failing test** (append to `migrate_test.go`)

```go
func TestMigrate_V8CoOwnedOwnership(t *testing.T) {
	db := OpenForTest(t)

	// co-owned is now accepted.
	if _, err := db.sql.Exec(`INSERT INTO pull_request
		(repo,number,ownership,state,head_sha,created_at,updated_at)
		VALUES ('o/r',1,'co-owned','open','s','t','t')`); err != nil {
		t.Fatalf("insert co-owned should succeed: %v", err)
	}
	// bogus ownership still rejected.
	if _, err := db.sql.Exec(`INSERT INTO pull_request
		(repo,number,ownership,state,head_sha,created_at,updated_at)
		VALUES ('o/r',2,'bogus','open','s','t','t')`); err == nil {
		t.Fatal("expected ownership CHECK to reject 'bogus'")
	}
	// Pre-existing rows preserved (id retained), and idempotent re-migrate.
	var cnt int
	_ = db.sql.QueryRow("SELECT count(*) FROM pull_request WHERE ownership='co-owned'").Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("want 1 co-owned row, got %d", cnt)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestMigrate_V8CoOwnedOwnership`
Expected: FAIL — CHECK rejects `co-owned` (constraint) at the first insert.

- [ ] **Step 3a: Bump `schemaVersion`** (`migrate.go`)

```go
const schemaVersion = 8
```

- [ ] **Step 3b: Append the v7→v8 migration** (`migrate.go`, as the last entry of the `migrations` slice)

```go
	// v7 -> v8: widen the pull_request.ownership CHECK to add 'co-owned'
	// (pg2-aag72). A column CHECK cannot be altered in place, so the table is
	// rebuilt (SQLite 12-step ALTER, as v6 did for feedback.kind). pull_request
	// has ON DELETE CASCADE children (feedback, pr_revision) keyed on its id;
	// applyMigration disables foreign_keys around the tx and runs
	// foreign_key_check after, so the rebuild preserves ids and child links.
	// Column set = v1 base + v2 enrichment columns; no indexes to recreate
	// (only the inline UNIQUE(repo,number)).
	`
CREATE TABLE pull_request_new (
    id             INTEGER PRIMARY KEY,
    repo           TEXT NOT NULL,
    number         INTEGER NOT NULL,
    ownership      TEXT NOT NULL CHECK (ownership IN ('mine','co-owned','team')),
    author         TEXT,
    state          TEXT NOT NULL,
    branch         TEXT,
    base           TEXT,
    url            TEXT,
    head_sha       TEXT,
    last_synced_at TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    kind            TEXT    NOT NULL DEFAULT '',
    languages       TEXT    NOT NULL DEFAULT '[]',
    size            TEXT    NOT NULL DEFAULT '',
    urgency         TEXT    NOT NULL DEFAULT '',
    urgency_score   INTEGER NOT NULL DEFAULT 0,
    urgency_reasons TEXT    NOT NULL DEFAULT '[]',
    UNIQUE (repo, number)
);
INSERT INTO pull_request_new
  SELECT id, repo, number, ownership, author, state, branch, base, url, head_sha,
         last_synced_at, created_at, updated_at, kind, languages, size, urgency,
         urgency_score, urgency_reasons
  FROM pull_request;
DROP TABLE pull_request;
ALTER TABLE pull_request_new RENAME TO pull_request;
`,
```

- [ ] **Step 3c: Update the doc comment** (`pull_request.go:17`)

```go
	Ownership      string // "mine" | "co-owned" | "team"
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/`
Expected: PASS (new test + all existing migration/store tests, incl. the FK-integrity path).

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrate.go internal/store/migrate_test.go internal/store/pull_request.go
git commit -m "feat(pg-pr): migrate ownership CHECK to allow co-owned (v8) (pg2-aag72)"
```

---

### Task 4: Sync main daemon loop — classify 3-way ownership; split the promote gate

**Files:**

- Modify: `internal/sync/sync.go` (the `Sync` per-PR loop, ~377-509)
- Test: `internal/sync/` (existing suite must stay green; behavior asserted via Task 6/7/11 consumer tests + an ownership-string test here)

**Interfaces:**

- Consumes: `ownership.Classify`, `vcs.EnrichedPR.CommitAuthors`, `e.cfg().SelfLogin`.
- Produces: the store row + `pr.opened/updated` event carry a 3-way `ownership` string; `maybePromoteDraft` is gated by an authorship-only set.

- [ ] **Step 1: Rename the pre-loop `mineSet` to `authoredByMe` (authorship-only, for the promote gate + telemetry)** (`sync.go:387-392`)

```go
	// authoredByMe gates UPSTREAM WRITES that assert readiness on the author's
	// behalf (maybePromoteDraft). It stays AUTHORSHIP-ONLY: a co-owned PR (I
	// pushed commits onto a teammate's PR) must NOT be auto-promoted out of
	// draft. This deliberately diverges from the `ownership` string below, which
	// is 3-way. Empty Author / empty SelfLogin => not mine.
	authoredByMe := make(map[prKey]bool, len(observed))
	for key, pr := range observed {
		if e.isSelfAuthored(pr.Author) {
			authoredByMe[key] = true
		}
	}
```

- [ ] **Step 2: Update the telemetry group + move the enriched pluck above the ownership decision** (`sync.go`, inside the per-PR loop)

Replace the `group := "team" … mineSet[key]` telemetry block to use `authoredByMe`, and move the `prEnriched` pluck (currently ~467-472) to just before the ownership decision so `CommitAuthors` are available for `emitPREvent`:

```go
			group := "team"
			if authoredByMe[key] {
				group = "mine"
			}
			// ... (defer telemetry unchanged) ...

			bdc := repoClients[key.Repo]
			if bdc == nil {
				return
			}

			// Pluck this PR's bulk-fetched enrichment BEFORE deciding ownership
			// (commit authors drive co-owned) and before emitPREvent writes the row.
			var prEnriched *vcs.EnrichedPR
			if byNum := enrichByRepo[key.Repo]; byNum != nil {
				if ep, ok := byNum[pr.Number]; ok {
					prEnriched = &ep
				}
			}

			// 3-way ownership string for the store row + event (drives dashboard,
			// replyposter, beadsbridge, attention). Degrades to authorship-only
			// when enrichment is absent (prEnriched == nil).
			var commitAuthors []string
			if prEnriched != nil {
				commitAuthors = prEnriched.CommitAuthors
			}
			own := ownership.Classify(ownership.Engagement{
				Self: e.cfg().SelfLogin, PRAuthor: pr.Author, CommitAuthors: commitAuthors,
			})
			ownershipStr := own.String()
```

Then the existing `ownership := "team"; if mineSet[key] { ownership = "mine" }` block is DELETED, and `emitPREvent(prCtx, eventType, key.Repo, pr, ownership)` becomes `emitPREvent(prCtx, eventType, key.Repo, pr, ownershipStr)`. Delete the old (now-duplicate) `prEnriched` pluck lower in the loop. Keep `reconcileTruncatedCI`/`enrichAndStore`/`processFeedback` calls as-is (they already receive `prEnriched`).

- [ ] **Step 3: Gate `maybePromoteDraft` on `authoredByMe`** (`sync.go:508`)

```go
			if authoredByMe[key] {
				if err := e.maybePromoteDraft(prCtx, prEnriched, key.Repo, pr, summary); err != nil {
```

- [ ] **Step 4: Add the `ownership` import** (`sync.go` import block)

```go
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
```

- [ ] **Step 5: Build + run the sync suite**

Run: `go build ./... && go test ./internal/sync/ -run TestSync`
Expected: PASS (existing behavior preserved: a mine PR still classifies `mine`; a team PR with no self commit still `team`).

- [ ] **Step 6: Commit**

```bash
git add internal/sync/sync.go
git commit -m "feat(pg-pr): classify 3-way ownership in sync loop; keep promote gate authorship-only (pg2-aag72)"
```

---

### Task 5: `applyFetchedPR` + close-detection paths — classify ownership

**Files:**

- Modify: `internal/sync/sync.go` (`applyFetchedPR` ~1255-1300; close-detection ~1186)

**Interfaces:**

- Consumes: `ownership.Classify` (with `enriched.CommitAuthors` where available).
- Produces: single-PR + close paths write the 3-way ownership string; the single-PR promote gate stays authorship-only.

- [ ] **Step 1: `applyFetchedPR` ownership** (`sync.go:1259-1261`) — replace the binary block with:

```go
	var commitAuthors []string
	if enriched != nil {
		commitAuthors = enriched.CommitAuthors
	}
	ownershipStr := ownership.Classify(ownership.Engagement{
		Self: e.cfg().SelfLogin, PRAuthor: pr.Author, CommitAuthors: commitAuthors,
	}).String()
```

and pass `ownershipStr` to `emitPREvent` (~1281).

- [ ] **Step 2: Keep the single-PR promote gate authorship-only** (`sync.go:1295`) — unchanged (`if e.isSelfAuthored(pr.Author)`), but add a clarifying comment:

```go
	// Authorship-only (NOT ownership): never auto-promote a co-owned teammate draft.
	if e.isSelfAuthored(pr.Author) {
		if err := e.maybePromoteDraft(ctx, enriched, rcfg.Remote, *pr, summary); err != nil {
```

- [ ] **Step 3: Close-detection ownership** (`sync.go:1186-1188`) — replace with `Classify` (enriched typically nil here → authorship degrade, acceptable):

```go
		ownershipStr := ownership.Classify(ownership.Engagement{
			Self: e.cfg().SelfLogin, PRAuthor: pr.Author,
		}).String()
		row := e.prToStoreRow(repo, *pr, ownershipStr)
```

- [ ] **Step 4: Build + test**

Run: `go build ./... && go test ./internal/sync/ -run TestSyncPR`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/sync/sync.go
git commit -m "feat(pg-pr): classify ownership in single-PR + close paths (pg2-aag72)"
```

---

### Task 6: `ingest.go` — ownership + `mine` bool via `Classify`

**Files:**

- Modify: `internal/sync/ingest.go:44-62,91-99`
- Test: `internal/sync/` ingest/ownership tests

**Interfaces:**

- Consumes: `ownership.Classify`, `enriched.CommitAuthors`.
- Produces: `ingestFeedbackToStore` writes the 3-way ownership + a `Mine` feedback bool that is true for `mine`+`co-owned`.

- [ ] **Step 1: Write the failing test** (append to the ingest test file, e.g. `internal/sync/ingest_test.go`; follow the existing ingest-test harness for `Engine`/`Store` wiring)

```go
func TestIngest_CoOwnedOwnership(t *testing.T) {
	// Harness: build an Engine with SelfLogin="me" and an in-memory store,
	// following the existing ingest test setup (see other TestIngest_* tests).
	e, st := newIngestTestEngine(t, "me") // existing/util helper; if absent, mirror a sibling ingest test's setup
	pr := api.PR{Repo: "o/r", Number: 7, Author: "you", HeadSHA: "h", BaseSHA: "b"}
	enriched := &vcs.EnrichedPR{PR: pr, CommitAuthors: []string{"you", "me"}}

	if err := e.ingestFeedbackToStore(context.Background(), "o/r", pr, enriched); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	got, _ := st.GetPR(context.Background(), "o/r", 7)
	if got == nil || got.Ownership != "co-owned" {
		t.Fatalf("ownership = %v, want co-owned", got)
	}
}
```

(If no ingest-test engine helper exists, replicate the construction used by the nearest existing `ingestFeedbackToStore` test in the package.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/sync/ -run TestIngest_CoOwnedOwnership`
Expected: FAIL — ownership is `team` (authorship-only today).

- [ ] **Step 3: Replace `mine`/`ownership` derivation** (`ingest.go:44-62`)

```go
	self := e.cfg().SelfLogin
	own := ownership.Classify(ownership.Engagement{
		Self: self, PRAuthor: pr.Author, CommitAuthors: enriched.CommitAuthors,
	})
	mine := own.ActsAsMine()
```

and (lines 59-62) replace the `ownership := "team"; if mine { ownership = "mine" }` block:

```go
	prID, err := e.deps.Store.UpsertPR(ctx, e.prToStoreRow(repo, pr, own.String()))
```

The `mine` bool continues to feed the `payloadBytes` struct (`Mine: mine`) unchanged — now true for co-owned too.

- [ ] **Step 4: Add import + run**

Add the `ownership` import to `ingest.go`. Run: `go test ./internal/sync/ -run TestIngest`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/sync/ingest.go internal/sync/ingest_test.go
git commit -m "feat(pg-pr): ingest classifies ownership + mine bool via Classify (pg2-aag72)"
```

---

### Task 7: `refresh.go` — enrich-early, surface co-owned drafts, clear team attention on transition

**Files:**

- Modify: `internal/sync/refresh.go` (`refreshPR` 38-124; also close-path ownership 63-66)
- Modify: `internal/sync/prevents.go` (`emitAttention` — force `Need=false` for non-team) OR pass ownership through
- Test: `internal/sync/` refresh + attention tests

**Interfaces:**

- Consumes: `ownership.Classify`, `enrichOnePR`.
- Produces: a `co-owned` draft PR is surfaced (not hidden); attention is emitted for `team` AND `co-owned`, with `Need=false` forced for `co-owned` (closes any open attention bead on transition).

- [ ] **Step 1: Move enrichment before the draft-hide check + classify once** (`refreshPR`, after the closed/merged early-return at line 78, before the hidden-draft check)

```go
	// Enrich BEFORE the draft-hide decision so a co-owned draft (teammate PR I
	// pushed commits onto) is detected and surfaced rather than hidden as a team
	// draft. (Closed/merged PRs returned above never reach here.)
	enriched := e.enrichOnePR(ctx, rcfg, *pr)
	own := ownership.Classify(ownership.Engagement{
		Self: e.cfg().SelfLogin, PRAuthor: pr.Author, CommitAuthors: commitAuthorsOf(enriched),
	})

	// Hidden team draft: only a genuine TEAM draft is hidden. A co-owned/mine
	// draft falls through to the active pipeline and is surfaced.
	if own == ownership.Team && pr.Draft {
		if err := e.emitPREvent(ctx, store.EventPRUpdated, repo, *pr, own.String()); err != nil {
			return nil, err
		}
		flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
		return nil, nil
	}
```

Delete the later `enriched := e.enrichOnePR(...)` at ~101 (now hoisted). Replace the old `if !e.isSelfAuthored(pr.Author) && pr.Draft { ... "team" ... }` block with the above.

- [ ] **Step 2: Add the `commitAuthorsOf` helper** (`refresh.go`, file scope)

```go
// commitAuthorsOf returns enriched.CommitAuthors, tolerating a nil enriched
// (single-PR enrich failure) — nil degrades ownership to authorship-only.
func commitAuthorsOf(enriched *vcs.EnrichedPR) []string {
	if enriched == nil {
		return nil
	}
	return enriched.CommitAuthors
}
```

- [ ] **Step 3: Close-path ownership** (`refresh.go:63-66`) — use `Classify` (authorship degrade; the close path has no enriched yet):

```go
		ownershipStr := ownership.Classify(ownership.Engagement{
			Self: e.cfg().SelfLogin, PRAuthor: pr.Author,
		}).String()
		row := e.prToStoreRow(repo, *pr, ownershipStr)
```

- [ ] **Step 4: Attention gate — emit for team AND co-owned; force Need=false for co-owned** (`refresh.go:111`)

Change the guard and thread ownership into `emitAttention`:

```go
	// Emit attention for any PR that is NOT mine-authored: team PRs carry the
	// real predicate; co-owned PRs emit Need=false so a team->co-owned transition
	// idempotently CLOSES the previously-opened attention bead. Mine-authored PRs
	// carry no attention signal.
	if e.deps.Store != nil && own != ownership.Mine {
		if stored, gerr := e.deps.Store.GetPR(ctx, rcfg.Remote, pr.Number); gerr == nil && stored != nil {
			if aerr := e.emitAttention(ctx, bdc, rcfg.Remote, pr.Number, stored.ID, own); aerr != nil {
				summary.Errors = append(summary.Errors, SummaryError{Repo: rcfg.Remote, Message: aerr.Error()})
			} else {
				flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
			}
		}
	}
```

- [ ] **Step 5: `emitAttention` forces Need=false for non-team** (`prevents.go:88-116`)

Add an `own ownership.Ownership` parameter; when `own != ownership.Team`, short-circuit `need=false`:

```go
func (e *Engine) emitAttention(ctx context.Context, bdc BeadClient, repo string, number int, prID int64, own ownership.Ownership) error {
	if e.deps.Store == nil {
		return nil
	}
	// A co-owned PR is never a review target for me — clear any attention bead.
	if own != ownership.Team {
		payload, err := json.Marshal(store.AttentionPayload{Repo: repo, Number: number, Need: false, Reason: ""})
		if err != nil {
			return err
		}
		return e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
			return tx.EnqueueEvent(store.EventPRAttention, payload)
		})
	}
	// ... existing team path (ListRevisions, draftReviewClosed, NeedsAttention) ...
}
```

Update the `applyFetchedPR`/other `emitAttention` call sites (if any) to pass ownership. (Check `grep -rn emitAttention internal/sync`; refresh.go is the only production caller.)

- [ ] **Step 6: Write/adjust the transition test** (`internal/sync/` — follow the existing refresh/attention test harness)

```go
// A team PR that gains a self-authored commit surfaces (not hidden) and emits
// pr.attention Need=false.
func TestRefreshPR_TeamToCoOwned_SurfacesAndClearsAttention(t *testing.T) {
	// Harness per existing refreshPR tests: provider returns a draft PR authored
	// by "you" whose enriched CommitAuthors include "me"; SelfLogin="me".
	// Assert: refreshPR returns a non-nil PRInput (surfaced), and an
	// EventPRAttention with Need=false was enqueued.
}
```

(Implement using the nearest existing refreshPR test's fakes/wiring.)

- [ ] **Step 7: Build + run**

Run: `go build ./... && go test ./internal/sync/ -run 'TestRefresh|TestAttention'`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/sync/refresh.go internal/sync/prevents.go internal/sync/*_test.go
git commit -m "feat(pg-pr): surface co-owned drafts; clear team attention on transition (pg2-aag72)"
```

---

### Task 8: `replyposter` — post replies for mine + co-owned

**Files:**

- Modify: `internal/replyposter/poster.go:71`
- Test: `internal/replyposter/poster_test.go`

**Interfaces:**

- Consumes: `store.PullRequest.Ownership`.
- Produces: replies posted for `mine` and `co-owned`; skipped only for `team`.

- [ ] **Step 1: Write the failing test** (append to `poster_test.go`, mirroring the existing team-skip test)

```go
func TestReplyPoster_PostsOnCoOwned(t *testing.T) {
	// Setup a co-owned PR row (Ownership="co-owned") with a pending reply,
	// mirroring the existing "skip team-owned" test's harness. Assert the reply
	// IS posted (replier.AddComment / ReplyToThread called; MarkReplied invoked).
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/replyposter/ -run TestReplyPoster_PostsOnCoOwned`
Expected: FAIL — current gate `pr.Ownership != "mine"` skips co-owned.

- [ ] **Step 3: Change the gate** (`poster.go:71`)

```go
		// Auto-reply on PRs I act on (mine + co-owned). Team-owned PRs are
		// monitored but replies are not auto-posted (M2: ownership gate).
		if pr.Ownership == "team" {
			p.log.DebugContext(ctx, "replyposter: skip team-owned pr", "pr_id", fb.PRID, "feedback_id", fb.ID, "ownership", pr.Ownership)
			continue
		}
```

- [ ] **Step 4: Run**

Run: `go test ./internal/replyposter/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replyposter/poster.go internal/replyposter/poster_test.go
git commit -m "feat(pg-pr): replyposter posts on co-owned PRs (pg2-aag72)"
```

---

### Task 9: beadsbridge — mine-style draft-review for co-owned + relabel on transition

**Files:**

- Modify: `internal/beadsbridge/bridge.go:93-94`
- Modify: `pkg/beads/draftreview.go` (new `EnsureDraftReviewMineLabel`)
- Modify: `internal/beadsbridge/bridge.go` `BeadClient` interface (add the method)
- Test: `pkg/beads/draftreview_test.go`, `internal/beadsbridge/bridge_test.go`

**Interfaces:**

- Consumes: `p.Ownership` string.
- Produces: `EnsureDraftReviewBead(mine = ownership != "team")`; a `co-owned` PR whose open draft-review lacks the `mine` label gets it added.

- [ ] **Step 1: Add the relabel capability** (`pkg/beads/draftreview.go`)

```go
// EnsureDraftReviewMineLabel adds the "mine" ownership label to the OPEN
// draft-review child of prBeadID when it lacks it — used on a team->co-owned
// transition so routing treats the review as mine-style. Closed (completed)
// review beads are left untouched. Idempotent; a lookup error PROPAGATES.
func (c *Client) EnsureDraftReviewMineLabel(ctx context.Context, prBeadID string) error {
	if prBeadID == "" {
		return errors.New("draft-review: pr bead id required")
	}
	childIDs, err := c.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return fmt.Errorf("relabel draft-review: list children of %s: %w", prBeadID, err)
	}
	if len(childIDs) == 0 {
		return nil
	}
	isChild := make(map[string]struct{}, len(childIDs))
	for _, id := range childIDs {
		isChild[id] = struct{}{}
	}
	out, err := c.Runner.Run(ctx, "list", "--type=task", "--json", "--limit=0") // open only (no --all)
	if err != nil {
		return fmt.Errorf("relabel draft-review: list tasks: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return err
	}
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, draftReviewTitlePrefix) {
			continue
		}
		if _, ok := isChild[iss.ID]; !ok {
			continue
		}
		if hasLabel(iss.Labels, "mine") {
			return nil // already mine-style
		}
		_, err := c.Runner.Run(ctx, "update", iss.ID, "--add-label", "mine")
		return err
	}
	return nil
}
```

- [ ] **Step 2: Write the failing capability test** (`pkg/beads/draftreview_test.go`, mirroring existing draft-review tests' fake `Runner`)

```go
func TestEnsureDraftReviewMineLabel_AddsWhenMissing(t *testing.T) {
	// Fake Runner: ListChildrenOfPR returns [dr-1]; list --type=task returns an
	// open "draft-review: o/r#1" task dr-1 with no labels. Assert the client runs
	// `update dr-1 --add-label mine`.
}
func TestEnsureDraftReviewMineLabel_NoopWhenPresent(t *testing.T) {
	// Same, but dr-1 already has label "mine": assert NO update call is made.
}
```

- [ ] **Step 3: Run to verify fail, implement (Step 1 already has impl), run to pass**

Run: `go test ./pkg/beads/ -run TestEnsureDraftReviewMineLabel`
Expected: PASS after Step 1.

- [ ] **Step 4: Add to `BeadClient` interface + call from bridge** (`bridge.go`)

Add to the `BeadClient` interface:

```go
	EnsureDraftReviewMineLabel(ctx context.Context, prBeadID string) error
```

Change the pr.opened/updated draft-review projection (bridge.go:93-96):

```go
			mine := p.Ownership != "team" // mine OR co-owned
			if !h.suppressDraftReviews && (mine || !p.Draft) {
				drID, err := h.client.EnsureDraftReviewBead(ctx, mrID, fmt.Sprintf("%s#%d", p.Repo, p.Number), mine)
				if err != nil {
					return err
				}
				// team->co-owned: an earlier team-style review bead must flip to mine.
				if p.Ownership == "co-owned" {
					if err := h.client.EnsureDraftReviewMineLabel(ctx, mrID); err != nil {
						return err
					}
				}
				_ = drID
				return nil
			}
			return nil
```

- [ ] **Step 5: bridge test** (`bridge_test.go`, extend the fake `BeadClient`)

```go
func TestHandle_CoOwnedCreatesMineDraftReviewAndRelabels(t *testing.T) {
	// Fake client records EnsureDraftReviewBead's mine arg and whether
	// EnsureDraftReviewMineLabel was called. Dispatch EventPRUpdated with
	// Ownership="co-owned", Draft=true. Assert mine==true and relabel called.
}
```

Add a no-op `EnsureDraftReviewMineLabel` to any other test fakes implementing `BeadClient`.

- [ ] **Step 6: Build + run**

Run: `go build ./... && go test ./pkg/beads/ ./internal/beadsbridge/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/beads/draftreview.go pkg/beads/draftreview_test.go internal/beadsbridge/bridge.go internal/beadsbridge/bridge_test.go
git commit -m "feat(pg-pr): co-owned gets mine-style draft-review; relabel on transition (pg2-aag72)"
```

---

### Task 10: beadsbridge — visible `co-owned` label on the merge-request bead

**Files:**

- Modify: `pkg/beads/mergerequest.go` (new `SetMergeRequestCoOwned`)
- Modify: `internal/beadsbridge/bridge.go` (`BeadClient` + call on pr.opened/updated)
- Test: `pkg/beads/mergerequest_test.go`, `bridge_test.go`

**Interfaces:**

- Produces: MR bead carries label `co-owned` iff `p.Ownership == "co-owned"` (added when co-owned, removed otherwise) — visibility only.

- [ ] **Step 1: Add the capability** (`pkg/beads/mergerequest.go`)

```go
// SetMergeRequestCoOwned adds (coOwned=true) or removes (false) the "co-owned"
// label on a merge-request bead — a visibility marker for a teammate PR I have
// pushed commits onto. Idempotent (bd add/remove-label are no-ops when already
// in the desired state).
func (c *Client) SetMergeRequestCoOwned(ctx context.Context, id string, coOwned bool) error {
	if id == "" {
		return errors.New("merge-request: id required")
	}
	flag := "--remove-label"
	if coOwned {
		flag = "--add-label"
	}
	_, err := c.Runner.Run(ctx, "update", id, flag, "co-owned")
	return err
}
```

- [ ] **Step 2: Capability test** (`mergerequest_test.go`, fake Runner asserts the `update <id> --add-label co-owned` / `--remove-label co-owned` call)

```go
func TestSetMergeRequestCoOwned(t *testing.T) {
	// coOwned=true -> update id --add-label co-owned
	// coOwned=false -> update id --remove-label co-owned
}
```

- [ ] **Step 3: Wire into bridge** (`bridge.go`, after `EnsureMergeRequest` succeeds in the pr.opened/updated case, before the draft-review block)

```go
			if err := h.client.SetMergeRequestCoOwned(ctx, mrID, p.Ownership == "co-owned"); err != nil {
				return err
			}
```

Add `SetMergeRequestCoOwned` to the `BeadClient` interface and to all test fakes (no-op).

- [ ] **Step 4: Build + run**

Run: `go build ./... && go test ./pkg/beads/ ./internal/beadsbridge/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/beads/mergerequest.go pkg/beads/mergerequest_test.go internal/beadsbridge/bridge.go internal/beadsbridge/bridge_test.go
git commit -m "feat(pg-pr): label merge-request bead co-owned for visibility (pg2-aag72)"
```

---

### Task 11: Dashboard — co-owned in the Mine panel (badged)

**Files:**

- Modify: `internal/snapshot/snapshot.go` (`MineRow` gains `CoOwned bool`)
- Modify: `internal/snapshot/builder.go` (`PRInput` gains `Ownership`; `Build` partitions on it; `buildMineRow` sets `CoOwned`)
- Modify: `internal/sync/sync.go` (`buildPRInput` ~840 sets `PRInput.Ownership` via `Classify`)
- Test: `internal/snapshot/builder_test.go`

**Interfaces:**

- Consumes: `ownership.Ownership` on `snapshot.PRInput`.
- Produces: a `co-owned` PR renders as a `MineRow` with `CoOwned=true`; partition no longer keys on `Author == Self`.

- [ ] **Step 1: Write the failing test** (append to `builder_test.go`, mirroring `TestBuildSplitsMineFromReview`)

```go
func TestBuild_CoOwnedInMinePanelBadged(t *testing.T) {
	in := BuilderInput{
		Self:        "me",
		TeamMembers: []string{"you"},
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 5, Author: "you", Draft: false},
			Ownership: ownership.CoOwned,
		}},
	}
	out := Build(in)
	if len(out.Mine) != 1 || len(out.Team) != 0 {
		t.Fatalf("want 1 mine / 0 team; got %d / %d", len(out.Mine), len(out.Team))
	}
	if !out.Mine[0].CoOwned {
		t.Errorf("MineRow.CoOwned = false, want true")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/snapshot/ -run TestBuild_CoOwnedInMinePanelBadged`
Expected: FAIL — `PRInput.Ownership` / `MineRow.CoOwned` undefined; co-owned PR lands in Team.

- [ ] **Step 3a: Add `MineRow.CoOwned`** (`snapshot.go`, in `MineRow`)

```go
	// CoOwned marks a teammate-authored PR I have pushed commits onto (I can act
	// on it but did not open it). Rendered in the Mine panel with a badge.
	CoOwned bool `json:"co_owned,omitempty"`
```

- [ ] **Step 3b: Add `PRInput.Ownership` + partition on it** (`builder.go`)

Add to `PRInput`:

```go
	// Ownership is the PR's classification (mine/co-owned/team), computed by the
	// sync layer (buildPRInput) via internal/ownership. Build partitions on it.
	Ownership ownership.Ownership
```

Change `Build`'s switch (builder.go:102-116):

```go
		switch {
		case p.Ownership.ActsAsMine():
			out.Mine = append(out.Mine, buildMineRow(p, in.Registry, excl))
		case !p.PR.Draft && len(reasons) > 0:
			out.Team = append(out.Team, buildTeamRow(p, in.Registry, reasons, excl))
		}
```

Set `CoOwned` in `buildMineRow`:

```go
	return MineRow{
		// ... existing fields ...
		CoOwned: p.Ownership == ownership.CoOwned,
	}
```

Add the `ownership` import to `builder.go`.

- [ ] **Step 3c: Populate `PRInput.Ownership` in `buildPRInput`** (`sync.go:840`)

```go
	in := snapshot.PRInput{
		PR: pr,
		Ownership: ownership.Classify(ownership.Engagement{
			Self: e.cfg().SelfLogin, PRAuthor: pr.Author, CommitAuthors: commitAuthorsOf(enriched),
		}),
	}
```

- [ ] **Step 4: Run**

Run: `go test ./internal/snapshot/ ./internal/sync/ -run 'TestBuild|TestSnapshot'`
Expected: PASS. Existing `TestBuildSplitsMineFromReview` / `TestBuild_MinePRStaysMineEvenDraft` still green (a mine PR now sets `Ownership=Mine` via `buildPRInput`; author==self ⇒ Mine).

- [ ] **Step 5: Commit**

```bash
git add internal/snapshot/snapshot.go internal/snapshot/builder.go internal/snapshot/builder_test.go internal/sync/sync.go
git commit -m "feat(pg-pr): render co-owned PRs in the Mine panel, badged (pg2-aag72)"
```

---

### Task 12: Verify `detector.go:181` existing-bead partition

**Files:**

- Inspect: `internal/sync/detector.go` (~170-190) and its caller/consumer
- Modify (only if it misroutes): the partitioning function

**Interfaces:** none new.

- [ ] **Step 1: Trace the consumer** — `grep -rn "existingByOwnership\|<the func name at detector.go:181>" internal/sync`. Determine whether the mine/not-mine partition (keyed on `isSelfAuthored(mr.Fields.Author)`) is used for a decision that would misroute a co-owned PR (whose MR bead `Author` is the teammate).

- [ ] **Step 2: Decide + document**
  - If the partition only affects opened-vs-updated event typing or a mine-only upstream write already covered by `authoredByMe`, it is correct as-is — add a one-line comment noting co-owned intentionally partitions as not-mine here, and STOP.
  - If it drives review-routing/attention/dashboard membership, route it through `ownership.Classify` (using the observed PR + its enriched commit authors) instead of `isSelfAuthored(author)`.

- [ ] **Step 3: Run + commit** (only if changed)

Run: `go test ./internal/sync/`

```bash
git add internal/sync/detector.go
git commit -m "fix(pg-pr): route co-owned correctly in existing-bead partition (pg2-aag72)"
```

---

### Task 13: Package gate

- [ ] **Step 1: Full package test suite**

Run: `go test ./...` (from `packages/pg-pr`)
Expected: PASS (note: `internal/sync` ~155s, `pkg/beads` ~140s — bd integration suites).

- [ ] **Step 2: Acceptance-criteria spot check** against the spec §4.6:
  - AC-A1/A2/A3/A4/A6 → Task 1 + Task 6 tests.
  - AC-A5 → Tasks 8, 9, 7, 11 tests.
  - AC-A7 → Tasks 4/5 (`maybePromoteDraft` gated on `authoredByMe`).
  - AC-A8 → Tasks 7, 9 tests.

- [ ] **Step 3: Repo gates** (run at repo root `phillipgreenii-nix-agent-support/`) — deferred to the shared completion step (also gates Plan B):

```bash
nix flake check
pre-commit run --all-files   # or: prek run --all-files
```

## Self-Review Notes

- Spec coverage: §4.1→T2, §4.2→T3, §4.3→T4/T5/T6, §4.4→T8/T9/T11, §4.5→T7/T9/T10, §4.6→T13. Covered.
- Type consistency: `ownership.Classify(Engagement{…})`, `own.String()`, `own.ActsAsMine()`, `PRInput.Ownership`, `MineRow.CoOwned`, `emitAttention(…, own)`, `EnsureDraftReviewMineLabel`, `SetMergeRequestCoOwned` used consistently across tasks.
- Degradation invariant holds everywhere `commitAuthorsOf`/nil is passed.
