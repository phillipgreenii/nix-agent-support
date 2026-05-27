# pg-pr Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `/api/v1/dashboard` JSON snapshot endpoint to the `pg-pr` daemon and provision a Grafana dashboard rendered via the Infinity datasource plugin.

**Architecture:** A new in-memory `snapshot.Store` is populated at the end of each `Engine.Sync` tick. The existing daemon HTTP listener (currently `/metrics` only) mounts an additional handler that JSON-serializes the snapshot. A new agent-registry config block classifies reviewers / comment authors as humans or agents, and feeds the `human_approved` / `agent_approved` derived booleans. The `waiting_on_me` boolean comes from a recursive `bd dep tree --direction=up` walk from each PR's merge-request bead. Grafana provisioning (dashboard JSON, datasource, Infinity plugin install) lives in the `otel-stack-tools` Nix module.

**Tech Stack:** Go 1.22+, `bd` (beads CLI), `gh` (GitHub CLI), Grafana 11+, Infinity datasource plugin, Nix (nix-darwin + home-manager), Prometheus `client_golang`, OpenTelemetry Go SDK.

---

## File Structure

**Go packages — new:**

- `phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot/` — snapshot types, RWMutex-guarded Store, JSON marshaling.
- `phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot/builder.go` — assembles a `Snapshot` from sync-loop outputs.
- `phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry/` — loads agent registry config, classifies logins and bodies.
- `phillipgreenii-nix-agent-support/packages/pg-pr/internal/httpapi/` — `/api/v1/dashboard` handler.

**Go packages — extended:**

- `pg-pr/pkg/api/` — `PR` gains `Title`, `Additions`, `Deletions`, `ChangedFiles`.
- `pg-pr/pkg/provider/vcs/iface.go` — adds `ListReviews` to the `Provider` interface.
- `pg-pr/pkg/provider/vcs/github/` — implements the new methods.
- `pg-pr/internal/config/` — adds `agents:` block.
- `pg-pr/internal/sync/sync.go` — Engine wires snapshot builder + writes Store at end of each tick.
- `pg-pr/internal/sync/daemon.go` — mounts `/api/v1/dashboard` next to `/metrics`.
- `pg-pr/internal/telemetry/metrics.go` — adds `pg_pr_snapshot_present` gauge.

**Nix module:**

- `phillipgreenii-nix-support-apps/packages/otel-stack-tools/grafana/dashboards/pg-pr.json` — new dashboard.
- `phillipgreenii-nix-support-apps/darwin/modules/observability/grafana.nix` (existing) — append Infinity plugin + datasource provisioning.

**Docs:**

- `phillipgreenii-nix-agent-support/packages/pg-pr/pg-pr.md` — append daemon endpoint reference.

---

## Working Directory

All Go-side tasks are run from `phillipgreenii-nix-agent-support/packages/pg-pr/`. All Nix-side tasks from `phillipgreenii-nix-support-apps/`. Both repos are separate git repos under `/Users/phillipg/phillipg_mbp/`.

Use `superpowers:using-git-worktrees` to isolate this work.

---

## Task 1: Extend `api.PR` with diff stats and title

**Files:**

- Modify: `pg-pr/pkg/api/pr.go`
- Modify: `pg-pr/pkg/api/pr_test.go` (create if missing)

- [ ] **Step 1: Write failing test for new fields**

Append to `pg-pr/pkg/api/pr_test.go`:

```go
package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPRJSONIncludesDiffStatsAndTitle(t *testing.T) {
	pr := PR{
		Repo: "owner/repo", Number: 1, State: "open", Title: "Fix bar",
		Additions: 10, Deletions: 3, ChangedFiles: 2,
	}
	b, err := json.Marshal(pr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"title":"Fix bar"`, `"additions":10`, `"deletions":3`, `"changed_files":2`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}
```

- [ ] **Step 2: Run and confirm failure**

```
cd packages/pg-pr && go test ./pkg/api/...
```

Expected: FAIL — `Title`, `Additions`, `Deletions`, `ChangedFiles` undefined.

- [ ] **Step 3: Add fields to `PR`**

In `pg-pr/pkg/api/pr.go`:

```go
type PR struct {
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	State        string `json:"state"`
	Branch       string `json:"branch"`
	Base         string `json:"base"`
	Author       string `json:"author"`
	URL          string `json:"url"`
	Draft        bool   `json:"draft"`
	Merged       bool   `json:"merged"`
	Additions    int    `json:"additions,omitempty"`
	Deletions    int    `json:"deletions,omitempty"`
	ChangedFiles int    `json:"changed_files,omitempty"`
}
```

- [ ] **Step 4: Run test**

```
go test ./pkg/api/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/api/pr.go pkg/api/pr_test.go
git commit -m "feat(pg-pr): add title + diff-stat fields to api.PR"
```

---

## Task 2: Add `ListReviews` to VCS provider interface

**Files:**

- Modify: `pg-pr/pkg/provider/vcs/iface.go`
- Modify: `pg-pr/pkg/api/pr.go` (Review struct already exists — extend if needed)

- [ ] **Step 1: Inspect existing `api.Review`**

Current shape (in `pkg/api/pr.go`):

```go
type Review struct {
	ID       string    `json:"id"`
	Author   string    `json:"author"`
	State    string    `json:"state"`
	Body     string    `json:"body"`
	Comments []Comment `json:"comments,omitempty"`
}
```

`State`, `Author`, `Body` are already present — sufficient for downstream classification.

- [ ] **Step 2: Add `ListReviews` to the interface**

In `pg-pr/pkg/provider/vcs/iface.go`, append to the `Provider` interface:

```go
// ListReviews returns the review summaries for a PR. State is one of
// APPROVED, CHANGES_REQUESTED, COMMENTED. Body is the review-summary text
// (used for agent approval-mining); Comments is left empty here — inline
// comments are fetched via ListComments separately.
ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error)
```

- [ ] **Step 3: Confirm GH provider compile-fails**

```
go build ./...
```

Expected: FAIL on `pkg/provider/vcs/github/*` missing method.

- [ ] **Step 4: Implement on GH provider**

In `pg-pr/pkg/provider/vcs/github/`, locate the impl file and add a method that shells out to `gh`:

```go
func (p *Provider) ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error) {
	out, err := p.runGH(ctx, "pr", "view", strconv.Itoa(number), "--repo", repo,
		"--json", "reviews",
		"-q", `.reviews[] | {id: (.id|tostring), author: .author.login, state: .state, body: .body}`)
	if err != nil {
		return nil, fmt.Errorf("gh pr view --json reviews: %w", err)
	}
	var reviews []api.Review
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var r api.Review
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("decode review: %w", err)
		}
		reviews = append(reviews, r)
	}
	return reviews, nil
}
```

(Locate the existing `runGH` helper; the snippet above is shape-only. If `gh pr view --json reviews -q` outputs a JSON array, swap the per-object decoder for a single `json.Unmarshal` into `[]api.Review`.)

- [ ] **Step 5: Add unit test stubbing the `runGH` exec**

Add a test that injects a fake runner returning a canned JSON payload and asserts the parsed result. Mirror the pattern used by the existing `vcs/github` tests.

```go
func TestListReviews(t *testing.T) {
	fake := newFakeRunner(t, map[string]string{
		"pr view 7 --repo owner/repo --json reviews": `[{"id":"1","author":"alice","state":"APPROVED","body":""},{"id":"2","author":"claude[bot]","state":"COMMENTED","body":"Verdict: approve"}]`,
	})
	p := NewProvider(fake)
	got, err := p.ListReviews(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("ListReviews: %v", err)
	}
	if len(got) != 2 || got[1].Author != "claude[bot]" {
		t.Fatalf("got %+v", got)
	}
}
```

- [ ] **Step 6: Run tests**

```
go test ./pkg/provider/vcs/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```
git add pkg/provider/vcs/
git commit -m "feat(pg-pr): add VCS Provider.ListReviews"
```

---

## Task 3: Populate title + diff stats in GH `GetPR` (and enumerate paths)

**Files:**

- Modify: `pg-pr/pkg/provider/vcs/github/<impl>.go`
- Modify: corresponding `_test.go`

- [ ] **Step 1: Failing test**

```go
func TestGetPRPopulatesTitleAndDiffStats(t *testing.T) {
	fake := newFakeRunner(t, map[string]string{
		"pr view 7 --repo owner/repo --json number,state,title,body,headRefName,baseRefName,author,url,isDraft,merged,additions,deletions,changedFiles":
			`{"number":7,"state":"OPEN","title":"Fix bar","headRefName":"f","baseRefName":"main","author":{"login":"me"},"url":"u","isDraft":false,"merged":false,"additions":10,"deletions":3,"changedFiles":2}`,
	})
	p := NewProvider(fake)
	got, err := p.GetPR(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got.Title != "Fix bar" || got.Additions != 10 || got.Deletions != 3 || got.ChangedFiles != 2 {
		t.Fatalf("got %+v", got)
	}
}
```

- [ ] **Step 2: Run, confirm fail**

```
go test ./pkg/provider/vcs/github/...
```

- [ ] **Step 3: Extend the GH `--json` field list in `GetPR` and the listing paths**

Find the existing `GetPR` implementation. Append `title,additions,deletions,changedFiles` to its `--json` field list and to the response decoder. Do the same in `ListMyPRs` and `ListTeamPRs` so titles are present on enumerated PRs without an extra fetch.

- [ ] **Step 4: Run, confirm pass**

```
go test ./pkg/provider/vcs/github/...
```

- [ ] **Step 5: Commit**

```
git add pkg/provider/vcs/github/
git commit -m "feat(pg-pr): populate PR title + diff stats in GH provider"
```

---

## Task 4: Agent registry config + classifier

**Files:**

- Create: `pg-pr/internal/agentregistry/registry.go`
- Create: `pg-pr/internal/agentregistry/registry_test.go`
- Modify: `pg-pr/internal/config/config.go` (add `Agents []AgentConfig`)
- Modify: `pg-pr/internal/config/config_test.go`

- [ ] **Step 1: Failing test for registry**

`pg-pr/internal/agentregistry/registry_test.go`:

```go
package agentregistry

import "testing"

func TestIsAgent(t *testing.T) {
	r, err := New([]Entry{{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !r.IsAgent("claude[bot]") {
		t.Error("expected claude[bot] to be classified as agent")
	}
	if r.IsAgent("alice") {
		t.Error("expected alice to not be agent")
	}
}

func TestMatchApproval(t *testing.T) {
	r, _ := New([]Entry{{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`}})
	if !r.MatchApproval("claude[bot]", "Verdict: Approve\nLGTM") {
		t.Error("expected approval match")
	}
	if r.MatchApproval("claude[bot]", "Verdict: request-changes") {
		t.Error("expected no match for non-approve body")
	}
	if r.MatchApproval("alice", "Verdict: Approve") {
		t.Error("expected no match for non-agent author")
	}
}

func TestInvalidRegex(t *testing.T) {
	if _, err := New([]Entry{{Login: "x", ApprovalRegex: "[unclosed"}}); err == nil {
		t.Fatal("expected error on invalid regex")
	}
}
```

- [ ] **Step 2: Run, confirm fail**

```
go test ./internal/agentregistry/...
```

- [ ] **Step 3: Implement registry**

`pg-pr/internal/agentregistry/registry.go`:

```go
// Package agentregistry classifies PR participants as agents vs humans,
// and matches agent-authored comment bodies against per-agent approval
// regexes. Config is loaded from the pg-pr config file.
package agentregistry

import (
	"fmt"
	"regexp"
)

// Entry describes one known agent account.
type Entry struct {
	Login         string `yaml:"login" json:"login"`
	ApprovalRegex string `yaml:"approval_regex" json:"approval_regex"`
}

// Registry classifies logins and bodies. Safe for concurrent reads.
type Registry struct {
	byLogin map[string]*regexp.Regexp
}

// New compiles entries; returns an error on the first invalid regex.
func New(entries []Entry) (*Registry, error) {
	r := &Registry{byLogin: make(map[string]*regexp.Regexp, len(entries))}
	for _, e := range entries {
		re, err := regexp.Compile(e.ApprovalRegex)
		if err != nil {
			return nil, fmt.Errorf("agent %q: compile approval_regex: %w", e.Login, err)
		}
		r.byLogin[e.Login] = re
	}
	return r, nil
}

// IsAgent reports whether login is a registered agent account.
func (r *Registry) IsAgent(login string) bool {
	_, ok := r.byLogin[login]
	return ok
}

// MatchApproval reports whether `body` constitutes an approval verdict
// authored by `login`. Returns false when login is not a registered agent.
func (r *Registry) MatchApproval(login, body string) bool {
	re, ok := r.byLogin[login]
	if !ok {
		return false
	}
	return re.MatchString(body)
}
```

- [ ] **Step 4: Run, confirm pass**

```
go test ./internal/agentregistry/...
```

- [ ] **Step 5: Wire into config**

In `pg-pr/internal/config/config.go`, add to the top-level config struct:

```go
type Config struct {
    // ... existing fields ...
    Agents []agentregistry.Entry `yaml:"agents,omitempty" json:"agents,omitempty"`
}
```

Add an import. Add a test asserting that `Load` parses an `agents:` block from YAML.

- [ ] **Step 6: Run config tests**

```
go test ./internal/config/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```
git add internal/agentregistry/ internal/config/
git commit -m "feat(pg-pr): agent registry config + classifier"
```

---

## Task 5: Snapshot types + Store

**Files:**

- Create: `pg-pr/internal/snapshot/snapshot.go`
- Create: `pg-pr/internal/snapshot/store.go`
- Create: `pg-pr/internal/snapshot/store_test.go`

- [ ] **Step 1: Failing tests**

`pg-pr/internal/snapshot/store_test.go`:

```go
package snapshot

import (
	"testing"
	"time"
)

func TestStoreEmpty(t *testing.T) {
	s := NewStore()
	got, ok := s.Get()
	if ok {
		t.Errorf("expected !ok on empty store, got snap=%+v", got)
	}
}

func TestStoreSetGet(t *testing.T) {
	s := NewStore()
	want := &Snapshot{GeneratedAt: time.Unix(1700000000, 0).UTC()}
	s.Set(want)
	got, ok := s.Get()
	if !ok {
		t.Fatal("expected ok=true after Set")
	}
	if !got.GeneratedAt.Equal(want.GeneratedAt) {
		t.Errorf("got %v want %v", got.GeneratedAt, want.GeneratedAt)
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := NewStore()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			s.Set(&Snapshot{GeneratedAt: time.Now()})
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_, _ = s.Get()
	}
	<-done
}
```

- [ ] **Step 2: Implement `Snapshot` types**

`pg-pr/internal/snapshot/snapshot.go`:

```go
// Package snapshot defines the JSON-serializable per-PR dashboard
// snapshot served by the pg-pr daemon's /api/v1/dashboard endpoint.
package snapshot

import "time"

// Snapshot is the top-level dashboard payload.
type Snapshot struct {
	GeneratedAt         time.Time `json:"generated_at"`
	SyncIntervalSeconds int       `json:"sync_interval_seconds"`
	Mine                []MineRow `json:"mine"`
	Team                []TeamRow `json:"team"`
}

// MineRow is one row in the "My PRs" table.
type MineRow struct {
	Repo           string     `json:"repo"`
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	Draft          bool       `json:"draft"`
	CIStatus       string     `json:"ci_status"`
	HumanApproved  bool       `json:"human_approved"`
	AgentApproved  bool       `json:"agent_approved"`
	WaitingOnMe    bool       `json:"waiting_on_me"`
	JIRA           []JIRAItem `json:"jira"`
	Beads          []BeadItem `json:"beads"`
}

// TeamRow is one row in the "Team PRs" table.
type TeamRow struct {
	Repo          string     `json:"repo"`
	Number        int        `json:"number"`
	Title         string     `json:"title"`
	Owner         string     `json:"owner"`
	URL           string     `json:"url"`
	CIStatus      string     `json:"ci_status"`
	HumanApproved bool       `json:"human_approved"`
	AgentApproved bool       `json:"agent_approved"`
	LinesChanged  int        `json:"lines_changed"`
	FilesChanged  int        `json:"files_changed"`
	JIRA          []JIRAItem `json:"jira"`
}

// JIRAItem is one resolved JIRA issue referenced by a PR.
type JIRAItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`
}

// BeadItem is one bead from the recursive dep tree of a merge-request bead.
type BeadItem struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
	URL    string   `json:"url"`
}
```

`pg-pr/internal/snapshot/store.go`:

```go
package snapshot

import "sync"

// Store holds the latest Snapshot for the dashboard handler.
// Safe for concurrent access.
type Store struct {
	mu   sync.RWMutex
	cur  *Snapshot
}

// NewStore constructs an empty Store. Get returns (nil, false) until Set
// is called.
func NewStore() *Store { return &Store{} }

// Set replaces the held snapshot atomically.
func (s *Store) Set(snap *Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = snap
}

// Get returns the held snapshot, or (nil, false) when none has been set.
func (s *Store) Get() (*Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cur == nil {
		return nil, false
	}
	return s.cur, true
}
```

- [ ] **Step 3: Run tests**

```
go test ./internal/snapshot/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add internal/snapshot/
git commit -m "feat(pg-pr): snapshot types and concurrent Store"
```

---

## Task 6: `/api/v1/dashboard` HTTP handler

**Files:**

- Create: `pg-pr/internal/httpapi/dashboard.go`
- Create: `pg-pr/internal/httpapi/dashboard_test.go`

- [ ] **Step 1: Failing test**

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

func TestDashboard503WhenEmpty(t *testing.T) {
	s := snapshot.NewStore()
	srv := httptest.NewServer(DashboardHandler(s))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", resp.StatusCode)
	}
}

func TestDashboard200WhenPopulated(t *testing.T) {
	s := snapshot.NewStore()
	s.Set(&snapshot.Snapshot{
		GeneratedAt:         time.Unix(1700000000, 0).UTC(),
		SyncIntervalSeconds: 60,
		Mine:                []snapshot.MineRow{},
		Team:                []snapshot.TeamRow{},
	})
	srv := httptest.NewServer(DashboardHandler(s))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q want application/json", ct)
	}
	var got snapshot.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SyncIntervalSeconds != 60 {
		t.Errorf("got interval %d want 60", got.SyncIntervalSeconds)
	}
}
```

- [ ] **Step 2: Implement handler**

`pg-pr/internal/httpapi/dashboard.go`:

```go
// Package httpapi exposes pg-pr daemon HTTP handlers beyond /metrics.
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

// DashboardHandler serves the current snapshot as JSON.
// Returns 503 until the daemon has populated a first snapshot.
func DashboardHandler(store *snapshot.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap, ok := store.Get()
		if !ok {
			http.Error(w, `{"error":"snapshot not yet populated"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})
}
```

- [ ] **Step 3: Run tests**

```
go test ./internal/httpapi/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add internal/httpapi/
git commit -m "feat(pg-pr): /api/v1/dashboard handler"
```

---

## Task 7: Mount `/api/v1/dashboard` on the daemon listener

**Files:**

- Modify: `pg-pr/internal/sync/daemon.go`
- Modify: `pg-pr/internal/sync/daemon_test.go`

- [ ] **Step 1: Threading the Store through DaemonOpts**

Add to `DaemonOpts`:

```go
// Dashboard, when non-nil, mounts /api/v1/dashboard on the same listener
// as /metrics, serving snapshots from this Store. Nil disables the
// endpoint (back-compat for callers that don't enable the dashboard).
Dashboard *snapshot.Store
```

Import the snapshot package.

- [ ] **Step 2: Update `startMetricsServer` mux wiring**

Inside `startMetricsServer`:

```go
mux := http.NewServeMux()
mux.Handle("/metrics", telemetry.MetricsHandler())
if opts.Dashboard != nil {
    mux.Handle("/api/v1/dashboard", httpapi.DashboardHandler(opts.Dashboard))
}
```

Add the httpapi import.

- [ ] **Step 3: Daemon test exercising both endpoints**

Add to `daemon_test.go` (or new file) — boot Daemon with `MetricsAddr="127.0.0.1:0"` and `Dashboard=store`, use `MetricsListener` to capture the bound port, then HTTP-GET both `/metrics` and `/api/v1/dashboard`:

```go
func TestDaemonMountsDashboard(t *testing.T) {
	t.Parallel()
	store := snapshot.NewStore()
	store.Set(&snapshot.Snapshot{GeneratedAt: time.Now().UTC(), Mine: []snapshot.MineRow{}, Team: []snapshot.TeamRow{}})

	var bound string
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = (&Engine{}).Daemon(ctx, DaemonOpts{
			Interval:        time.Hour, // never tick
			LockDir:         t.TempDir(),
			Sighup:          make(chan os.Signal),
			MetricsAddr:     "127.0.0.1:0",
			MetricsListener: func(ln net.Listener) { bound = ln.Addr().String() },
			Dashboard:       store,
		})
	}()
	// poll for listener
	deadline := time.Now().Add(2 * time.Second)
	for bound == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if bound == "" {
		t.Fatal("listener never bound")
	}
	resp, err := http.Get("http://" + bound + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET dashboard: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("got %d", resp.StatusCode)
	}
	cancel()
}
```

Note: the construction `(&Engine{}).Daemon(...)` will not run iterations because Engine.Sync requires deps — the test uses a long Interval to avoid the first tick. If the test framework requires a real Engine, mirror the existing `daemon_test.go` test helpers; do not invent a new pattern.

- [ ] **Step 4: Run tests**

```
go test ./internal/sync/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/sync/
git commit -m "feat(pg-pr): mount /api/v1/dashboard on daemon listener"
```

---

## Task 8: Recursive bd dep walk for a merge-request bead

**Files:**

- Create: `pg-pr/pkg/beads/deptree.go`
- Create: `pg-pr/pkg/beads/deptree_test.go`

- [ ] **Step 1: Failing test**

`pg-pr/pkg/beads/deptree_test.go`:

```go
package beads

import (
	"context"
	"testing"
)

// Test setup: create merge-request bead M, three children A, B, C.
// Add label "human" to B and C. Close C.
// Expect TreeUp(M) to return [A, B, C] with labels and status populated.
// Expect AllNonClosedHumanLabeled(deps_excluding_M) to return true:
//   non-closed = {A, B}; A has no human label → false.
// After adding "human" to A, expect true.
func TestDepTreeUpWalk(t *testing.T) {
	c, _ := newBDWorkspace(t) // existing helper from mergerequest_test.go
	ctx := context.Background()

	m, _, err := c.EnsureMergeRequest(ctx, "MR", MergeRequestFields{Repo: "x/y", PRNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Create three child beads with dep on M.
	a := createChildBead(t, c, m, "A")
	b := createChildBead(t, c, m, "B")
	_ = createChildBead(t, c, m, "C") // closed below
	addLabel(t, c, b, "human")
	// close C via bd CLI
	closeBead(t, c, _ /*c id*/)

	got, err := c.DepTreeUp(ctx, m)
	if err != nil {
		t.Fatalf("DepTreeUp: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(deps): got %d want 3", len(got))
	}
	// Walk should be set-wise; order not specified.
	// ...assertions on each ID, labels, status...
}
```

Helpers `createChildBead`, `addLabel`, `closeBead` to be implemented alongside the existing `newBDWorkspace` test helper. Use direct `bd` CLI calls through `Runner`.

- [ ] **Step 2: Implement `DepTreeUp`**

`pg-pr/pkg/beads/deptree.go`:

```go
package beads

import (
	"context"
	"encoding/json"
	"fmt"
)

// DepNode is one bead in a recursive dependency walk.
type DepNode struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
}

// DepTreeUp returns all beads that recursively depend on rootID
// (i.e. beads in the "up" direction from rootID). The root itself is NOT
// included. Walk is breadth-first; results are de-duplicated.
//
// Implementation: shell out to `bd dep tree <rootID> --direction=up --json`.
// If `bd dep tree --json` is not stable, fall back to repeated
// `bd dep list <id> --direction=up --json` walks.
func (c *Client) DepTreeUp(ctx context.Context, rootID string) ([]DepNode, error) {
	out, err := c.Runner.Run(ctx, "dep", "tree", rootID, "--direction=up", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd dep tree --direction=up: %w", err)
	}
	var tree struct {
		Nodes []DepNode `json:"nodes"`
	}
	if err := json.Unmarshal(out, &tree); err != nil {
		// Fallback path: bd dep tree shape may differ. The implementer
		// should run `bd dep tree <real-id> --direction=up --json` once
		// against a live workspace and shape DepNode + parsing to match.
		return nil, fmt.Errorf("decode bd dep tree json: %w (raw=%s)", err, string(out))
	}
	out2 := make([]DepNode, 0, len(tree.Nodes))
	for _, n := range tree.Nodes {
		if n.ID == rootID {
			continue
		}
		out2 = append(out2, n)
	}
	return out2, nil
}

// AllNonClosedHumanLabeled reports whether every non-closed dep carries
// the `human` label. Empty non-closed set → false (PR is not "waiting on
// me" if nothing's pending).
func AllNonClosedHumanLabeled(deps []DepNode) bool {
	any := false
	for _, d := range deps {
		if d.Status == "closed" {
			continue
		}
		any = true
		if !hasLabel(d.Labels, "human") {
			return false
		}
	}
	return any
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
```

**Implementation note:** before coding, run `bd dep tree <some-real-id> --direction=up --json` in a workspace and confirm the actual JSON shape — adjust `tree.Nodes` decoding accordingly. Document the exact shape in a comment.

- [ ] **Step 3: Verify with a real `bd` workspace**

```
bd dep tree <some-merge-request-bead-id> --direction=up --json
```

Confirm the JSON structure matches the `DepNode` decoder. Fix the decoder if needed.

- [ ] **Step 4: Run tests**

```
go test ./pkg/beads/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/beads/deptree.go pkg/beads/deptree_test.go
git commit -m "feat(pg-pr): bd dep tree --direction=up walker"
```

---

## Task 9: Snapshot builder — assemble a row from sync state

**Files:**

- Create: `pg-pr/internal/snapshot/builder.go`
- Create: `pg-pr/internal/snapshot/builder_test.go`

The builder is pure: takes already-fetched data, returns a `Snapshot`. All upstream IO is the caller's responsibility (Engine.Sync). This keeps the builder fully unit-testable.

- [ ] **Step 1: Define the builder input shape**

```go
package snapshot

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// PRInput is the per-PR data the sync loop has gathered.
type PRInput struct {
	PR        api.PR
	Reviews   []api.Review
	Comments  []api.Comment
	CIRuns    []api.CIRun
	JIRA      []api.Issue
	BeadsDeps []beads.DepNode // recursive deps of the merge-request bead
}

// BuilderInput is the full snapshot input.
type BuilderInput struct {
	GeneratedAt         time.Time
	SyncIntervalSeconds int
	Self                string // self-author login
	TeamMembers         []string
	Registry            *agentregistry.Registry
	PRs                 []PRInput
}
```

- [ ] **Step 2: Test for `Build` — happy path**

```go
func TestBuildSplitsMineFromTeam(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		GeneratedAt:         time.Unix(1700000000, 0).UTC(),
		SyncIntervalSeconds: 60,
		Self:                "me",
		TeamMembers:         []string{"alice"},
		Registry:            reg,
		PRs: []PRInput{
			{PR: api.PR{Repo: "r/x", Number: 1, Author: "me",    Title: "mine", URL: "u1", Draft: false}},
			{PR: api.PR{Repo: "r/x", Number: 2, Author: "alice", Title: "teem", URL: "u2", Draft: false, Additions: 5, Deletions: 1, ChangedFiles: 1}},
			{PR: api.PR{Repo: "r/x", Number: 3, Author: "alice", Title: "drft", URL: "u3", Draft: true}},
			{PR: api.PR{Repo: "r/x", Number: 4, Author: "bob",   Title: "outs", URL: "u4"}},
		},
	}
	got := Build(in)
	if len(got.Mine) != 1 || got.Mine[0].Number != 1 {
		t.Fatalf("mine: %+v", got.Mine)
	}
	if len(got.Team) != 1 || got.Team[0].Number != 2 {
		t.Fatalf("team: %+v", got.Team)
	}
	if got.Team[0].LinesChanged != 6 || got.Team[0].FilesChanged != 1 {
		t.Errorf("team diff stats: %+v", got.Team[0])
	}
}
```

- [ ] **Step 3: Test for derived fields**

```go
func TestBuildDerivesApprovalAndWaiting(t *testing.T) {
	reg, _ := agentregistry.New([]agentregistry.Entry{
		{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`},
	})
	in := BuilderInput{
		GeneratedAt: time.Now().UTC(), Self: "me", Registry: reg,
		PRs: []PRInput{
			{
				PR: api.PR{Repo: "r/x", Number: 1, Author: "me"},
				Reviews: []api.Review{
					{Author: "alice", State: "APPROVED"},
					{Author: "claude[bot]", State: "COMMENTED", Body: "Verdict: approve"},
				},
				CIRuns: []api.CIRun{{Conclusion: "success"}, {Conclusion: "success"}},
				BeadsDeps: []beads.DepNode{
					{ID: "b1", Status: "open", Labels: []string{"human"}},
					{ID: "b2", Status: "open", Labels: []string{"human"}},
				},
			},
		},
	}
	got := Build(in)
	row := got.Mine[0]
	if !row.HumanApproved {
		t.Error("expected human_approved=true (alice approved)")
	}
	if !row.AgentApproved {
		t.Error("expected agent_approved=true (claude comment matched regex)")
	}
	if row.CIStatus != "success" {
		t.Errorf("ci_status: got %q want success", row.CIStatus)
	}
	if !row.WaitingOnMe {
		t.Error("expected waiting_on_me=true (all non-closed deps human-labeled)")
	}
}
```

- [ ] **Step 4: Implement `Build`**

```go
// Build assembles a Snapshot from the given input. Pure; no IO.
func Build(in BuilderInput) *Snapshot {
	out := &Snapshot{
		GeneratedAt:         in.GeneratedAt,
		SyncIntervalSeconds: in.SyncIntervalSeconds,
		Mine:                []MineRow{},
		Team:                []TeamRow{},
	}
	teamSet := make(map[string]struct{}, len(in.TeamMembers))
	for _, m := range in.TeamMembers {
		teamSet[m] = struct{}{}
	}
	for _, p := range in.PRs {
		switch {
		case p.PR.Author == in.Self:
			out.Mine = append(out.Mine, buildMineRow(p, in.Registry))
		case isTeam(p.PR.Author, teamSet) && !p.PR.Draft:
			out.Team = append(out.Team, buildTeamRow(p, in.Registry))
		}
	}
	return out
}

func buildMineRow(p PRInput, reg *agentregistry.Registry) MineRow {
	hum, agt := classifyApprovals(p, reg)
	return MineRow{
		Repo:          p.PR.Repo,
		Number:        p.PR.Number,
		Title:         p.PR.Title,
		URL:           p.PR.URL,
		Draft:         p.PR.Draft,
		CIStatus:      rollupCI(p.CIRuns),
		HumanApproved: hum,
		AgentApproved: agt,
		WaitingOnMe:   beadsAllHuman(p.BeadsDeps),
		JIRA:          mapJIRA(p.JIRA),
		Beads:         mapBeads(p.BeadsDeps),
	}
}

func buildTeamRow(p PRInput, reg *agentregistry.Registry) TeamRow {
	hum, agt := classifyApprovals(p, reg)
	return TeamRow{
		Repo:          p.PR.Repo,
		Number:        p.PR.Number,
		Title:         p.PR.Title,
		Owner:         p.PR.Author,
		URL:           p.PR.URL,
		CIStatus:      rollupCI(p.CIRuns),
		HumanApproved: hum,
		AgentApproved: agt,
		LinesChanged:  p.PR.Additions + p.PR.Deletions,
		FilesChanged:  p.PR.ChangedFiles,
		JIRA:          mapJIRA(p.JIRA),
	}
}

func isTeam(author string, team map[string]struct{}) bool {
	_, ok := team[author]
	return ok
}

func classifyApprovals(p PRInput, reg *agentregistry.Registry) (human bool, agent bool) {
	for _, r := range p.Reviews {
		if r.State != "APPROVED" {
			continue
		}
		if reg.IsAgent(r.Author) {
			agent = true
		} else {
			human = true
		}
	}
	if !agent {
		// Comment-mining: only top-level / review-summary bodies. The
		// sync loop is responsible for filtering ListComments output to
		// non-inline comments (Path/Line empty).
		for _, c := range p.Comments {
			if c.Path != "" || c.Line != 0 {
				continue
			}
			if reg.MatchApproval(c.Author, c.Body) {
				agent = true
				break
			}
		}
		// Also scan review-summary bodies authored by agents.
		for _, r := range p.Reviews {
			if reg.MatchApproval(r.Author, r.Body) {
				agent = true
				break
			}
		}
	}
	return
}

func rollupCI(runs []api.CIRun) string {
	if len(runs) == 0 {
		return "none"
	}
	any := false
	for _, r := range runs {
		switch r.Conclusion {
		case "failure", "cancelled", "timed_out":
			return "failure"
		}
		if r.Status == "in_progress" || r.Status == "queued" || r.Conclusion == "" {
			any = true
		}
	}
	if any {
		return "pending"
	}
	return "success"
}

func beadsAllHuman(deps []beads.DepNode) bool {
	return beads.AllNonClosedHumanLabeled(deps)
}

func mapJIRA(issues []api.Issue) []JIRAItem {
	out := make([]JIRAItem, 0, len(issues))
	for _, i := range issues {
		out = append(out, JIRAItem{ID: i.ID, Title: i.Title, State: i.State, URL: i.URL})
	}
	return out
}

func mapBeads(deps []beads.DepNode) []BeadItem {
	out := make([]BeadItem, 0, len(deps))
	for _, d := range deps {
		out = append(out, BeadItem{
			ID: d.ID, Title: d.Title, Status: d.Status, Labels: d.Labels,
			URL: "bd://" + d.ID,
		})
	}
	return out
}
```

- [ ] **Step 5: Run tests**

```
go test ./internal/snapshot/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/snapshot/builder.go internal/snapshot/builder_test.go
git commit -m "feat(pg-pr): snapshot builder with derived fields"
```

---

## Task 10: Wire builder into Engine.Sync

**Files:**

- Modify: `pg-pr/internal/sync/sync.go`
- Modify: `pg-pr/internal/sync/sync_test.go`
- Modify: `pg-pr/cmd/pg-pr/sync.go` (CLI plumbing — pass Store into DaemonOpts)

This is the integration task. Each sync iteration must:

1. For each enumerated PR, fetch reviews, comments, CI runs, JIRA issues (from cached jira-link beads), and the recursive bead dep tree.
2. Pack into `[]snapshot.PRInput`.
3. Call `snapshot.Build(...)`.
4. `Engine.snapshotStore.Set(...)`.

- [ ] **Step 1: Add `SnapshotStore` to Engine.Deps**

```go
type Deps struct {
    // ... existing fields ...
    SnapshotStore *snapshot.Store // optional; nil disables dashboard population
    SyncInterval  time.Duration   // mirror of DaemonOpts.Interval, for SyncIntervalSeconds on the snapshot
    Self          string          // self-author login (already required for enumerate)
    AgentRegistry *agentregistry.Registry
}
```

(Add only what is missing; many of these likely already exist under different names. Match the existing config-resolution path.)

- [ ] **Step 2: Test — Engine.Sync writes a snapshot when Store is non-nil**

Use the existing sync test harness (fakes for the VCS provider, beads runner, etc.). Assert that after one `Sync` call, `store.Get()` returns a populated snapshot with the expected `Mine`/`Team` partition.

- [ ] **Step 3: Implement the gather-and-build step at the end of `Engine.Sync`**

Place the call AFTER all per-repo iterations succeed (or partially succeed). On a failed iteration where no PRs were enumerated, do not overwrite the store — preserve the last good snapshot.

```go
if e.deps.SnapshotStore != nil {
    inputs := make([]snapshot.PRInput, 0, sum.TotalPRs)
    for _, rs := range sum.PerRepo {
        for _, pr := range rs.PRs {
            inputs = append(inputs, e.gatherPRInput(ctx, rs.Repo, pr))
        }
    }
    snap := snapshot.Build(snapshot.BuilderInput{
        GeneratedAt:         time.Now().UTC(),
        SyncIntervalSeconds: int(e.deps.SyncInterval.Seconds()),
        Self:                e.deps.Self,
        TeamMembers:         e.allTeamMembers(),
        Registry:            e.deps.AgentRegistry,
        PRs:                 inputs,
    })
    e.deps.SnapshotStore.Set(snap)
    telemetry.SnapshotPresent.Set(1)
}
```

`gatherPRInput` is a private helper that calls the VCS provider for reviews/comments/CI, walks the merge-request bead deps via `beads.DepTreeUp`, and reads the JIRA cache.

- [ ] **Step 4: CLI wiring**

In `cmd/pg-pr/sync.go`, when `--daemon` is set:

- Construct `snapshot.NewStore()`.
- Build the agent registry from `cfg.Agents`.
- Pass both into `Engine.Deps` and `DaemonOpts.Dashboard`.

- [ ] **Step 5: Run integration tests**

```
go test ./internal/sync/... ./cmd/pg-pr/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/sync/ cmd/pg-pr/
git commit -m "feat(pg-pr): populate dashboard snapshot from sync loop"
```

---

## Task 11: New metric `pg_pr_snapshot_present`

**Files:**

- Modify: `pg-pr/internal/telemetry/metrics.go`
- Modify: `pg-pr/internal/telemetry/metrics_test.go`

- [ ] **Step 1: Failing test**

```go
func TestSnapshotPresentMetric(t *testing.T) {
	SnapshotPresent.Set(1)
	resp, err := http.Get(srv.URL + "/metrics")
	// ... assert body contains "pg_pr_snapshot_present 1"
}
```

- [ ] **Step 2: Register metric**

```go
SnapshotPresent = prometheus.NewGauge(prometheus.GaugeOpts{
    Name: "pg_pr_snapshot_present",
    Help: "1 once the dashboard snapshot has been populated for the first time this process; otherwise 0.",
})
// in init():
defaultRegistry.MustRegister(..., SnapshotPresent)
```

- [ ] **Step 3: Run tests**

```
go test ./internal/telemetry/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add internal/telemetry/
git commit -m "feat(pg-pr): pg_pr_snapshot_present gauge"
```

---

## Task 12: Grafana provisioning — Infinity plugin + datasource

**Files:**

- Modify: `phillipgreenii-nix-support-apps/darwin/modules/observability/grafana.nix`

- [ ] **Step 1: Add Infinity to plugin install list**

Locate the Grafana plugin install setting (search for `GF_INSTALL_PLUGINS` or `provisioning` in the module). Append the Infinity plugin:

```nix
GF_INSTALL_PLUGINS = "yesoreyeram-infinity-datasource";
```

If the option already contains plugins, append with `,`.

- [ ] **Step 2: Provision a datasource**

Drop a YAML file into the provisioned-datasources directory:

```nix
{
  apiVersion = 1;
  datasources = [{
    name = "pg-pr-infinity";
    type = "yesoreyeram-infinity-datasource";
    access = "proxy";
    url = "http://127.0.0.1:9818";
    isDefault = false;
    jsonData = {
      auth_method = "none";
    };
  }];
}
```

Match the existing repo convention for declaring provisioned datasources (Nix expression vs static YAML).

- [ ] **Step 3: Format + build**

```
cd phillipgreenii-nix-support-apps
nix fmt
nix flake check
```

- [ ] **Step 4: Commit**

```
git add darwin/modules/observability/
git commit -m "feat(observability): install Infinity plugin + pg-pr datasource"
```

---

## Task 13: Grafana dashboard JSON

**Files:**

- Create: `phillipgreenii-nix-support-apps/packages/otel-stack-tools/grafana/dashboards/pg-pr.json`
- Modify: Whatever Nix expression provisions the dashboards directory (locate via `grep -r "dashboards" packages/otel-stack-tools/`).

The dashboard contains three rows: header stats, My PRs table, Team PRs table. Hand-authoring Grafana JSON from scratch is error-prone; prefer to build the dashboard interactively in the local Grafana, export to JSON, then check that JSON in.

- [ ] **Step 1: Boot the local otel-stack with the new datasource**

```
otel-stack restart
```

Verify Infinity plugin loads and the `pg-pr-infinity` datasource appears in Grafana's datasource list.

- [ ] **Step 2: Manually build the dashboard**

In Grafana UI:

- **Row 1 — Header (3 Stat panels)**:
  - Snapshot age (sec): Infinity, JSON URL `http://127.0.0.1:9818/api/v1/dashboard`. Query type "JSON", rows path `$`. Field: derive `now() - $.generated_at` via the Grafana transform "Add field from calculation". Red threshold > `$.sync_interval_seconds * 2`.
  - Open mine: `count($.mine)`.
  - Waiting on me: `count($.mine[?(@.waiting_on_me == true)])`. Red threshold > 0.

- **Row 2 — My PRs (Table panel)**:
  - Datasource: pg-pr-infinity. Rows path: `$.mine[*]`.
  - Columns: `number`, `title`, `draft`, `ci_status`, `human_approved`, `agent_approved`, `waiting_on_me`, computed `jira_summary` via JSONata `$join(jira.id, ', ')`, computed `beads_summary` via `$join(beads.id, ', ')`.
  - Cell overrides: `number` → data link `${__data.fields.url}`; `ci_status` → value-mapped badges (green/red/yellow/gray); booleans → ✓/✗.

- **Row 3 — Team PRs (Table panel)**:
  - Rows path: `$.team[*]`.
  - Columns: `number`, `title`, `owner`, `ci_status`, `human_approved`, `agent_approved`, `files_changed`, `lines_changed`, computed `jira_summary`.
  - Default sort: `ci_status` (failures first), then `lines_changed` desc.

Save the dashboard, then **Export → Save to file** (Grafana → Dashboard settings → JSON Model → copy).

- [ ] **Step 3: Drop the exported JSON into the package**

```
mkdir -p packages/otel-stack-tools/grafana/dashboards
cp <exported>.json packages/otel-stack-tools/grafana/dashboards/pg-pr.json
```

- [ ] **Step 4: Wire it into the Nix provisioning**

Locate the existing dashboard-provisioning expression (in `darwin/modules/observability/`) and add the new JSON file to its list. Match the existing pattern; do not invent a new mechanism.

- [ ] **Step 5: Rebuild + restart**

```
nix fmt && nix flake check
darwin-rebuild switch --flake .  # or whatever the local build command is
otel-stack restart
```

Open Grafana → dashboards → confirm `pg-pr / My Work` appears and renders.

- [ ] **Step 6: Commit**

```
git add packages/otel-stack-tools/grafana/dashboards/pg-pr.json darwin/modules/observability/
git commit -m "feat(otel-stack): provision pg-pr dashboard"
```

---

## Task 14: End-to-end smoke test

**Files:** None new. Manual verification.

- [ ] **Step 1: Boot the daemon**

```
cd phillipgreenii-nix-agent-support
pg-pr sync --daemon --interval 30s --scrape-addr 127.0.0.1:9818
```

- [ ] **Step 2: Hit the endpoint directly**

```
curl -sS http://127.0.0.1:9818/api/v1/dashboard | jq .
```

Expected: a JSON document with `generated_at`, `sync_interval_seconds`, `mine[]`, `team[]`.

- [ ] **Step 3: Confirm dashboard renders**

Open `http://localhost:3000/d/pg-pr` (or wherever Grafana serves it). Verify rows populate. Confirm the snapshot age stat is below the red threshold.

- [ ] **Step 4: Confirm staleness signal**

Stop the daemon. Wait `sync_interval * 2`. Confirm Grafana snapshot-age stat turns red.

- [ ] **Step 5: Confirm `bd close` propagates**

`bd close` a non-closed `human`-labeled bead under one of your open PRs. Wait one sync tick. Confirm `waiting_on_me` flips appropriately.

---

## Task 15: Documentation

**Files:**

- Modify: `phillipgreenii-nix-agent-support/packages/pg-pr/pg-pr.md`
- Modify: `phillipgreenii-nix-agent-support/packages/pg-pr/README.md` (if a README exists; create only if instructed)

- [ ] **Step 1: Append daemon endpoint reference**

In `pg-pr.md`, append:

```markdown
- Daemon dashboard snapshot (JSON):

`curl http://127.0.0.1:9818/api/v1/dashboard`
```

- [ ] **Step 2: Run prettier / treefmt**

```
prek run --files packages/pg-pr/pg-pr.md
```

- [ ] **Step 3: Commit**

```
git add packages/pg-pr/pg-pr.md
git commit -m "docs(pg-pr): add daemon dashboard endpoint to tldr"
```

---

## Self-Review Checklist

Run before declaring complete:

- [ ] All tasks above checked off.
- [ ] `cd packages/pg-pr && go test ./...` — passes.
- [ ] `nix flake check` — passes (both repos).
- [ ] `prek run --all-files` — passes (both repos).
- [ ] `curl /api/v1/dashboard | jq` shows non-empty `mine` for a real PR you own.
- [ ] Grafana renders both panels with data.
- [ ] Staleness signal goes red when daemon is stopped.
- [ ] `bd close` of a `human`-labeled bead flips `waiting_on_me` on the next tick.

## Open Items Not Blocking Implementation

- Exact claude approval-comment format. Recommended `^Verdict:\s*Approve` regex is illustrative; tune after observing real claude bot comment bodies.
- `bd dep tree --direction=up --json` exact shape. Confirmed exists per `bd dep tree --help`; the Task 8 implementer must verify the JSON envelope and adjust the decoder.
- Whether to add `lines_changed` / `files_changed` to `mine` rows too. Not required by the spec; trivial follow-up if requested.
