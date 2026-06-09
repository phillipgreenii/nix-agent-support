# Fingerprint-Driven Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `pg-pr` daemon's periodic full-sync with a ~60s GraphQL "fingerprint" poll that detects which PRs changed and refreshes only those, so the dashboard is fresh within ~1 tick.

**Architecture:** Tier 1 is a pure detector — two slim, paginated GraphQL fingerprint queries (mine cross-repo, team per-repo) diffed against the previous roster (for added/changed) and the open merge-request beads (for disappeared). It mutates nothing and enqueues `(repo, number)` keys onto two dedup FIFO queues. Tier 2 is two workers that call a new `refreshPR` (built on the existing single-PR bead path plus per-PR enrichment + dep tree) and feed a single snapshot-owner goroutine that rebuilds and `Set`s the dashboard snapshot per PR. Engine config moves behind an `atomic.Pointer` so SIGHUP reload is race-free.

**Tech Stack:** Go, `gh api graphql` (via the existing `ghRunner.RunStdin`), Prometheus client, `bd` (beads) CLI, `cobra`. Tests are standard `go test` table-driven with injected fakes.

**Spec:** `docs/superpowers/specs/2026-06-09-fingerprint-driven-sync-design.md`. Read it before starting.

**Conventions for every task:**

- TDD: write the failing test, run it red, implement minimally, run it green, commit.
- Run a package's tests with `go test ./internal/sync/... -run <Name> -v` (use `-race` for the concurrency tasks).
- Before committing, the repo's pre-commit runs `treefmt`/`gofmt`; if it reformats a file, `git add` it again and re-commit (first commit attempt may abort with "files were modified by this hook").
- Commit messages: `feat(pg-pr): …` / `refactor(pg-pr): …` / `test(pg-pr): …`.

---

## File Structure

**New files:**

- `pkg/provider/vcs/github/fingerprint.go` — slim paginated fingerprint query, response structs, `parseFingerprints`, `FingerprintPRs`.
- `pkg/provider/vcs/github/fingerprint_test.go` — parser + pagination + fake-runner tests.
- `internal/sync/detector.go` — query builders, `fingerprintHash`, roster diff (pure), the `(*Engine).fingerprintTick` IO wrapper.
- `internal/sync/detector_test.go`.
- `internal/sync/queue.go` — dedup FIFO `refreshQueue`.
- `internal/sync/queue_test.go`.
- `internal/sync/refresh.go` — `refreshPR`, `buildPRInput` (extracted), dormant-mark, shared `applyFetchedPR`.
- `internal/sync/refresh_test.go`.
- `internal/sync/snapshotowner.go` — single-owner incremental snapshot.
- `internal/sync/snapshotowner_test.go`.
- `docs/adr/0012-pg-pr-fingerprint-driven-daemon-sync.md`.

**Modified files:**

- `pkg/provider/vcs/iface.go` — add `PRFingerprint`, `FingerprintResult`, `FingerprintProvider`.
- `internal/sync/sync.go` — extract `buildPRInput` from `buildAndStoreSnapshot`; extract `applyFetchedPR` from `SyncPR`; migrate `e.deps.Cfg` reads to an atomic accessor.
- `internal/sync/daemon.go` — rewrite the loop into detector-tick + workers + owner; `DefaultDaemonInterval` 10m → 60s; atomic `ReplaceCfg`.
- `internal/telemetry/metrics.go` — retire `pg_pr_last_sync_success_timestamp_seconds`; add fingerprint/queue/graphql metrics; add `group` label to `SyncPRDuration`.
- `cmd/pg-pr/sync.go` — `--interval` default 10m → 60s.
- `phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix` — `interval` 5m → 1m.
- `phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr-ops.json` — repoint freshness panel + add panels.
- `docs/adr/index.md` — add the 0012 row.

---

## Task 1: Fingerprint provider — types

**Files:**

- Modify: `pkg/provider/vcs/iface.go`

- [ ] **Step 1: Add the fingerprint types and capability interface**

Append to `pkg/provider/vcs/iface.go` (after `EnrichedPRsProvider`):

```go
// PRFingerprint is the change-detection signature for one open PR, fetched
// by FingerprintProvider. It is intentionally small: just enough to decide
// "did this PR change since last tick?" without any node bodies.
type PRFingerprint struct {
	Repo              string
	Number            int
	Author            string // canonical login (bot suffix normalized)
	IsDraft           bool
	State             string // lowercased: open/closed/merged
	UpdatedAt         string
	HeadOID           string // last commit oid — catches pushes updated_at misses
	StatusRollup      string // statusCheckRollup.state, "" when none
	ReviewCount       int
	CommentCount      int
	ReviewThreadCount int
}

// FingerprintResult bundles one fingerprint query's PRs with pagination and
// rate-limit telemetry. Truncated is true when a hard page cap was hit before
// pagination completed — the caller MUST treat the roster as incomplete (do
// not infer "disappeared" from a truncated result).
type FingerprintResult struct {
	PRs       []PRFingerprint
	Truncated bool
	RateCost  int // rateLimit.cost from the GraphQL envelope
	RateLeft  int // rateLimit.remaining
}

// FingerprintProvider is an optional capability for VCS providers that can
// cheaply fetch per-PR change signatures via one (paginated) search. No repo
// arg: the search may span repos and each node carries its own repo. (The
// EnrichedPRsProvider keeps its repo arg for error context; this one does not
// — keep the asymmetry.)
type FingerprintProvider interface {
	FingerprintPRs(ctx context.Context, searchQuery string) (FingerprintResult, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./pkg/provider/vcs/...`
Expected: builds clean (no impl yet; types only).

- [ ] **Step 3: Commit**

```bash
git add pkg/provider/vcs/iface.go
git commit -m "feat(pg-pr): add FingerprintProvider capability types"
```

---

## Task 2: Fingerprint query + parser (single page)

**Files:**

- Create: `pkg/provider/vcs/github/fingerprint.go`
- Test: `pkg/provider/vcs/github/fingerprint_test.go`

The `github` package already has `ghPageInfo`, `ghUser`, `canonicalLogin`, and the `fakeStdinRunner` test fake (in `enrich_test.go`) — reuse them.

- [ ] **Step 1: Write the failing parser test**

Create `pkg/provider/vcs/github/fingerprint_test.go`:

```go
package github

import (
	"context"
	"errors"
	"testing"
)

func TestParseFingerprints_Basic(t *testing.T) {
	raw := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4999},
	  "search":{"pageInfo":{"hasNextPage":false,"endCursor":"Y"},"nodes":[
	    {"number":7,"updatedAt":"2026-06-09T00:00:00Z","isDraft":false,"state":"OPEN",
	     "author":{"__typename":"User","login":"alice"},
	     "repository":{"nameWithOwner":"o/r"},
	     "commits":{"nodes":[{"commit":{"oid":"abc","statusCheckRollup":{"state":"SUCCESS"}}}]},
	     "reviews":{"totalCount":2},"comments":{"totalCount":3},"reviewThreads":{"totalCount":1}},
	    {"number":8,"updatedAt":"2026-06-09T01:00:00Z","isDraft":true,"state":"OPEN",
	     "author":{"__typename":"Bot","login":"claude"},
	     "repository":{"nameWithOwner":"o/r2"},
	     "commits":{"nodes":[{"commit":{"oid":"def","statusCheckRollup":null}}]},
	     "reviews":{"totalCount":0},"comments":{"totalCount":0},"reviewThreads":{"totalCount":0}}
	  ]}}}`)
	res, cursor, more, err := parseFingerprints(raw)
	if err != nil {
		t.Fatalf("parseFingerprints: %v", err)
	}
	if more || cursor != "Y" {
		t.Fatalf("pageInfo: more=%v cursor=%q", more, cursor)
	}
	if res.RateCost != 1 || res.RateLeft != 4999 {
		t.Errorf("rate: cost=%d left=%d", res.RateCost, res.RateLeft)
	}
	if len(res.PRs) != 2 {
		t.Fatalf("want 2 PRs, got %d", len(res.PRs))
	}
	p0 := res.PRs[0]
	if p0.Repo != "o/r" || p0.Number != 7 || p0.Author != "alice" ||
		p0.State != "open" || p0.HeadOID != "abc" || p0.StatusRollup != "SUCCESS" ||
		p0.ReviewCount != 2 || p0.CommentCount != 3 || p0.ReviewThreadCount != 1 {
		t.Errorf("p0 mismatch: %+v", p0)
	}
	if res.PRs[1].Author != "claude[bot]" {
		t.Errorf("bot login not canonicalized: %q", res.PRs[1].Author)
	}
	if res.PRs[1].StatusRollup != "" {
		t.Errorf("nil rollup should yield empty StatusRollup, got %q", res.PRs[1].StatusRollup)
	}
}

func TestParseFingerprints_GraphQLError(t *testing.T) {
	_, _, _, err := parseFingerprints([]byte(`{"errors":[{"message":"boom"}]}`))
	if err == nil {
		t.Fatal("want error on errors envelope")
	}
}

func TestFingerprintPRs_SinglePage(t *testing.T) {
	raw := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":10},
	  "search":{"pageInfo":{"hasNextPage":false},"nodes":[
	    {"number":1,"state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"},
	     "commits":{"nodes":[{"commit":{"oid":"x"}}]}}]}}}`)
	p := NewWithRunner(&fakeStdinRunner{out: raw})
	res, err := p.FingerprintPRs(context.Background(), "is:pr is:open author:me")
	if err != nil {
		t.Fatalf("FingerprintPRs: %v", err)
	}
	if res.Truncated || len(res.PRs) != 1 || res.PRs[0].Number != 1 {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestFingerprintPRs_EmptySearchRejected(t *testing.T) {
	p := NewWithRunner(&fakeStdinRunner{out: []byte("{}")})
	if _, err := p.FingerprintPRs(context.Background(), "  "); err == nil {
		t.Fatal("want error on empty search")
	}
}

func TestFingerprintPRs_RunnerError(t *testing.T) {
	p := NewWithRunner(&fakeStdinRunner{err: errors.New("gh boom")})
	if _, err := p.FingerprintPRs(context.Background(), "is:pr"); err == nil {
		t.Fatal("want error when runner fails")
	}
}
```

- [ ] **Step 2: Run it red**

Run: `go test ./pkg/provider/vcs/github/ -run TestFingerprint -v`
Expected: FAIL — `parseFingerprints`/`FingerprintPRs` undefined.

- [ ] **Step 3: Implement `fingerprint.go` (single page; pagination added in Task 3)**

Create `pkg/provider/vcs/github/fingerprint.go`:

```go
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fingerprintQuery is the slim sibling of enrichedPRsQuery: no node bodies,
// only change-detection fields + updatedAt. $after drives pagination (null on
// the first page).
const fingerprintQuery = `query($search: String!, $after: String) {
  rateLimit { cost remaining }
  search(query: $search, type: ISSUE, first: 100, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        updatedAt
        isDraft
        state
        author { __typename login }
        repository { nameWithOwner }
        commits(last: 1) { nodes { commit { oid statusCheckRollup { state } } } }
        reviews { totalCount }
        comments { totalCount }
        reviewThreads { totalCount }
      }
    }
  }
}`

// maxFingerprintPages caps pagination so a pathological roster can't loop
// forever; hitting it sets Truncated.
const maxFingerprintPages = 20

type ghFingerprintResponse struct {
	Data struct {
		RateLimit struct {
			Cost      int `json:"cost"`
			Remaining int `json:"remaining"`
		} `json:"rateLimit"`
		Search struct {
			PageInfo ghPageInfo      `json:"pageInfo"`
			Nodes    []ghFPNode      `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
	Errors []ghGraphQLError `json:"errors"`
}

type ghFPNode struct {
	Number     int     `json:"number"`
	UpdatedAt  string  `json:"updatedAt"`
	IsDraft    bool    `json:"isDraft"`
	State      string  `json:"state"`
	Author     *ghUser `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				OID               string `json:"oid"`
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews       struct{ TotalCount int `json:"totalCount"` } `json:"reviews"`
	Comments      struct{ TotalCount int `json:"totalCount"` } `json:"comments"`
	ReviewThreads struct{ TotalCount int `json:"totalCount"` } `json:"reviewThreads"`
}

// parseFingerprints decodes one page. Returns the page's PRs+rate info, the
// endCursor, whether another page exists, and any error.
func parseFingerprints(raw []byte) (vcs.FingerprintResult, string, bool, error) {
	var resp ghFingerprintResponse
	if e := json.Unmarshal(raw, &resp); e != nil {
		return vcs.FingerprintResult{}, "", false, fmt.Errorf("github: parse fingerprint response: %w", e)
	}
	if len(resp.Errors) > 0 {
		return vcs.FingerprintResult{}, "", false, fmt.Errorf("github: GraphQL error: %s", resp.Errors[0].Message)
	}
	res := vcs.FingerprintResult{
		RateCost: resp.Data.RateLimit.Cost,
		RateLeft: resp.Data.RateLimit.Remaining,
		PRs:      make([]vcs.PRFingerprint, 0, len(resp.Data.Search.Nodes)),
	}
	for _, n := range resp.Data.Search.Nodes {
		if n.Number == 0 {
			continue // non-PullRequest node
		}
		fp := vcs.PRFingerprint{
			Repo:              n.Repository.NameWithOwner,
			Number:            n.Number,
			Author:            n.Author.canonicalLogin(),
			IsDraft:           n.IsDraft,
			State:             strings.ToLower(n.State),
			UpdatedAt:         n.UpdatedAt,
			ReviewCount:       n.Reviews.TotalCount,
			CommentCount:      n.Comments.TotalCount,
			ReviewThreadCount: n.ReviewThreads.TotalCount,
		}
		if len(n.Commits.Nodes) > 0 {
			fp.HeadOID = n.Commits.Nodes[0].Commit.OID
			if r := n.Commits.Nodes[0].Commit.StatusCheckRollup; r != nil {
				fp.StatusRollup = r.State
			}
		}
		res.PRs = append(res.PRs, fp)
	}
	return res, resp.Data.Search.PageInfo.EndCursor, resp.Data.Search.PageInfo.HasNextPage, nil
}

// FingerprintPRs runs the fingerprint search, paginating until the roster is
// complete or maxFingerprintPages is hit (Truncated=true). RateCost/RateLeft
// reflect the last page fetched.
func (p *Provider) FingerprintPRs(ctx context.Context, searchQuery string) (vcs.FingerprintResult, error) {
	if strings.TrimSpace(searchQuery) == "" {
		return vcs.FingerprintResult{}, fmt.Errorf("github: search query required for FingerprintPRs")
	}
	var acc vcs.FingerprintResult
	cursor := ""
	for page := 0; ; page++ {
		args := []string{"api", "graphql", "-F", "search=" + searchQuery, "-F", "query=@-"}
		if cursor != "" {
			args = append(args, "-F", "after="+cursor)
		}
		raw, err := p.gh.RunStdin(ctx, []byte(fingerprintQuery), args...)
		if err != nil {
			return vcs.FingerprintResult{}, fmt.Errorf("github: gh api graphql (fingerprint): %w", err)
		}
		pageRes, next, more, err := parseFingerprints(raw)
		if err != nil {
			return vcs.FingerprintResult{}, err
		}
		acc.PRs = append(acc.PRs, pageRes.PRs...)
		acc.RateCost = pageRes.RateCost
		acc.RateLeft = pageRes.RateLeft
		if !more {
			break
		}
		if page+1 >= maxFingerprintPages {
			acc.Truncated = true
			break
		}
		cursor = next
	}
	return acc, nil
}

// Compile-time check that *Provider satisfies vcs.FingerprintProvider.
var _ vcs.FingerprintProvider = (*Provider)(nil)
```

- [ ] **Step 4: Run it green**

Run: `go test ./pkg/provider/vcs/github/ -run TestFingerprint -v`
Expected: PASS.

- [ ] **Step 5: Add a pagination test**

Append to `fingerprint_test.go`:

```go
// pagingRunner returns a different page per RunStdin call.
type pagingRunner struct {
	pages [][]byte
	calls int
}

func (r *pagingRunner) Run(_ context.Context, _ ...string) ([]byte, error) { return nil, nil }
func (r *pagingRunner) RunStdin(_ context.Context, _ []byte, _ ...string) ([]byte, error) {
	out := r.pages[r.calls]
	r.calls++
	return out, nil
}

func TestFingerprintPRs_Paginates(t *testing.T) {
	page1 := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":9},
	  "search":{"pageInfo":{"hasNextPage":true,"endCursor":"C1"},"nodes":[
	    {"number":1,"state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"},"commits":{"nodes":[{"commit":{"oid":"a"}}]}}]}}}`)
	page2 := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":8},
	  "search":{"pageInfo":{"hasNextPage":false},"nodes":[
	    {"number":2,"state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"},"commits":{"nodes":[{"commit":{"oid":"b"}}]}}]}}}`)
	r := &pagingRunner{pages: [][]byte{page1, page2}}
	res, err := NewWithRunner(r).FingerprintPRs(context.Background(), "is:pr is:open author:me")
	if err != nil {
		t.Fatalf("FingerprintPRs: %v", err)
	}
	if r.calls != 2 {
		t.Errorf("want 2 page fetches, got %d", r.calls)
	}
	if len(res.PRs) != 2 || res.PRs[0].Number != 1 || res.PRs[1].Number != 2 {
		t.Errorf("pages not accumulated: %+v", res.PRs)
	}
}
```

- [ ] **Step 6: Run it green, then commit**

Run: `go test ./pkg/provider/vcs/github/ -run TestFingerprint -v`
Expected: PASS.

```bash
git add pkg/provider/vcs/github/fingerprint.go pkg/provider/vcs/github/fingerprint_test.go
git commit -m "feat(pg-pr): paginated GraphQL fingerprint query"
```

---

## Task 3: Extract `buildPRInput` (with human-label fix)

The per-PR snapshot body currently lives inline in `buildAndStoreSnapshot` (`internal/sync/sync.go`). Extract it so `refreshPR` (Task 5) can reuse it. **Critical:** the inline code applies `human` labels only when a `*beads.TickCache` is present; the per-PR path has no cache, so `buildPRInput` must fetch `HumanLabeledBeads` itself or `WaitingOnMe` regresses.

**Files:**

- Modify: `internal/sync/sync.go`
- Test: `internal/sync/refresh_test.go` (new)

- [ ] **Step 1: Write the failing test (human label on cache-less path)**

Create `internal/sync/refresh_test.go`:

```go
package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func TestBuildPRInput_AppliesHumanLabelWithoutCache(t *testing.T) {
	// A merge-request bead with a human-labeled open dependent must produce
	// WaitingOnMe=true even on the cache-less per-PR path.
	bdc := &humanLabelFakeBeads{
		mrID:        "mr-1",
		depsUp:      []depRow{{ID: "fb-1", Status: "open"}},
		humanLabels: map[string]bool{"fb-1": true},
	}
	e := newTestEngine(t, &config.Config{SelfLogin: "me"}, bdc)
	pr := api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"}

	in := e.buildPRInput(context.Background(), pr, nil, bdc, nil, config.RepoConfig{Remote: "o/r"})

	if len(in.BeadsDeps) != 1 {
		t.Fatalf("want 1 dep, got %d", len(in.BeadsDeps))
	}
	found := false
	for _, l := range in.BeadsDeps[0].Labels {
		if l == "human" {
			found = true
		}
	}
	if !found {
		t.Fatal("human label not applied on cache-less path; WaitingOnMe will regress")
	}
}
```

> The executing engineer defines `humanLabelFakeBeads` (implementing the
> `BeadClient` subset plus `FindByRepoAndNumber`/`DepTreeUp`/`HumanLabeledBeads`
> used by `buildPRInput`) and `newTestEngine` helper in this file, mirroring the
> existing fakes in `sync_test.go`. `depRow` mirrors `beads.DepNode`.

- [ ] **Step 2: Run it red**

Run: `go test ./internal/sync/ -run TestBuildPRInput -v`
Expected: FAIL — `buildPRInput` undefined.

- [ ] **Step 3: Extract `buildPRInput` from `buildAndStoreSnapshot`**

In `internal/sync/sync.go`, factor the per-PR body of `buildAndStoreSnapshot`'s loop (the block building one `snapshot.PRInput` — reviews/comments/CI gather + dep tree) into:

```go
// buildPRInput gathers one PR's snapshot input: enrichment (reviews/comments/
// CI) and the bd dep tree with human labels overlaid. enriched, when non-nil,
// supplies bulk-fetched VCS data; otherwise per-PR REST is used. cache, when
// non-nil, answers dep-tree + human-label lookups; otherwise this function
// fetches HumanLabeledBeads itself (the per-PR/daemon path).
func (e *Engine) buildPRInput(ctx context.Context, pr api.PR, enriched *vcs.EnrichedPR, bdc BeadClient, cache *beads.TickCache, rcfg config.RepoConfig) snapshot.PRInput {
	if pr.Repo == "" {
		pr.Repo = rcfg.Remote
	}
	in := snapshot.PRInput{PR: pr}

	// reviews/comments/CI — prefer bulk enrichment, else per-PR REST.
	// (Move the existing sync.go:572-601 logic here verbatim.)
	// ... reviews/comments/CIRuns assignment ...

	// dep tree + human labels.
	if c, ok := bdc.(*beads.Client); ok {
		var mrID string
		if cache != nil {
			if mr, found := cache.FindMergeRequest(pr.Repo, pr.Number); found {
				mrID = mr.ID
			}
		}
		if mrID == "" {
			if mr, ferr := c.FindByRepoAndNumber(ctx, pr.Repo, pr.Number); ferr == nil && mr != nil {
				mrID = mr.ID
			}
		}
		if mrID != "" {
			var deps []beads.DepNode
			if cache != nil {
				if cached, ok := cache.DepsUpFor(mrID); ok {
					deps = cached
				}
			}
			if deps == nil {
				if live, derr := c.DepTreeUp(ctx, mrID); derr == nil {
					deps = live
				}
			}
			// Human-label overlay: cache when present, else fetch directly.
			if cache != nil {
				beads.ApplyHumanLabels(deps, cache.HumanLabeled)
			} else if set, herr := c.HumanLabeledBeads(ctx); herr == nil {
				beads.ApplyHumanLabels(deps, set)
			}
			in.BeadsDeps = deps
		}
	}
	return in
}
```

Then refactor `buildAndStoreSnapshot` to call `e.buildPRInput(ctx, pr, enriched, bdc, cachesByRepo[key.Repo], rcfg)` per PR instead of the inline block. Keep its existing behavior identical for the full-sync path (it passes the cache, so `HumanLabeledBeads` is not re-fetched).

- [ ] **Step 4: Run it green**

Run: `go test ./internal/sync/ -run TestBuildPRInput -v`
Expected: PASS.

- [ ] **Step 5: Run the whole sync package to confirm no regression in `buildAndStoreSnapshot`**

Run: `go test ./internal/sync/...`
Expected: PASS (existing snapshot tests still green).

- [ ] **Step 6: Commit**

```bash
git add internal/sync/sync.go internal/sync/refresh_test.go
git commit -m "refactor(pg-pr): extract buildPRInput; fix human labels on cache-less path"
```

---

## Task 4: `refreshPR` + dormant-mark + shared `applyFetchedPR`

`refreshPR` is the daemon worker's per-PR entry point. It returns `(*snapshot.PRInput, error)`: a non-nil input means "upsert into the snapshot"; a nil input means "remove from the snapshot" (closed/merged, or team-draft hidden). It is the **only** place beads are closed/marked, decided from real `GetPR` state.

**Files:**

- Modify: `internal/sync/sync.go` (extract `applyFetchedPR` from `SyncPR`)
- Create: `internal/sync/refresh.go`
- Test: `internal/sync/refresh_test.go`

- [ ] **Step 1: Write failing tests for the three outcomes**

Append to `internal/sync/refresh_test.go`:

```go
func TestRefreshPR_ClosedMerged_ClosesAndRemoves(t *testing.T) {
	bdc := &refreshFakeBeads{existing: &mrRow{id: "mr-1", repo: "o/r", number: 1}}
	vcs := &fakeVCS{getPR: api.PR{Repo: "o/r", Number: 1, Author: "me", State: "closed", Merged: true}}
	e := newRefreshEngine(t, &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}, bdc, vcs)

	in, err := e.refreshPR(context.Background(), "o/r", 1)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in != nil {
		t.Errorf("closed PR must remove from snapshot (nil input), got %+v", in)
	}
	if !bdc.closed {
		t.Error("closed PR must close the bead")
	}
}

func TestRefreshPR_TeamDraft_MarksDraftKeepsBeadRemovesFromSnapshot(t *testing.T) {
	bdc := &refreshFakeBeads{}
	vcs := &fakeVCS{getPR: api.PR{Repo: "o/r", Number: 2, Author: "teammate", State: "open", Draft: true}}
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r", TeamMembers: []string{"teammate"}}}}
	e := newRefreshEngine(t, cfg, bdc, vcs)

	in, err := e.refreshPR(context.Background(), "o/r", 2)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in != nil {
		t.Errorf("team draft must be hidden (nil input), got %+v", in)
	}
	if bdc.closed {
		t.Error("team draft must NOT close the bead")
	}
	if bdc.lastEnsureState != "draft" {
		t.Errorf("team draft bead must be marked state=draft, got %q", bdc.lastEnsureState)
	}
	if bdc.feedbackRan {
		t.Error("team draft must NOT run the feedback pipeline")
	}
}

func TestRefreshPR_ActiveMine_UpsertsSnapshot(t *testing.T) {
	bdc := &refreshFakeBeads{}
	vcs := &fakeVCS{getPR: api.PR{Repo: "o/r", Number: 3, Author: "me", State: "open"}}
	e := newRefreshEngine(t, &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}, bdc, vcs)

	in, err := e.refreshPR(context.Background(), "o/r", 3)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in == nil || in.PR.Number != 3 {
		t.Fatalf("active PR must upsert a snapshot input, got %+v", in)
	}
}
```

> Define `refreshFakeBeads` (records `closed`, `lastEnsureState`, `feedbackRan`),
> `mrRow`, `fakeVCS.getPR`, and `newRefreshEngine` in this file following the
> `sync_test.go` fake patterns. `fakeVCS` must implement `GetPR` returning
> `&f.getPR`.

- [ ] **Step 2: Run it red**

Run: `go test ./internal/sync/ -run TestRefreshPR -v`
Expected: FAIL — `refreshPR` undefined.

- [ ] **Step 3: Extract `applyFetchedPR` from `SyncPR`**

In `sync.go`, factor `SyncPR`'s body that operates on an already-fetched open PR (the `EnsureMergeRequest` + `processFeedback` + self-only `maybePromoteDraft` + reply drafts block, sync.go:780-804) into:

```go
// applyFetchedPR runs the bead-upsert + feedback + (self) draft-promote
// pipeline for an OPEN, active PR. Caller has already handled closed/merged.
func (e *Engine) applyFetchedPR(ctx context.Context, bdc BeadClient, rcfg config.RepoConfig, pr *api.PR, summary *Summary) (prBeadID string, err error) {
	fields := beads.MergeRequestFields{
		Repo: rcfg.Remote, PRNumber: pr.Number, State: stateForPR(*pr),
		Branch: pr.Branch, Base: pr.Base, Author: pr.Author, URL: pr.URL,
		LastSyncedAt: e.cfgNow(), Draft: pr.Draft,
	}
	id, alreadyClosed, err := bdc.EnsureMergeRequest(ctx, pr.URL, fields)
	if err != nil || alreadyClosed {
		return id, err
	}
	summary.BeadsUpdated = 1
	if err := e.processFeedback(ctx, bdc, nil, nil, rcfg.Remote, *pr, id, summary); err != nil {
		return id, err
	}
	if e.isSelfAuthored(pr.Author) {
		if err := e.maybePromoteDraft(ctx, bdc, nil, rcfg.Remote, *pr, id, summary); err != nil {
			return id, err
		}
	}
	if err := e.processReplyDrafts(ctx, bdc, rcfg, summary); err != nil {
		return id, err
	}
	return id, nil
}
```

(`cfgNow()` is a tiny helper returning `e.deps.Now().UTC().Format(time.RFC3339)`; or inline it.) Refactor `SyncPR` to call `applyFetchedPR` for the open path so the CLI one-shot and the daemon share logic.

- [ ] **Step 4: Implement `refreshPR`**

Create `internal/sync/refresh.go`:

```go
package sync

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// refreshPR fetches one PR and reconciles its bead + snapshot from real state.
// Returns (nil, nil) when the PR should be removed from the dashboard
// (closed/merged, or a hidden team draft); (input, nil) to upsert it.
func (e *Engine) refreshPR(ctx context.Context, repo string, number int) (*snapshot.PRInput, error) {
	rcfg, err := e.repoConfig(repo)
	if err != nil {
		return nil, err
	}
	provider, err := e.providerFor(rcfg)
	if err != nil {
		return nil, err
	}
	bdc := e.bdClientFor(rcfg)
	pr, err := provider.GetPR(ctx, repo, number)
	if err != nil {
		return nil, fmt.Errorf("refreshPR %s#%d: %w", repo, number, err)
	}
	summary := &Summary{}

	// Closed/merged → close bead + cascade, remove from snapshot.
	if pr.State == "closed" || pr.State == "merged" || pr.Merged {
		if existing, ferr := e.findBeadByPR(ctx, bdc, repo, pr.Number); ferr == nil && existing != nil {
			reason := "upstream-" + pr.State
			if pr.Merged {
				reason = "upstream-merged"
			}
			if cerr := bdc.CloseMergeRequest(ctx, existing.ID, reason); cerr == nil {
				e.cascadeClose(ctx, bdc, existing.ID, "pr-closed", summary)
			}
		}
		return nil, nil
	}

	// Team draft → mark bead draft (keep open), skip feedback, hide.
	if !e.isSelfAuthored(pr.Author) && pr.Draft {
		fields := beads.MergeRequestFields{
			Repo: repo, PRNumber: pr.Number, State: "draft",
			Branch: pr.Branch, Base: pr.Base, Author: pr.Author, URL: pr.URL,
			LastSyncedAt: e.cfgNow(), Draft: true,
		}
		_, _, _ = bdc.EnsureMergeRequest(ctx, pr.URL, fields)
		return nil, nil
	}

	// Active (mine any-draft, or team non-draft) → full refresh + snapshot.
	if _, err := e.applyFetchedPR(ctx, bdc, rcfg, pr, summary); err != nil {
		return nil, err
	}
	in := e.buildPRInput(ctx, *pr, nil, bdc, nil, rcfg)
	return &in, nil
}
```

- [ ] **Step 5: Run it green, run package, commit**

Run: `go test ./internal/sync/ -run 'TestRefreshPR|TestBuildPRInput' -v` then `go test ./internal/sync/...`
Expected: PASS.

```bash
git add internal/sync/sync.go internal/sync/refresh.go internal/sync/refresh_test.go
git commit -m "feat(pg-pr): refreshPR with closed/team-draft/active dispatch"
```

---

## Task 5: Dedup FIFO queue

**Files:**

- Create: `internal/sync/queue.go`
- Test: `internal/sync/queue_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/sync/queue_test.go`:

```go
package sync

import "testing"

func TestRefreshQueue_DedupAndFIFO(t *testing.T) {
	q := newRefreshQueue()
	q.enqueue(prKey{Repo: "o/r", Number: 1})
	q.enqueue(prKey{Repo: "o/r", Number: 2})
	q.enqueue(prKey{Repo: "o/r", Number: 1}) // dup → ignored, keeps position
	if got := q.depth(); got != 2 {
		t.Fatalf("depth = %d, want 2", got)
	}
	k1, ok := q.dequeue()
	if !ok || k1 != (prKey{Repo: "o/r", Number: 1}) {
		t.Fatalf("first dequeue = %+v ok=%v, want o/r#1", k1, ok)
	}
	k2, _ := q.dequeue()
	if k2 != (prKey{Repo: "o/r", Number: 2}) {
		t.Fatalf("second dequeue = %+v, want o/r#2", k2)
	}
	if _, ok := q.dequeue(); ok {
		t.Fatal("empty queue should return ok=false")
	}
	// Re-enqueue after dequeue is allowed.
	q.enqueue(prKey{Repo: "o/r", Number: 1})
	if q.depth() != 1 {
		t.Fatal("re-enqueue after drain should add")
	}
}
```

- [ ] **Step 2: Run it red**

Run: `go test ./internal/sync/ -run TestRefreshQueue -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement the queue**

Create `internal/sync/queue.go`:

```go
package sync

import "sync"

// refreshQueue is a dedup FIFO of prKeys. Enqueuing a key already pending is a
// no-op that keeps its position. Safe for concurrent producers/consumers.
type refreshQueue struct {
	mu      sync.Mutex
	order   []prKey
	pending map[prKey]struct{}
}

func newRefreshQueue() *refreshQueue {
	return &refreshQueue{pending: map[prKey]struct{}{}}
}

func (q *refreshQueue) enqueue(k prKey) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[k]; ok {
		return
	}
	q.pending[k] = struct{}{}
	q.order = append(q.order, k)
}

func (q *refreshQueue) dequeue() (prKey, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		return prKey{}, false
	}
	k := q.order[0]
	q.order = q.order[1:]
	delete(q.pending, k)
	return k, true
}

func (q *refreshQueue) depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.order)
}
```

- [ ] **Step 4: Run it green, commit**

Run: `go test ./internal/sync/ -run TestRefreshQueue -v`
Expected: PASS.

```bash
git add internal/sync/queue.go internal/sync/queue_test.go
git commit -m "feat(pg-pr): dedup FIFO refresh queue"
```

---

## Task 6: Snapshot owner (incremental, sorted, single goroutine)

**Files:**

- Create: `internal/sync/snapshotowner.go`
- Test: `internal/sync/snapshotowner_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/sync/snapshotowner_test.go`:

```go
package sync

import (
	"sort"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func TestSnapshotModel_UpsertDeleteSorted(t *testing.T) {
	m := newSnapshotModel()
	m.upsert(snapshot.PRInput{PR: api.PR{Repo: "o/r", Number: 2, Author: "me", State: "open"}})
	m.upsert(snapshot.PRInput{PR: api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"}})
	m.upsert(snapshot.PRInput{PR: api.PR{Repo: "a/b", Number: 9, Author: "me", State: "open"}})
	got := m.sortedInputs()
	want := []struct {
		repo string
		num  int
	}{{"a/b", 9}, {"o/r", 1}, {"o/r", 2}}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	for i, w := range want {
		if got[i].PR.Repo != w.repo || got[i].PR.Number != w.num {
			t.Errorf("[%d] = %s#%d, want %s#%d", i, got[i].PR.Repo, got[i].PR.Number, w.repo, w.num)
		}
	}
	m.delete(prKey{Repo: "o/r", Number: 1})
	if got := m.sortedInputs(); len(got) != 2 {
		t.Fatalf("after delete len = %d, want 2", len(got))
	}
	_ = sort.IntSlice(nil) // keep import if unused after edits
}
```

- [ ] **Step 2: Run it red**

Run: `go test ./internal/sync/ -run TestSnapshotModel -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement the model + owner goroutine**

Create `internal/sync/snapshotowner.go`:

```go
package sync

import (
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

// snapshotModel holds the authoritative per-PR inputs. Not concurrency-safe by
// itself; the owner goroutine is its sole mutator.
type snapshotModel struct {
	prs map[prKey]snapshot.PRInput
}

func newSnapshotModel() *snapshotModel {
	return &snapshotModel{prs: map[prKey]snapshot.PRInput{}}
}

func (m *snapshotModel) upsert(in snapshot.PRInput) {
	m.prs[prKey{Repo: in.PR.Repo, Number: in.PR.Number}] = in
}
func (m *snapshotModel) delete(k prKey) { delete(m.prs, k) }

// sortedInputs returns inputs deterministically ordered by repo then number,
// so per-PR rebuilds don't reshuffle dashboard rows.
func (m *snapshotModel) sortedInputs() []snapshot.PRInput {
	out := make([]snapshot.PRInput, 0, len(m.prs))
	for _, v := range m.prs {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PR.Repo != out[j].PR.Repo {
			return out[i].PR.Repo < out[j].PR.Repo
		}
		return out[i].PR.Number < out[j].PR.Number
	})
	return out
}

// snapshotUpdate is the message workers send the owner. Input==nil → delete.
type snapshotUpdate struct {
	Key   prKey
	Input *snapshot.PRInput
}

// runSnapshotOwner owns the model and rebuilds+Sets the store per update until
// updates is closed. Build inputs (Self/TeamMembers/Registry/interval) come
// from the engine via the supplied closure so SIGHUP changes are picked up.
func (e *Engine) runSnapshotOwner(updates <-chan snapshotUpdate, store *snapshot.Store) {
	m := newSnapshotModel()
	for u := range updates {
		if u.Input == nil {
			m.delete(u.Key)
		} else {
			m.upsert(*u.Input)
		}
		store.Set(snapshot.Build(snapshot.BuilderInput{
			GeneratedAt:         e.deps.Now(),
			SyncIntervalSeconds: int(e.deps.SyncInterval.Seconds()),
			Self:                e.cfg().SelfLogin,
			TeamMembers:         e.allTeamMembers(),
			Registry:            e.deps.AgentRegistry,
			PRs:                 m.sortedInputs(),
		}))
	}
}
```

> `e.cfg()` is the atomic accessor added in Task 7. If Task 7 isn't done yet,
> temporarily use `e.deps.Cfg`; Task 7's test will catch the switch.

- [ ] **Step 4: Run it green, commit**

Run: `go test ./internal/sync/ -run TestSnapshotModel -v`
Expected: PASS.

```bash
git add internal/sync/snapshotowner.go internal/sync/snapshotowner_test.go
git commit -m "feat(pg-pr): incremental sorted snapshot owner"
```

---

## Task 7: Atomic config accessor

Move `e.deps.Cfg` reads behind `e.cfg()` backed by an `atomic.Pointer`, so the
detector/workers/owner can read config while SIGHUP swaps it.

**Files:**

- Modify: `internal/sync/sync.go` (Engine struct, accessor, all readers), `internal/sync/daemon.go` (`ReplaceCfg`)
- Test: `internal/sync/refresh_test.go` (add a race-safe swap test)

- [ ] **Step 1: Write the failing test**

Append to `internal/sync/refresh_test.go`:

```go
func TestEngineCfg_AtomicSwap(t *testing.T) {
	e := newRefreshEngine(t, &config.Config{SelfLogin: "old"}, &refreshFakeBeads{}, &fakeVCS{})
	if e.cfg().SelfLogin != "old" {
		t.Fatalf("cfg() initial = %q", e.cfg().SelfLogin)
	}
	e.ReplaceCfg(&config.Config{SelfLogin: "new"})
	if e.cfg().SelfLogin != "new" {
		t.Fatalf("cfg() after swap = %q", e.cfg().SelfLogin)
	}
}
```

- [ ] **Step 2: Run it red**

Run: `go test ./internal/sync/ -run TestEngineCfg_AtomicSwap -v`
Expected: FAIL — `cfg()` undefined.

- [ ] **Step 3: Add the accessor and migrate readers**

In `sync.go`, change `Engine` to hold the config atomically:

```go
type Engine struct {
	deps Deps
	cfgP atomic.Pointer[config.Config]
}
```

In `New`, after defaulting, `e := &Engine{deps: d}; e.cfgP.Store(d.Cfg); return e, nil`. Add:

```go
func (e *Engine) cfg() *config.Config { return e.cfgP.Load() }
func (e *Engine) cfgNow() string       { return e.deps.Now().UTC().Format(time.RFC3339) }
```

Replace **every** `e.deps.Cfg` read with `e.cfg()` in: `Sync` (range over repos), `repoConfig`, `isSelfAuthored`, `allTeamMembers`, `tryEnumerateEnriched`, and `buildAndStoreSnapshot`. In `daemon.go`, change `ReplaceCfg` to `if cfg != nil { e.cfgP.Store(cfg) }`. Remove the `e.deps.Cfg = cfg` line. Keep `Deps.Cfg` as the seed only.

> Note the existing `daemon_test.go` reads `e.deps.Cfg.SelfLogin` directly to
> assert reload. Update those reads to `e.cfg().SelfLogin` (3 sites:
> `TestDaemon_SighupReloadsConfig`, `TestDaemon_SighupReloadFailureKeepsPreviousConfig`,
> `TestReplaceCfg_NilIsNoop`, `makeDaemonEngine` precondition).

- [ ] **Step 4: Run the full package with the race detector**

Run: `go test ./internal/sync/... -race`
Expected: PASS, no races.

- [ ] **Step 5: Commit**

```bash
git add internal/sync/sync.go internal/sync/daemon.go internal/sync/refresh_test.go internal/sync/daemon_test.go
git commit -m "refactor(pg-pr): engine config behind atomic.Pointer for race-free SIGHUP"
```

---

## Task 8: Detector — query builders, fingerprint hash, diff

**Files:**

- Create: `internal/sync/detector.go`
- Test: `internal/sync/detector_test.go`

- [ ] **Step 1: Write failing tests for the pure diff**

Create `internal/sync/detector_test.go`:

```go
package sync

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

func fp(repo string, n int, oid string) vcs.PRFingerprint {
	return vcs.PRFingerprint{Repo: repo, Number: n, Author: "me", State: "open", HeadOID: oid}
}

func TestDiffRoster_AddedChangedDisappeared(t *testing.T) {
	prev := map[prKey]string{
		{Repo: "o/r", Number: 1}: fingerprintHash(fp("o/r", 1, "a")),
		{Repo: "o/r", Number: 2}: fingerprintHash(fp("o/r", 2, "b")),
	}
	roster := []vcs.PRFingerprint{
		fp("o/r", 1, "a"),   // unchanged → skip
		fp("o/r", 3, "c"),   // new → added
		fp("o/r", 2, "b2"),  // changed (oid) → changed
	}
	openBeads := map[prKey]bool{
		{Repo: "o/r", Number: 1}: true,
		{Repo: "o/r", Number: 2}: true,
		{Repo: "o/r", Number: 9}: true, // bead with no roster entry → disappeared
	}
	d := diffRoster(prev, roster, openBeads, true /*complete*/)
	if !d.enqueued[prKey{"o/r", 3}] || d.reasons[prKey{"o/r", 3}] != "added" {
		t.Errorf("PR 3 should be added: %+v", d)
	}
	if !d.enqueued[prKey{"o/r", 2}] || d.reasons[prKey{"o/r", 2}] != "changed" {
		t.Errorf("PR 2 should be changed: %+v", d)
	}
	if !d.enqueued[prKey{"o/r", 9}] || d.reasons[prKey{"o/r", 9}] != "disappeared" {
		t.Errorf("bead 9 should be disappeared: %+v", d)
	}
	if d.enqueued[prKey{"o/r", 1}] {
		t.Errorf("PR 1 unchanged should be skipped")
	}
}

func TestDiffRoster_TruncatedSkipsDisappeared(t *testing.T) {
	openBeads := map[prKey]bool{{Repo: "o/r", Number: 9}: true}
	d := diffRoster(map[prKey]string{}, nil, openBeads, false /*incomplete*/)
	if d.enqueued[prKey{"o/r", 9}] {
		t.Error("incomplete roster must NOT enqueue disappeared (mass-close guard)")
	}
}
```

> Note: prKey literal `{Repo:..., Number:...}` — the test uses positional
> `prKey{"o/r", 9}`; ensure `prKey`'s field order is `{Repo string; Number int}`
> (it is, in sync.go). `added` requires "no open bead"; here PR 3 has no bead.

- [ ] **Step 2: Run it red**

Run: `go test ./internal/sync/ -run TestDiffRoster -v`
Expected: FAIL — `fingerprintHash`/`diffRoster` undefined.

- [ ] **Step 3: Implement the detector core**

Create `internal/sync/detector.go`:

```go
package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fingerprintHash is a stable hash of the change-relevant fields.
func fingerprintHash(f vcs.PRFingerprint) string {
	s := fmt.Sprintf("%s|%s|%s|%s|%t|%d|%d|%d",
		f.UpdatedAt, f.HeadOID, f.StatusRollup, f.State, f.IsDraft,
		f.ReviewCount, f.CommentCount, f.ReviewThreadCount)
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// diffResult is the detector's per-tick decision.
type diffResult struct {
	enqueued map[prKey]bool
	reasons  map[prKey]string // added|changed|dormant|disappeared
	roster   map[prKey]string // new prev (hashes)
}

// diffRoster computes enqueues from the fresh roster vs the previous-tick
// hashes and the open-bead set. complete=false (query failed/truncated) skips
// the disappeared check (mass-close guard) but still records the roster.
//
// Caller has already filtered roster to the group it owns and provided the
// matching openBeads + completeness for that (repo,group). Active-vs-dormant
// classification is the caller's (it knows self/team); diffRoster treats every
// roster entry as a candidate and lets the caller route dormant vs refresh —
// but for added/changed it needs to know dormancy, so callers pass only
// ACTIVE roster entries here and handle dormant separately (see fingerprintTick).
func diffRoster(prev map[prKey]string, roster []vcs.PRFingerprint, openBeads map[prKey]bool, complete bool) diffResult {
	d := diffResult{
		enqueued: map[prKey]bool{},
		reasons:  map[prKey]string{},
		roster:   map[prKey]string{},
	}
	inRoster := map[prKey]bool{}
	for _, f := range roster {
		k := prKey{Repo: f.Repo, Number: f.Number}
		h := fingerprintHash(f)
		d.roster[k] = h
		inRoster[k] = true
		old, seen := prev[k]
		if seen && old == h {
			continue // unchanged
		}
		d.enqueued[k] = true
		if openBeads[k] {
			d.reasons[k] = "changed"
		} else {
			d.reasons[k] = "added"
		}
	}
	if complete {
		for k := range openBeads {
			if !inRoster[k] {
				d.enqueued[k] = true
				d.reasons[k] = "disappeared"
			}
		}
	}
	return d
}

// buildMineQuery is the cross-repo "my open PRs" search.
func buildMineQuery(cfg *config.Config) string {
	parts := []string{"is:pr", "is:open", "author:" + cfg.SelfLogin}
	for _, r := range cfg.Repos {
		parts = append(parts, "repo:"+r.Remote)
	}
	return strings.Join(parts, " ")
}

// buildTeamQuery is one repo's "team open PRs" search (drafts included).
func buildTeamQuery(rcfg config.RepoConfig) string {
	if len(rcfg.TeamMembers) == 0 {
		return ""
	}
	parts := []string{"is:pr", "is:open", "repo:" + rcfg.Remote}
	for _, m := range rcfg.TeamMembers {
		parts = append(parts, "author:"+m)
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 4: Run it green, commit**

Run: `go test ./internal/sync/ -run TestDiffRoster -v`
Expected: PASS.

```bash
git add internal/sync/detector.go internal/sync/detector_test.go
git commit -m "feat(pg-pr): fingerprint detector diff + query builders"
```

---

## Task 9: Metrics

**Files:**

- Modify: `internal/telemetry/metrics.go`
- Test: `internal/telemetry/metrics_test.go` (extend existing)

- [ ] **Step 1: Write the failing test**

Append to `internal/telemetry/metrics_test.go`:

```go
func TestFingerprintMetricsRegistered(t *testing.T) {
	// Touch each new metric so a /metrics scrape would emit it.
	FingerprintPollDuration.WithLabelValues("mine").Observe(0.1)
	FingerprintChangesTotal.WithLabelValues("mine", "added").Inc()
	RefreshQueueDepth.WithLabelValues("team").Set(3)
	GraphQLRateRemaining.Set(4999)
	FingerprintPollSuccessTimestamp.WithLabelValues("team").Set(1.0)
	// If any metric were unregistered, the Vec methods above would still work
	// but Gather would omit them; assert they gather.
	mfs, err := DefaultRegistry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	want := map[string]bool{
		"pg_pr_fingerprint_poll_duration_seconds":            false,
		"pg_pr_fingerprint_changes_total":                    false,
		"pg_pr_refresh_queue_depth":                          false,
		"pg_pr_graphql_rate_remaining":                       false,
		"pg_pr_fingerprint_poll_success_timestamp_seconds":   false,
	}
	for _, mf := range mfs {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("metric %q not registered", name)
		}
	}
}
```

- [ ] **Step 2: Run it red**

Run: `go test ./internal/telemetry/ -run TestFingerprintMetrics -v`
Expected: FAIL — metrics undefined.

- [ ] **Step 3: Implement metric changes**

In `internal/telemetry/metrics.go`: change `SyncPRDuration`'s labels from `[]string{"repo"}` to `[]string{"repo", "group"}`; delete `LastSyncSuccessTimestamp` + `ObserveSyncSuccess`; add and register:

```go
FingerprintPollDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{Name: "pg_pr_fingerprint_poll_duration_seconds",
		Help: "Fingerprint poll latency by group.", Buckets: prometheus.DefBuckets},
	[]string{"group"})

FingerprintPollErrorsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{Name: "pg_pr_fingerprint_poll_errors_total",
		Help: "Fingerprint poll errors by group."}, []string{"group"})

FingerprintPollTruncatedTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{Name: "pg_pr_fingerprint_poll_truncated_total",
		Help: "Fingerprint polls that hit the page cap (incomplete roster)."}, []string{"group"})

FingerprintPollSuccessTimestamp = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{Name: "pg_pr_fingerprint_poll_success_timestamp_seconds",
		Help: "Unix time of last successful fingerprint poll by group."}, []string{"group"})

FingerprintChangesTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{Name: "pg_pr_fingerprint_changes_total",
		Help: "Detected changes by group and kind."}, []string{"group", "kind"})

RefreshQueueDepth = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{Name: "pg_pr_refresh_queue_depth",
		Help: "Current refresh queue depth by group."}, []string{"group"})

RefreshEnqueuedTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{Name: "pg_pr_refresh_enqueued_total",
		Help: "PRs enqueued for refresh by group."}, []string{"group"})

GraphQLCost = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{Name: "pg_pr_graphql_cost",
		Help: "Last fingerprint query point cost by group."}, []string{"group"})

GraphQLRateRemaining = prometheus.NewGauge(
	prometheus.GaugeOpts{Name: "pg_pr_graphql_rate_remaining",
		Help: "GraphQL rate-limit points remaining."})
```

Register all in `init()`. Remove `ObserveSyncSuccess` usages in `sync.go` (the `for repo := range healthyRepos { telemetry.ObserveSyncSuccess(...) }` block) — replace with nothing (the daemon now records `FingerprintPollSuccessTimestamp` in Task 10; the one-shot `Sync` no longer records a per-repo timestamp).

Update `SyncPRDuration.WithLabelValues(key.Repo)` call sites to pass the group: in `Sync` use `"mixed"` or derive from `mineSet`; in `refreshPR` the worker passes its group. (Simplest: have the worker call `telemetry.SyncPRDuration.WithLabelValues(repo, group).Observe(...)`.)

- [ ] **Step 4: Run it green, run the sync package (compile after removing `ObserveSyncSuccess`), commit**

Run: `go test ./internal/telemetry/... ./internal/sync/...`
Expected: PASS.

```bash
git add internal/telemetry/metrics.go internal/telemetry/metrics_test.go internal/sync/sync.go
git commit -m "feat(pg-pr): fingerprint/queue/graphql metrics; retire last_sync_success"
```

---

## Task 10: Daemon loop rewrite (wire it all together)

**Files:**

- Modify: `internal/sync/daemon.go`
- Test: `internal/sync/daemon_test.go` (add a tick-drains-to-snapshot test)

- [ ] **Step 1: Write the failing integration test**

Append to `daemon_test.go` (uses a fake VCS that returns one fingerprint then a `GetPR`):

```go
func TestDaemon_FingerprintTickPopulatesSnapshot(t *testing.T) {
	// fakeVCS returns one mine PR from FingerprintPRs and a matching GetPR.
	vcs := &fakeFingerprintVCS{
		mine:  []vcs.PRFingerprint{{Repo: "o/r", Number: 1, Author: "me", State: "open", HeadOID: "a"}},
		getPR: api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"},
	}
	store := snapshot.NewStore()
	e := newFingerprintDaemonEngine(t, vcs) // cfg: SelfLogin "me", one repo "o/r"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- e.Daemon(ctx, DaemonOpts{
			Interval: 20 * time.Millisecond, LockDir: t.TempDir(),
			Logger: discardLogger(), Dashboard: store,
		})
	}()

	// Poll the store until the PR shows up (workers drain async).
	deadline := time.After(2 * time.Second)
	for {
		if snap, ok := store.Get(); ok && len(snap.Mine) == 1 && snap.Mine[0].Number == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("snapshot never populated from fingerprint tick")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit after cancel")
	}
}
```

> Define `fakeFingerprintVCS` (implements `VCSProvider` + `FingerprintPRs` +
> `GetPR`) and `newFingerprintDaemonEngine` here.

- [ ] **Step 2: Run it red**

Run: `go test ./internal/sync/ -run TestDaemon_FingerprintTick -v`
Expected: FAIL — daemon still calls the old `Sync`.

- [ ] **Step 3: Rewrite the `Daemon` loop**

Replace the body of `Daemon` (after lock + metrics-server setup) so it:

1. starts the snapshot-owner goroutine (`updates := make(chan snapshotUpdate, 64); go e.runSnapshotOwner(updates, opts.Dashboard)` when `Dashboard != nil`);
2. creates `mineQ, teamQ := newRefreshQueue(), newRefreshQueue()`;
3. starts two worker goroutines that loop: dequeue (blocking via a small condition/sleep or a signaling channel), call `e.refreshPR`, send the resulting `snapshotUpdate` (Input or nil) to `updates`, update `RefreshQueueDepth`;
4. each interval tick (and once immediately) calls `e.fingerprintTick(ctx, mineQ, teamQ)`;
5. on ctx cancel / SIGHUP behaves as today (SIGHUP calls `opts.ReloadConfig` then `e.ReplaceCfg`);
6. on shutdown: stop ticking, signal workers to drain-and-exit, close `updates`, wait on a `sync.WaitGroup`.

Add `fingerprintTick`:

```go
func (e *Engine) fingerprintTick(ctx context.Context, mineQ, teamQ *refreshQueue) {
	cfg := e.cfg()
	prov, ok := e.firstFingerprintProvider()
	if !ok {
		return
	}
	// MINE (cross-repo, active = any draft state).
	mineRes, mineErr := prov.FingerprintPRs(ctx, buildMineQuery(cfg))
	e.recordPoll("mine", mineRes, mineErr)
	mineBeads := e.openBeadsForGroup(ctx, cfg, true /*mine*/)
	mineDiff := diffRoster(e.prevMine, mineRes.PRs, mineBeads, mineErr == nil && !mineRes.Truncated)
	for k := range mineDiff.enqueued {
		mineQ.enqueue(k)
		telemetry.FingerprintChangesTotal.WithLabelValues("mine", mineDiff.reasons[k]).Inc()
		telemetry.RefreshEnqueuedTotal.WithLabelValues("mine").Inc()
	}
	e.prevMine = mineDiff.roster

	// TEAM (per repo; active = non-draft, dormant = draft).
	// For each repo: run buildTeamQuery, split roster into active (non-draft)
	// and dormant (draft). Pass ACTIVE entries to diffRoster; for dormant
	// entries that are newly-draft, enqueue directly (reason "dormant").
	// Disappeared uses that repo's team open beads with per-repo completeness.
	// (Accumulate prevTeam across repos keyed by prKey.)
	// ... see spec §3 rule 2 ...
}
```

> The engineer implements `firstFingerprintProvider` (type-assert the registered
> `github` provider to `vcs.FingerprintProvider`), `recordPoll` (sets duration,
> errors, truncated, success-timestamp, graphql cost/remaining), `openBeadsForGroup`
> (enumerate each repo's bd workspace via `bdClientFor(rcfg)` + `ListMergeRequests(ctx, false)`,
> keyed by prKey, filtered to the group via `isSelfAuthored(mr.Fields.Author)`),
> and stores `prevMine`/`prevTeam` maps on the Engine. Team handling mirrors the
> spec's §3 rule 2: dormant = team+draft, enqueued only when newly draft.

- [ ] **Step 4: Run it green with the race detector**

Run: `go test ./internal/sync/... -race`
Expected: PASS (the new integration test + all existing daemon tests).

- [ ] **Step 5: Change `DefaultDaemonInterval`**

In `daemon.go`: `const DefaultDaemonInterval = 60 * time.Second`.

- [ ] **Step 6: Commit**

```bash
git add internal/sync/daemon.go internal/sync/detector.go internal/sync/daemon_test.go
git commit -m "feat(pg-pr): fingerprint-driven daemon loop with workers + snapshot owner"
```

---

## Task 11: CLI flag default + nix service interval

**Files:**

- Modify: `cmd/pg-pr/sync.go`
- Modify: `phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix`

- [ ] **Step 1: Change the flag default**

In `cmd/pg-pr/sync.go` `init()`, change the `--interval` default from `"10m"` to `"60s"`:

```go
syncCmd.Flags().StringVar(&syFlags.interval, "interval", "60s",
	"Daemon fingerprint poll interval (effective only with --daemon)")
```

- [ ] **Step 2: Build + smoke test the CLI**

Run: `go build ./cmd/pg-pr && ./pg-pr sync --help | grep -A1 interval`
Expected: shows `default "60s"`.

- [ ] **Step 3: Change the launchd service interval**

In `phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix`, change the `interval` option default:

```nix
    interval = lib.mkOption {
      type = lib.types.str;
      default = "1m";
      description = "Fingerprint poll interval passed to `pg-pr sync --daemon`. Accepts any Go duration (e.g. '30s', '5m').";
    };
```

- [ ] **Step 4: Commit (two repos)**

```bash
git add cmd/pg-pr/sync.go
git commit -m "feat(pg-pr): default daemon interval to 60s (fingerprint cadence)"
# then in the ziprecruiter repo:
git -C ../../../phillipg-nix-ziprecruiter add darwin/services/pg-pr-sync/default.nix
git -C ../../../phillipg-nix-ziprecruiter commit -m "feat(pg-pr-sync): fingerprint poll interval 5m -> 1m"
```

> The `pg-pr` package source lives in `phillipgreenii-nix-agent-support`; the
> launchd service lives in `phillipg-nix-ziprecruiter`. Commit each in its own
> repo. Do not push unless asked.

---

## Task 12: Grafana Ops dashboard

**Files:**

- Modify: `phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr-ops.json`

- [ ] **Step 1: Repoint the freshness panel**

Find the panel with `expr: time() - max(pg_pr_last_sync_success_timestamp_seconds)` and replace with:

```
time() - max by (group)(pg_pr_fingerprint_poll_success_timestamp_seconds)
```

Set the panel to repeat by `group` (or show both series). This MUST land in the same change as retiring the metric (Task 9), or the tile reads "No data".

- [ ] **Step 2: Add new panels (one timeseries each)**

Add panels with these exprs (copy an existing timeseries panel's JSON and swap the `expr`/title):

- `pg_pr_refresh_queue_depth` — "Refresh queue depth (by group)"
- `sum by (group)(rate(pg_pr_fingerprint_poll_duration_seconds_count[$__rate_interval]))` — "Fingerprint poll rate"
- `histogram_quantile(0.95, sum by (le,group)(rate(pg_pr_fingerprint_poll_duration_seconds_bucket[$__rate_interval])))` — "Fingerprint poll p95"
- `sum by (group,kind)(rate(pg_pr_fingerprint_changes_total[$__rate_interval]))` — "Changes detected (by kind)"
- `sum by (group)(rate(pg_pr_fingerprint_poll_truncated_total[$__rate_interval]))` — "Roster truncation rate"
- `pg_pr_graphql_rate_remaining` — "GraphQL rate remaining"

Update any existing `pg_pr_sync_pr_duration_seconds*` panels to keep working with the new `{repo,group}` labels (the existing `sum`/`histogram_quantile` exprs aggregate away labels, so they keep working — verify after).

- [ ] **Step 3: Validate JSON**

Run: `jq . phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr-ops.json > /dev/null && echo OK`
Expected: `OK` (no parse error).

- [ ] **Step 4: Commit (support-apps repo)**

```bash
git -C ../../../phillipgreenii-nix-support-apps add darwin/modules/observability/dashboards/pg-pr-ops.json
git -C ../../../phillipgreenii-nix-support-apps commit -m "feat(observability): pg-pr Ops dashboard for fingerprint-driven sync"
```

---

## Task 13: ADR 0012

**Files:**

- Create: `docs/adr/0012-pg-pr-fingerprint-driven-daemon-sync.md`
- Modify: `docs/adr/index.md`

- [ ] **Step 1: Write the ADR**

Create `docs/adr/0012-pg-pr-fingerprint-driven-daemon-sync.md` using the repo's ADR template (Status: Accepted; Date: 2026-06-09). Decision: replace the daemon's periodic full-sync with a fingerprint-poll change-detector + targeted per-PR refresh. Include Context (staleness vs cost, localhost-only), Consequences (two-tier detector/worker, team-draft dormancy behavior change, atomic config, metric/dashboard churn), and Alternatives Considered (webhooks, `updated_at`, Events/Notifications, conditional REST, bead-stored fingerprints, shorter interval). Cross-link the spec and `0009-pg-pr-bead-schema.md`.

- [ ] **Step 2: Update the index**

Add a row to `docs/adr/index.md` for 0012.

- [ ] **Step 3: Commit**

```bash
git add docs/adr/0012-pg-pr-fingerprint-driven-daemon-sync.md docs/adr/index.md
git commit -m "docs(adr): 0012 pg-pr fingerprint-driven daemon sync"
```

---

## Final verification

- [ ] `go test ./... -race` in `packages/pg-pr` — all green.
- [ ] `go vet ./...` — clean.
- [ ] `gofmt -l .` (or the repo `treefmt`) — no diffs.
- [ ] `pg-pr sync --daemon --interval 60s --metrics-addr 127.0.0.1:9818` locally; confirm `/api/v1/dashboard` populates within ~1 tick and `/metrics` shows the new `pg_pr_fingerprint_*` series.
- [ ] If `flake.nix`/pre-commit are in scope for the touched repos, run `nix flake check` / `prek run --all-files` per each repo's CLAUDE.md before declaring done.

## Notes on ordering and dependencies

- Tasks 1→2 (provider) and 5/6 (queue/snapshot owner) are independent and can be done in any order.
- Task 3 (`buildPRInput`) is the load-bearing seam — Task 4 (`refreshPR`) and Task 10 (daemon) depend on it.
- Task 7 (atomic config) should land before Task 10 (the daemon's goroutines read config concurrently); the snapshot owner in Task 6 references `e.cfg()` — keep the temporary `e.deps.Cfg` shim noted there until Task 7.
- Task 9 (metrics) before Task 10 (the loop emits them) and Task 12 (dashboard reads them).
