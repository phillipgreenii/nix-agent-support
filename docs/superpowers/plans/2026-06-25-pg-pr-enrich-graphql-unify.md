# pg-pr enrich GraphQL unify — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make per-PR enrichment (`pg-pr sync --pr` / `enrichOnePR`) fetch via GraphQL so it produces the same real review-thread node ids (`PRRT_`) and `createdAt` as the bulk daemon path, ending divergent/duplicate `posted_at`-less rows.

**Architecture:** Add a single-PR GraphQL fetch (`EnrichPR`) that reuses the bulk query's PullRequest field selection + parsers, route `enrichOnePR` through a new optional `SinglePREnricher` capability (REST only as hard-error fallback), and backfill `CreatedAt` in the REST `ListComments` path. No change to the bulk daemon path.

**Tech Stack:** Go; `gh api graphql` via the injected `ghRunner`; `internal/sync` engine; bead `pg2-re7e`.

**Spec:** `docs/superpowers/specs/2026-06-25-pg-pr-enrich-graphql-unify-design.md`

**Worktree:** `.worktrees/pg-pr-enrich-graphql-unify` (branch `pg-pr-enrich-graphql-unify`, off local `main`). All paths below are relative to `packages/pg-pr/`.

---

## File Structure

- `pkg/provider/vcs/github/github.go` — add `CreatedAt` to `ghIssueComment`/`ghReviewComment`; map it in `ListComments`.
- `pkg/provider/vcs/github/enrich.go` — extract `prNodeSelection(connFirst)`; rebuild `enrichedPRsQuery`; add `enrichedPRByNumberQuery`, `enrichedPRFromNode`, `parseEnrichedPR`, `(*Provider).EnrichPR`.
- `pkg/provider/vcs/github/github_test.go` — `ListComments` CreatedAt test (arg-aware fake runner).
- `pkg/provider/vcs/github/enrich_test.go` — `parseEnrichedPR` + `EnrichPR` tests.
- `internal/sync/sync.go` — `SinglePREnricher` interface; route `enrichOnePR` through it.
- `internal/sync/refresh_test.go` (or `sync_test.go`) — `enrichOnePR` routing/fallback test.

---

## Task 1: REST `ListComments` populates `CreatedAt`

**Files:**

- Modify: `pkg/provider/vcs/github/github.go` (structs `ghIssueComment` ~line 480, `ghReviewComment` ~line 491; `ListComments` mapping ~lines 533, 566)
- Test: `pkg/provider/vcs/github/github_test.go`

- [ ] **Step 1: Write the failing test**

Add to `github_test.go` (an arg-aware runner is needed because `ListComments` calls `Run` twice — issues then pulls):

```go
// argRunner returns canned stdout based on which gh api path is requested.
type argRunner struct {
	issues, pulls []byte
}

func (r *argRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "/issues/") {
		return r.issues, nil
	}
	if strings.Contains(joined, "/pulls/") {
		return r.pulls, nil
	}
	return []byte("[]"), nil
}
func (r *argRunner) RunStdin(_ context.Context, _ []byte, _ ...string) ([]byte, error) {
	return []byte("[]"), nil
}

func TestListComments_PopulatesCreatedAt(t *testing.T) {
	issues := []byte(`[{"node_id":"IC_1","body":"top","user":{"login":"alice"},"author_association":"MEMBER","created_at":"2026-06-01T10:00:00Z"}]`)
	pulls := []byte(`[{"node_id":"PRRC_1","body":"inline","user":{"login":"bob"},"path":"x.go","line":5,"author_association":"NONE","created_at":"2026-06-02T11:00:00Z"}]`)
	p := NewWithRunner(&argRunner{issues: issues, pulls: pulls})
	got, err := p.ListComments(context.Background(), "o/r", 1)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	byID := map[string]string{}
	for _, c := range got {
		byID[c.ID] = c.CreatedAt
	}
	if byID["IC_1"] != "2026-06-01T10:00:00Z" {
		t.Errorf("issue comment CreatedAt = %q, want 2026-06-01T10:00:00Z", byID["IC_1"])
	}
	if byID["PRRC_1"] != "2026-06-02T11:00:00Z" {
		t.Errorf("review comment CreatedAt = %q, want 2026-06-02T11:00:00Z", byID["PRRC_1"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/provider/vcs/github/ -run TestListComments_PopulatesCreatedAt -v`
Expected: FAIL (CreatedAt empty for both).

- [ ] **Step 3: Add the struct fields**

Both `ghIssueComment` and `ghReviewComment` gain a `CreatedAt` string field with the JSON tag `created_at` (matching the existing field-tag style in those structs).

- [ ] **Step 4: Map it in `ListComments`**

In the issue-comments loop append, add `CreatedAt: c.CreatedAt,`. In the review-comments loop append, add `CreatedAt: c.CreatedAt,`.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/provider/vcs/github/ -run TestListComments_PopulatesCreatedAt -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/provider/vcs/github/github.go pkg/provider/vcs/github/github_test.go
git commit -m "fix(pg-pr): populate CreatedAt in REST ListComments (pg2-re7e)"
```

---

## Task 2: single-PR GraphQL fetch (`EnrichPR`)

**Files:**

- Modify: `pkg/provider/vcs/github/enrich.go`
- Test: `pkg/provider/vcs/github/enrich_test.go`

- [ ] **Step 1: Refactor — extract the shared PullRequest field selection**

Replace the existing `enrichedPRsQuery` constant with a function that templates the four thread-bearing connection page sizes, then rebuild the bulk query from it with a page size of 30 (byte-identical to today) and add the by-number query with 100:

```go
// prNodeSelection returns the PullRequest field selection shared by the bulk
// search query and the single-PR by-number query. connFirst sets the page
// size for the thread-bearing connections (reviews, comments, reviewThreads
// and its nested comments) so the single-PR path can request more than the
// bulk path without changing bulk cost.
func prNodeSelection(connFirst int) string {
	return fmt.Sprintf(`
        number
        title
        url
        author { __typename login }
        baseRefName
        headRefName
        isDraft
        state
        merged
        additions
        deletions
        changedFiles
        repository { nameWithOwner }
        reviews(first: %[1]d) {
          totalCount
          pageInfo { hasNextPage }
          nodes { id state author { __typename login } body submittedAt }
        }
        comments(first: %[1]d, orderBy: { field: UPDATED_AT, direction: DESC }) {
          totalCount
          pageInfo { hasNextPage }
          nodes { id author { __typename login } authorAssociation body createdAt }
        }
        reviewThreads(first: %[1]d) {
          totalCount
          pageInfo { hasNextPage }
          nodes {
            id
            isResolved
            isOutdated
            comments(first: %[1]d) {
              totalCount
              pageInfo { hasNextPage }
              nodes {
                id
                author { __typename login }
                authorAssociation
                body
                path
                originalLine
                line
                createdAt
                isMinimized
                minimizedReason
                originalCommit { oid }
              }
            }
          }
        }
        body
        labels(first: 20) { totalCount pageInfo { hasNextPage } nodes { name } }
        files(first: 100) { totalCount pageInfo { hasNextPage } nodes { path } }
        commits(last: 20) {
          totalCount
          pageInfo { hasNextPage }
          nodes {
            commit {
              oid
              message
              statusCheckRollup {
                state
                contexts(first: 30) {
                  totalCount
                  pageInfo { hasNextPage }
                  nodes {
                    __typename
                    ... on CheckRun { id name status conclusion detailsUrl }
                    ... on StatusContext { id context state targetUrl }
                  }
                }
              }
            }
          }
        }`, connFirst)
}

var enrichedPRsQuery = `query($search: String!) {
  rateLimit { cost remaining resetAt }
  search(query: $search, type: ISSUE, first: 50) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {` + prNodeSelection(30) + `
      }
    }
  }
}`

var enrichedPRByNumberQuery = `query($owner: String!, $name: String!, $number: Int!) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {` + prNodeSelection(100) + `
    }
  }
}`
```

- [ ] **Step 2: Verify the refactor is behavior-preserving**

Run: `go test ./pkg/provider/vcs/github/ -run 'EnrichedPRs|TruncationFlags|ParseEnriched' -v`
Expected: PASS (existing bulk tests unchanged).

- [ ] **Step 3: Write failing tests for `parseEnrichedPR` + `EnrichPR`**

Add to `enrich_test.go`:

```go
func TestParseEnrichedPR_ThreadIDAndCreatedAt(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"pullRequest":{
	  "number":42,"title":"t","author":{"__typename":"User","login":"alice"},
	  "repository":{"nameWithOwner":"o/r"},
	  "reviewThreads":{"nodes":[
	    {"id":"PRRT_abc","isResolved":false,"comments":{"nodes":[
	      {"id":"PRRC_1","author":{"__typename":"Bot","login":"coderabbitai"},"authorAssociation":"NONE","body":"nit","path":"x.go","line":7,"createdAt":"2026-06-03T09:00:00Z"}
	    ]}}
	  ]}}}}}`)
	ep, err := parseEnrichedPR(raw, "o/r")
	if err != nil {
		t.Fatalf("parseEnrichedPR: %v", err)
	}
	if ep == nil || ep.PR.Number != 42 {
		t.Fatalf("unexpected PR: %+v", ep)
	}
	if len(ep.Comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(ep.Comments))
	}
	c := ep.Comments[0]
	if c.ThreadID != "PRRT_abc" {
		t.Errorf("ThreadID = %q, want PRRT_abc", c.ThreadID)
	}
	if c.CreatedAt != "2026-06-03T09:00:00Z" {
		t.Errorf("CreatedAt = %q, want 2026-06-03T09:00:00Z", c.CreatedAt)
	}
}

func TestParseEnrichedPR_TruncationFlag(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"pullRequest":{
	  "number":1,"reviewThreads":{"pageInfo":{"hasNextPage":true},"nodes":[]}}}}}`)
	ep, err := parseEnrichedPR(raw, "o/r")
	if err != nil {
		t.Fatalf("parseEnrichedPR: %v", err)
	}
	if !sliceEq(ep.Truncated, []string{"reviewThreads"}) {
		t.Errorf("Truncated = %v, want [reviewThreads]", ep.Truncated)
	}
}

func TestParseEnrichedPR_NotFound(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"pullRequest":null}}}`)
	if _, err := parseEnrichedPR(raw, "o/r"); err == nil {
		t.Fatal("want error when pullRequest is null")
	}
}

func TestEnrichPR_FakeRunner(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"pullRequest":{"number":7,"title":"t","repository":{"nameWithOwner":"o/r"}}}}}`)
	p := NewWithRunner(&fakeStdinRunner{out: raw})
	ep, err := p.EnrichPR(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatalf("EnrichPR: %v", err)
	}
	if ep.PR.Number != 7 {
		t.Errorf("PR.Number = %d, want 7", ep.PR.Number)
	}
}

func TestEnrichPR_BadRepo(t *testing.T) {
	p := NewWithRunner(&fakeStdinRunner{out: []byte("{}")})
	if _, err := p.EnrichPR(context.Background(), "no-slash", 1); err == nil {
		t.Fatal("want error for repo without owner/name")
	}
}
```

- [ ] **Step 4: Run to verify they fail**

Run: `go test ./pkg/provider/vcs/github/ -run 'ParseEnrichedPR|EnrichPR' -v`
Expected: FAIL (undefined `parseEnrichedPR` / `EnrichPR`).

- [ ] **Step 5: Implement `enrichedPRFromNode`, `parseEnrichedPR`, `EnrichPR`**

Refactor the per-node conversion out of `parseEnrichedPRs` into a helper and reuse it, then add the single-PR parse + method:

```go
// enrichedPRFromNode converts one parsed PullRequest node into an EnrichedPR,
// shared by the bulk search parser and the single-PR parser so both produce
// identical ThreadID/CreatedAt mapping.
func enrichedPRFromNode(n ghPRNode, repo string) vcs.EnrichedPR {
	ep := vcs.EnrichedPR{PR: prFromGHNode(n, repo)}
	ep.Reviews = reviewsFromGHNode(n)
	ep.Comments = commentsFromGHNode(n)
	ep.CIRuns = ciRunsFromGHNode(n)
	for _, f := range n.Files.Nodes {
		ep.Files = append(ep.Files, f.Path)
	}
	for _, c := range n.Commits.Nodes {
		ep.Commits = append(ep.Commits, c.Commit.Message)
	}
	ep.Truncated = truncationFlags(n)
	return ep
}

// ghSinglePRResponse is the envelope for the by-number query.
type ghSinglePRResponse struct {
	Data struct {
		Repository struct {
			PullRequest *ghPRNode `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []ghGraphQLError `json:"errors"`
}

// parseEnrichedPR parses the single-PR by-number GraphQL response.
func parseEnrichedPR(raw []byte, repo string) (*vcs.EnrichedPR, error) {
	var resp ghSinglePRResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("github: parse single-PR GraphQL response for %s: %w", repo, err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("github: GraphQL error for %s: %s", repo, resp.Errors[0].Message)
	}
	if resp.Data.Repository.PullRequest == nil {
		return nil, fmt.Errorf("github: PR not found in %s", repo)
	}
	ep := enrichedPRFromNode(*resp.Data.Repository.PullRequest, repo)
	return &ep, nil
}

// EnrichPR fetches one PR's enrichment in a single GraphQL round-trip, using
// the same field selection + parsers as the bulk path so review-thread node
// ids (PRRT_) and comment createdAt match exactly.
func (p *Provider) EnrichPR(ctx context.Context, repo string, number int) (*vcs.EnrichedPR, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("github: invalid repo %q (want owner/name)", repo)
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	raw, err := p.gh.RunStdin(ctx, []byte(enrichedPRByNumberQuery),
		"api", "graphql",
		"-F", "owner="+owner,
		"-F", "name="+name,
		"-F", fmt.Sprintf("number=%d", number),
		"-F", "query=@-",
	)
	if err != nil {
		return nil, fmt.Errorf("github: gh api graphql (pr %s#%d): %w", repo, number, err)
	}
	return parseEnrichedPR(raw, repo)
}
```

Then update `parseEnrichedPRs` to use the helper: replace its per-node body with `out = append(out, enrichedPRFromNode(n, repo))` (keeping the `n.Number == 0` skip guard).

- [ ] **Step 6: Run the github package tests**

Run: `go test ./pkg/provider/vcs/github/ -v`
Expected: PASS (new + existing).

- [ ] **Step 7: Commit**

```bash
git add pkg/provider/vcs/github/enrich.go pkg/provider/vcs/github/enrich_test.go
git commit -m "feat(pg-pr): single-PR GraphQL enrichment EnrichPR (PRRT thread ids + createdAt) (pg2-re7e)"
```

---

## Task 3: route `enrichOnePR` through `SinglePREnricher`

**Files:**

- Modify: `internal/sync/sync.go` (add `SinglePREnricher` near the other capability interfaces; rewrite `enrichOnePR` ~line 673)
- Test: `internal/sync/refresh_test.go`

- [ ] **Step 1: Add the capability interface**

In `sync.go`, beside `CommentReader`/`ReviewLister`, add:

```go
// SinglePREnricher is an optional capability: fetch one PR's enrichment
// (reviews, comments incl. real review-thread node ids, CI) in a single
// GraphQL round-trip. enrichOnePR prefers it over per-PR REST so `sync --pr`
// keys threads the same way as the bulk daemon path (no divergent duplicates).
type SinglePREnricher interface {
	EnrichPR(ctx context.Context, repo string, number int) (*vcs.EnrichedPR, error)
}
```

- [ ] **Step 2: Write the failing routing test**

Add to `refresh_test.go` (embed the existing `fakeVCS`; confirm `fakeVCS` already implements `CommentReader`/`ReviewLister` for the fallback branch — if not, the fallback assertions in the existing fake stay as-is):

```go
type enricherVCS struct {
	fakeVCS
	ep        *vcs.EnrichedPR
	enrichErr error
	called    bool
}

func (e *enricherVCS) EnrichPR(_ context.Context, _ string, _ int) (*vcs.EnrichedPR, error) {
	e.called = true
	return e.ep, e.enrichErr
}

func TestEnrichOnePR_PrefersGraphQL(t *testing.T) {
	vp := &enricherVCS{ep: &vcs.EnrichedPR{
		Comments: []api.Comment{{ID: "PRRC_1", ThreadID: "PRRT_abc", Path: "x.go", CreatedAt: "2026-06-03T09:00:00Z"}},
	}}
	e, err := New(Deps{
		Config: func() config.Config { return config.Config{Repos: []config.RepoConfig{{Remote: "o/r", VCS: "github"}}} },
		VCS:    map[string]VCSProvider{"github": vp},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := e.enrichOnePR(context.Background(), config.RepoConfig{Remote: "o/r", VCS: "github"}, api.PR{Repo: "o/r", Number: 42})
	if !vp.called {
		t.Fatal("expected EnrichPR to be used")
	}
	if len(got.Comments) != 1 || got.Comments[0].ThreadID != "PRRT_abc" {
		t.Errorf("expected GraphQL comments with PRRT thread id, got %+v", got.Comments)
	}
}

func TestEnrichOnePR_FallsBackOnError(t *testing.T) {
	vp := &enricherVCS{enrichErr: errors.New("graphql boom")}
	e, err := New(Deps{
		Config: func() config.Config { return config.Config{Repos: []config.RepoConfig{{Remote: "o/r", VCS: "github"}}} },
		VCS:    map[string]VCSProvider{"github": vp},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Must not panic and must return a non-nil EnrichedPR via the REST path.
	got := e.enrichOnePR(context.Background(), config.RepoConfig{Remote: "o/r", VCS: "github"}, api.PR{Repo: "o/r", Number: 42})
	if got == nil || got.PR.Number != 42 {
		t.Fatalf("REST fallback should return the PR, got %+v", got)
	}
}
```

(Adjust the `New(Deps{...})` field names — `Config`/`Repos` — to match the exact existing harness in `maintenance_test.go`/`daemon_test.go`.)

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/sync/ -run TestEnrichOnePR -v`
Expected: FAIL (EnrichPR not yet routed; `vp.called` false).

- [ ] **Step 4: Rewrite `enrichOnePR`**

```go
func (e *Engine) enrichOnePR(ctx context.Context, rcfg config.RepoConfig, pr api.PR) *vcs.EnrichedPR {
	if pr.Repo == "" {
		pr.Repo = rcfg.Remote
	}
	// Prefer single-PR GraphQL enrichment so per-PR sync produces the SAME
	// real review-thread node ids (PRRT_) + createdAt as the bulk daemon path,
	// avoiding divergent/duplicate feedback rows. Fall back to per-PR REST only
	// on a hard error — a truncated-but-correctly-keyed GraphQL result is still
	// preferred over REST.
	if vp, err := e.providerFor(rcfg); err == nil {
		if spe, ok := vp.(SinglePREnricher); ok {
			ep, eerr := spe.EnrichPR(ctx, pr.Repo, pr.Number)
			if eerr == nil && ep != nil {
				if len(ep.Truncated) > 0 {
					e.logWarn(ctx, "single-PR GraphQL enrichment truncated", "repo", pr.Repo, "number", pr.Number, "truncated", strings.Join(ep.Truncated, ","))
				}
				ep.PR = pr // keep the observed PR state
				return ep
			}
			if eerr != nil {
				e.logWarn(ctx, "single-PR GraphQL enrichment failed; falling back to REST", "repo", pr.Repo, "number", pr.Number, "err", eerr.Error())
			}
		}
	}

	out := vcs.EnrichedPR{PR: pr}
	if vp, err := e.providerFor(rcfg); err == nil {
		if rl, ok := vp.(ReviewLister); ok {
			if reviews, rerr := rl.ListReviews(ctx, pr.Repo, pr.Number); rerr == nil {
				out.Reviews = reviews
			}
		}
		if reader, ok := vp.(CommentReader); ok {
			if comments, cerr := reader.ListComments(ctx, pr.Repo, pr.Number); cerr == nil {
				out.Comments = comments
			}
		}
	}
	if cp := e.firstCICDFor(rcfg); cp != nil {
		if bl, ok := cp.(CICDBranchLister); ok && strings.TrimSpace(pr.Branch) != "" {
			if runs, cerr := bl.ListRunsByBranch(ctx, pr.Repo, pr.Branch); cerr == nil {
				out.CIRuns = runs
			}
		} else if runs, cerr := cp.ListRuns(ctx, pr.Repo, pr.Number); cerr == nil {
			out.CIRuns = runs
		}
	}
	return &out
}
```

Replace `e.logWarn(...)` with the engine's existing warn-logging idiom (match how `"refresh failed"` is logged in `sync.go`; e.g. `slog.WarnContext(ctx, ...)` or the engine's logger). Do not invent a new logging mechanism.

- [ ] **Step 5: Run the routing tests**

Run: `go test ./internal/sync/ -run TestEnrichOnePR -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/sync/sync.go internal/sync/refresh_test.go
git commit -m "feat(pg-pr): route enrichOnePR through single-PR GraphQL, REST as hard-error fallback (pg2-re7e)"
```

---

## Task 4: full verification

**Files:** none (verification only)

- [ ] **Step 1: Full package test suite**

Run: `go test ./...` (in `packages/pg-pr`)
Expected: all packages `ok`; the bulk-path tests (`internal/sync`, `pkg/provider/vcs/github`, `internal/store`) unchanged and green.

- [ ] **Step 2: Vet**

Run: `go vet ./...`
Expected: no findings.

- [ ] **Step 3: nix build (the deployable package)**

Run (from repo root): `nix build .#pg-pr 2>&1 | tail -5 && ./result/bin/pg-pr --version`
Expected: builds; version digest differs from `254da4db` (source changed).

- [ ] **Step 4: Commit any incidental fixes; confirm clean tree**

```bash
git status --short   # expect empty
```

---

## Self-Review notes

- **Spec coverage:** Task 1 ⇒ REST `CreatedAt`; Task 2 ⇒ single-PR GraphQL fetch + identical parsers + `first:100`/truncation-flag; Task 3 ⇒ `SinglePREnricher` routing + REST hard-error fallback + WARN; Task 4 ⇒ green `go test ./...`, no bulk-path change. Existing-data cleanup is out of scope (deferred), per spec.
- **Type consistency:** `EnrichPR(ctx, repo string, number int) (*vcs.EnrichedPR, error)` used identically in the provider, the `SinglePREnricher` interface, and the routing call. `parseEnrichedPR` returns `*vcs.EnrichedPR`. `enrichedPRFromNode` returns `vcs.EnrichedPR` (value), appended/dereferenced consistently.
- **Harness specifics to confirm at execution:** exact `Deps` field names (`Config` func vs `Cfg`), whether `fakeVCS` already satisfies `CommentReader`/`ReviewLister`, and the engine's WARN-logging idiom. These are mechanical lookups, not design gaps.
