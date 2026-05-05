# PR Display in Directory Header — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the GitHub PR for a directory's current branch inline in the dir header row, with a file-backed XDG cache to avoid hammering the GitHub API on startup.

**Architecture:** New `internal/session/pr.go` holds `PRInfo` and the raw `gh` CLI call. New `internal/session/pr_cache.go` wraps it with a file-backed write-through cache. `Poller` gets an injectable `PRLookupFn` (matches `CCUsageFn` pattern) that calls the cache. `aggregate.Build` accepts a `prByDir` map and sets `Directory.PRInfo`. `renderDirRow` appends the PR inline after the branch using an OSC 8 terminal hyperlink.

**Tech Stack:** Go 1.24, lipgloss v1.1.0, `gh` CLI, XDG cache (`~/.cache` fallback), `encoding/json`, `sync.Mutex`

---

## File map

| File                                   | Status | Role                                                                   |
| -------------------------------------- | ------ | ---------------------------------------------------------------------- |
| `internal/session/pr.go`               | Create | `PRInfo` struct, `LookupPR` raw `gh` call                              |
| `internal/session/pr_test.go`          | Create | tests for `LookupPR` (fake `gh` via PATH)                              |
| `internal/session/pr_cache.go`         | Create | `PRCache`: file-backed write-through cache, `DefaultPRCachePath`       |
| `internal/session/pr_cache_test.go`    | Create | cache hit/miss/TTL/file round-trip tests                               |
| `internal/aggregate/tree.go`           | Modify | add `PRInfo *session.PRInfo` to `Directory`                            |
| `internal/aggregate/aggregate.go`      | Modify | add `prByDir` param to `Build`, assign `d.PRInfo`                      |
| `internal/aggregate/aggregate_test.go` | Modify | add `nil` arg to existing `Build` calls; add `prByDir` assignment test |
| `internal/poller/poller.go`            | Modify | add `PRLookupFn` field; per-dir dedup lookup in `Snapshot`             |
| `internal/poller/poller_test.go`       | Modify | inject mock `PRLookupFn`, verify called once per unique cwd            |
| `internal/render/tree.go`              | Modify | `osc8Link` helper; `renderDirRow` appends PR after branch              |
| `internal/render/tree_test.go`         | Modify | assert PR text in dir row; width alignment still holds                 |
| `cmd/claude-agents-tui/main.go`        | Modify | wire `NewPRCache` + `cache.Get` into `Poller.PRLookupFn`               |
| `default.nix`                          | Modify | add `gh` to `wrapProgram --prefix PATH`                                |

---

## Task 1: `PRInfo` struct and `LookupPR`

**Files:**

- Create: `internal/session/pr.go`
- Create: `internal/session/pr_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/session/pr_test.go`:

```go
package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeFakeGH installs a fake `gh` binary at the front of PATH for this test.
func writeFakeGH(t *testing.T, stdout string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	var script string
	if exitCode == 0 {
		// Use printf to avoid issues with special chars in stdout
		script = fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nexit 0\n", stdout)
	} else {
		script = fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestLookupPRFound(t *testing.T) {
	writeFakeGH(t,
		`{"number":42,"title":"Add the thing","url":"https://github.com/owner/repo/pull/42"}`,
		0,
	)
	info, found, err := LookupPR(context.Background(), t.TempDir(), "feat/xyz")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if info.Number != 42 {
		t.Errorf("Number = %d, want 42", info.Number)
	}
	if info.Title != "Add the thing" {
		t.Errorf("Title = %q, want \"Add the thing\"", info.Title)
	}
	if info.URL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("URL = %q unexpected", info.URL)
	}
}

func TestLookupPRNotFound(t *testing.T) {
	writeFakeGH(t, "", 1)
	_, found, err := LookupPR(context.Background(), t.TempDir(), "feat/xyz")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected found=false when gh exits non-zero")
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd packages/claude-agents-tui
go test ./internal/session/ -run TestLookupPR -v
```

Expected: `FAIL — undefined: LookupPR`

- [ ] **Step 3: Implement `pr.go`**

Create `internal/session/pr.go`:

```go
package session

import (
	"context"
	"encoding/json"
	"os/exec"
)

type PRInfo struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// LookupPR calls `gh pr view --head <branch> --json number,title,url` in cwd.
// Returns (PRInfo, true, nil) when a PR is found, (PRInfo{}, false, nil) when
// gh exits non-zero (no PR), and (PRInfo{}, false, err) only on unexpected failures.
func LookupPR(ctx context.Context, cwd, branch string) (PRInfo, bool, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--head", branch, "--json", "number,title,url")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return PRInfo{}, false, nil
	}
	var info PRInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return PRInfo{}, false, err
	}
	return info, true, nil
}
```

- [ ] **Step 4: Run to confirm pass**

```bash
go test ./internal/session/ -run TestLookupPR -v
```

Expected: `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/session/pr.go internal/session/pr_test.go
git commit -m "feat(tui): add PRInfo type and LookupPR via gh CLI"
```

---

## Task 2: File-backed `PRCache`

**Files:**

- Create: `internal/session/pr_cache.go`
- Create: `internal/session/pr_cache_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/session/pr_cache_test.go`:

```go
package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPRCacheHitFound(t *testing.T) {
	calls := 0
	c := newTestCache(t, func(_ context.Context, _, _ string) (PRInfo, bool, error) {
		calls++
		return PRInfo{Number: 7, Title: "Hello", URL: "https://gh/7"}, true, nil
	})

	// First call — miss, should fetch.
	pr, err := c.Get(context.Background(), "/repo", "feat/a")
	if err != nil {
		t.Fatal(err)
	}
	if pr == nil || pr.Number != 7 {
		t.Fatalf("first Get: want pr.Number=7, got %v", pr)
	}
	if calls != 1 {
		t.Fatalf("expected 1 LookupFn call, got %d", calls)
	}

	// Second call — hit, must NOT call LookupFn again.
	pr2, err := c.Get(context.Background(), "/repo", "feat/a")
	if err != nil {
		t.Fatal(err)
	}
	if pr2 == nil || pr2.Number != 7 {
		t.Fatalf("second Get: want pr.Number=7, got %v", pr2)
	}
	if calls != 1 {
		t.Fatalf("cache hit should not call LookupFn; got %d calls", calls)
	}
}

func TestPRCacheNotFoundWithinTTL(t *testing.T) {
	calls := 0
	now := time.Now()
	c := newTestCache(t, func(_ context.Context, _, _ string) (PRInfo, bool, error) {
		calls++
		return PRInfo{}, false, nil
	})
	c.Now = func() time.Time { return now }

	// First call — miss.
	pr, _ := c.Get(context.Background(), "/repo", "feat/b")
	if pr != nil {
		t.Fatalf("expected nil for not-found, got %v", pr)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}

	// Second call within TTL — must reuse not-found entry.
	pr, _ = c.Get(context.Background(), "/repo", "feat/b")
	if pr != nil {
		t.Fatalf("expected nil within TTL, got %v", pr)
	}
	if calls != 1 {
		t.Fatalf("within TTL should not re-fetch; got %d calls", calls)
	}
}

func TestPRCacheNotFoundExpired(t *testing.T) {
	calls := 0
	now := time.Now()
	c := newTestCache(t, func(_ context.Context, _, _ string) (PRInfo, bool, error) {
		calls++
		return PRInfo{}, false, nil
	})
	c.Now = func() time.Time { return now }

	c.Get(context.Background(), "/repo", "feat/c") //nolint:errcheck

	// Advance clock past TTL.
	c.Now = func() time.Time { return now.Add(prNotFoundTTL + time.Second) }
	c.Get(context.Background(), "/repo", "feat/c") //nolint:errcheck

	if calls != 2 {
		t.Fatalf("expired not-found should re-fetch; got %d calls", calls)
	}
}

func TestPRCacheDifferentBranchesTreatedIndependently(t *testing.T) {
	calls := 0
	c := newTestCache(t, func(_ context.Context, _, branch string) (PRInfo, bool, error) {
		calls++
		return PRInfo{Number: calls, Title: branch, URL: ""}, true, nil
	})

	c.Get(context.Background(), "/repo", "branch-a") //nolint:errcheck
	c.Get(context.Background(), "/repo", "branch-b") //nolint:errcheck

	if calls != 2 {
		t.Fatalf("different branches need independent lookups; got %d calls", calls)
	}
}

func TestPRCacheFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c1 := &PRCache{
		Path:     path,
		LookupFn: func(_ context.Context, _, _ string) (PRInfo, bool, error) {
			return PRInfo{Number: 99, Title: "Round trip", URL: "u"}, true, nil
		},
		Now: time.Now,
	}
	c1.entries = map[string]cacheEntry{}

	c1.Get(context.Background(), "/repo", "feat/rt") //nolint:errcheck

	// Load fresh cache from same file.
	c2 := NewPRCache(path)
	c2.LookupFn = func(_ context.Context, _, _ string) (PRInfo, bool, error) {
		t.Fatal("should not call LookupFn — cache loaded from file")
		return PRInfo{}, false, nil
	}
	pr, err := c2.Get(context.Background(), "/repo", "feat/rt")
	if err != nil {
		t.Fatal(err)
	}
	if pr == nil || pr.Number != 99 {
		t.Fatalf("file round-trip: want Number=99, got %v", pr)
	}
}

func TestDefaultPRCachePath(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg")
	p := DefaultPRCachePath()
	want := "/tmp/xdg/claude-agents-tui/pr-cache.json"
	if p != want {
		t.Errorf("DefaultPRCachePath = %q, want %q", p, want)
	}
}

func TestDefaultPRCachePathFallback(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	p := DefaultPRCachePath()
	if p == "" {
		t.Error("DefaultPRCachePath returned empty string")
	}
	// Must contain the app name directory
	if !filepath.IsAbs(p) {
		t.Errorf("expected absolute path, got %q", p)
	}
}

// newTestCache creates a PRCache wired to a given lookup function, using a temp file.
func newTestCache(t *testing.T, fn func(context.Context, string, string) (PRInfo, bool, error)) *PRCache {
	t.Helper()
	c := NewPRCache(filepath.Join(t.TempDir(), "pr-cache.json"))
	c.LookupFn = fn
	return c
}

// Satisfy json round-trip: entries must be exported for marshal/unmarshal.
var _ = json.Marshal
```

- [ ] **Step 2: Run to confirm failure**

```bash
go test ./internal/session/ -run TestPRCache -v
```

Expected: `FAIL — undefined: PRCache, NewPRCache, DefaultPRCachePath, prNotFoundTTL`

- [ ] **Step 3: Implement `pr_cache.go`**

Create `internal/session/pr_cache.go`:

```go
package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const prNotFoundTTL = 5 * time.Minute

type cacheEntry struct {
	PR        *PRInfo   `json:"pr"`        // nil = not found
	FetchedAt time.Time `json:"fetchedAt"`
}

// PRCache is a file-backed write-through cache for PR lookups.
// Found entries never expire. Not-found entries expire after prNotFoundTTL.
type PRCache struct {
	Path     string
	LookupFn func(ctx context.Context, cwd, branch string) (PRInfo, bool, error)
	Now      func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

// DefaultPRCachePath returns ${XDG_CACHE_HOME}/claude-agents-tui/pr-cache.json,
// falling back to ~/.cache when XDG_CACHE_HOME is unset.
func DefaultPRCachePath() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "claude-agents-tui", "pr-cache.json")
}

// NewPRCache creates a PRCache, loading any existing file. Missing file is not an error.
func NewPRCache(path string) *PRCache {
	c := &PRCache{
		Path:     path,
		LookupFn: LookupPR,
		Now:      time.Now,
		entries:  map[string]cacheEntry{},
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &c.entries)
	}
	return c
}

func prCacheKey(cwd, branch string) string {
	return cwd + "\x00" + branch
}

// Get returns the cached PRInfo for (cwd, branch), fetching via LookupFn on miss.
// Returns (nil, nil) when no PR exists for the branch.
func (c *PRCache) Get(ctx context.Context, cwd, branch string) (*PRInfo, error) {
	key := prCacheKey(cwd, branch)
	now := c.Now()

	c.mu.Lock()
	e, ok := c.entries[key]
	if ok && e.PR != nil {
		// Found entry — never expires.
		c.mu.Unlock()
		return e.PR, nil
	}
	if ok && now.Sub(e.FetchedAt) < prNotFoundTTL {
		// Not-found entry within TTL.
		c.mu.Unlock()
		return nil, nil
	}
	c.mu.Unlock()

	// Fetch without holding the lock.
	info, found, err := c.LookupFn(ctx, cwd, branch)
	if err != nil {
		return nil, err
	}

	entry := cacheEntry{FetchedAt: now}
	if found {
		entry.PR = &info
	}

	c.mu.Lock()
	c.entries[key] = entry
	c.save()
	c.mu.Unlock()

	return entry.PR, nil
}

// save writes the entries map to disk. Must be called with mu held.
func (c *PRCache) save() {
	data, err := json.Marshal(c.entries)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(c.Path, data, 0o644)
}
```

- [ ] **Step 4: Run to confirm pass**

```bash
go test ./internal/session/ -run TestPRCache -v
```

Expected: all `TestPRCache*` tests `PASS`

- [ ] **Step 5: Run full session package tests**

```bash
go test ./internal/session/... -v
```

Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add internal/session/pr_cache.go internal/session/pr_cache_test.go
git commit -m "feat(tui): add file-backed PRCache with XDG path and TTL"
```

---

## Task 3: `Directory.PRInfo` field and `Build` wiring

**Files:**

- Modify: `internal/aggregate/tree.go:22-32`
- Modify: `internal/aggregate/aggregate.go:13` (signature) + `aggregate.go:24-26` (assignment)
- Modify: `internal/aggregate/aggregate_test.go` (fix call sites, add new test)

- [ ] **Step 1: Write the failing test**

Add to `internal/aggregate/aggregate_test.go`:

```go
func TestBuildSetsPRInfo(t *testing.T) {
	sessions := []*session.Session{
		{SessionID: "a", Cwd: "/p1"},
		{SessionID: "b", Cwd: "/p2"},
	}
	enriched := map[string]SessionEnrichment{}
	prByDir := map[string]*session.PRInfo{
		"/p1": {Number: 42, Title: "My PR", URL: "https://gh/42"},
	}
	tree := Build(sessions, enriched, prByDir, nil, "max_5x")
	byPath := map[string]*Directory{}
	for _, d := range tree.Dirs {
		byPath[d.Path] = d
	}
	if byPath["/p1"].PRInfo == nil {
		t.Fatal("/p1 should have PRInfo set")
	}
	if byPath["/p1"].PRInfo.Number != 42 {
		t.Errorf("/p1 PRInfo.Number = %d, want 42", byPath["/p1"].PRInfo.Number)
	}
	if byPath["/p2"].PRInfo != nil {
		t.Error("/p2 should have nil PRInfo (not in prByDir)")
	}
}
```

- [ ] **Step 2: Update existing `Build` call sites in the test file**

In `aggregate_test.go`, the three existing `Build(...)` calls do not have the `prByDir` parameter. Add `nil` as the third argument to each:

`TestBuildGroupsByCwdAndTotalsTokens`:

```go
tree := Build(sessions, enriched, nil, block, "max_5x")
```

`TestBuildSessionsSortedByStartedAtDesc`:

```go
tree := Build(sessions, enriched, nil, nil, "max_5x")
```

- [ ] **Step 3: Run to confirm failure**

```bash
go test ./internal/aggregate/ -run TestBuildSetsPRInfo -v
```

Expected: `FAIL — too many arguments in call to Build` (or signature mismatch)

- [ ] **Step 4: Add `PRInfo` field to `Directory`**

In `internal/aggregate/tree.go`, add `PRInfo` field after `Branch`:

```go
type Directory struct {
	Path         string
	Branch       string
	PRInfo       *session.PRInfo
	Sessions     []*SessionView
	WorkingN     int
	IdleN        int
	DormantN     int
	TotalTokens  int
	TotalCostUSD float64
}
```

- [ ] **Step 5: Update `Build` signature and add PRInfo assignment**

In `internal/aggregate/aggregate.go`, change the signature:

```go
func Build(sessions []*session.Session, enriched map[string]SessionEnrichment, prByDir map[string]*session.PRInfo, block *ccusage.Block, planTier string) *Tree {
```

After the sessions loop (the `for _, s := range sessions` block) and the sort-within-dir block, add a loop to assign PRInfo before building the tree. Place it after the sort loop and before the `grandTokens` block:

```go
	// Assign PRInfo from the per-directory lookup results.
	if prByDir != nil {
		for _, d := range byDir {
			d.PRInfo = prByDir[d.Path]
		}
	}
```

The `import` block in `aggregate.go` must include `session`. It already imports `session` — no change needed.

- [ ] **Step 6: Run to confirm pass**

```bash
go test ./internal/aggregate/... -v
```

Expected: all pass

- [ ] **Step 7: Confirm the one external caller still compiles**

```bash
go build ./...
```

Expected: `FAIL — aggregate.Build` call in `poller.go` has wrong arg count. That is expected — fix it in Task 4.

- [ ] **Step 8: Commit**

```bash
git add internal/aggregate/tree.go internal/aggregate/aggregate.go internal/aggregate/aggregate_test.go
git commit -m "feat(tui): add PRInfo to Directory; extend Build with prByDir param"
```

---

## Task 4: Poller `PRLookupFn` and per-directory dedup

**Files:**

- Modify: `internal/poller/poller.go`
- Modify: `internal/poller/poller_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/poller/poller_test.go`:

```go
func TestSnapshotPRLookupCalledOncePerDir(t *testing.T) {
	type call struct{ cwd, branch string }
	var calls []call

	p := &Poller{
		SessionsDir: "../../tests/fixtures/sessions",
		ClaudeHome:  "../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
		CCUsageFn:   func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil },
		PRLookupFn: func(_ context.Context, cwd, branch string) (*session.PRInfo, error) {
			calls = append(calls, call{cwd, branch})
			return nil, nil
		},
	}
	_, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The fixture has one session in /tmp/x with no branch (no git repo).
	// PRLookupFn must not be called for sessions with empty branch.
	for _, c := range calls {
		if c.branch == "" {
			t.Errorf("PRLookupFn called with empty branch for cwd=%q", c.cwd)
		}
	}
}
```

Also add an import for the `session` package at the top of `poller_test.go`:

```go
import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)
```

- [ ] **Step 2: Run to confirm failure**

```bash
go test ./internal/poller/ -run TestSnapshotPRLookup -v
```

Expected: `FAIL — unknown field PRLookupFn in Poller`

- [ ] **Step 3: Add `PRLookupFn` to `Poller`**

In `internal/poller/poller.go`, add the field to the struct after `CCUsageStateFn`:

```go
type Poller struct {
	SessionsDir      string
	ClaudeHome       string
	PidAlive         func(int) bool
	PlanTier         string
	WorkingThreshold time.Duration
	IdleThreshold    time.Duration
	BurnWindowShort  time.Duration
	BurnWindowLong   time.Duration
	Now              func() time.Time
	CCUsageFn        func(ctx context.Context) ([]byte, error)
	CCUsageStateFn   func() (probed bool, err error)
	PRLookupFn       func(ctx context.Context, cwd, branch string) (*session.PRInfo, error)

	burnShort       map[string]*burnrate.Buffer
	burnLong        map[string]*burnrate.Buffer
	prevTotalTokens map[string]int
}
```

- [ ] **Step 4: Add the per-dir PR lookup in `Snapshot`**

In `internal/poller/poller.go`, in `Snapshot()`, add this block after the burn-buffer pruning loop and before the ccusage block fetch. Insert after the `for id := range p.burnShort` block:

```go
	// Look up PRs once per unique (cwd, branch) pair — one gh call per directory.
	type cwdBranch struct{ cwd, branch string }
	prByDir := map[string]*session.PRInfo{}
	if p.PRLookupFn != nil {
		seen := map[cwdBranch]bool{}
		for _, s := range sessions {
			if s.Branch == "" {
				continue
			}
			key := cwdBranch{s.Cwd, s.Branch}
			if seen[key] {
				continue
			}
			seen[key] = true
			if info, err := p.PRLookupFn(ctx, s.Cwd, s.Branch); err == nil {
				prByDir[s.Cwd] = info
			}
		}
	}
```

- [ ] **Step 5: Fix the `aggregate.Build` call**

In `Snapshot()`, change:

```go
tree := aggregate.Build(sessions, enriched, block, p.PlanTier)
```

to:

```go
tree := aggregate.Build(sessions, enriched, prByDir, block, p.PlanTier)
```

- [ ] **Step 6: Add `session` to poller imports**

The `session` package import is already present (`"github.com/phillipgreenii/claude-agents-tui/internal/session"`). Confirm — no change needed.

- [ ] **Step 7: Run all poller tests**

```bash
go test ./internal/poller/... -v
```

Expected: all pass

- [ ] **Step 8: Build check**

```bash
go build ./...
```

Expected: `ok` — all packages compile

- [ ] **Step 9: Commit**

```bash
git add internal/poller/poller.go internal/poller/poller_test.go
git commit -m "feat(tui): add PRLookupFn to Poller; deduplicate per-dir PR lookups"
```

---

## Task 5: Render PR inline in directory row

**Files:**

- Modify: `internal/render/tree.go`
- Modify: `internal/render/tree_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/render/tree_test.go`:

```go
func TestDirRowShowsPRInfo(t *testing.T) {
	d := &aggregate.Directory{
		Path:   "/p",
		Branch: "feat/xyz",
		PRInfo: &session.PRInfo{Number: 42, Title: "Add the thing", URL: "https://github.com/owner/repo/pull/42"},
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Name: "n", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		t.Fatal("no output")
	}
	dirLine := lines[0]
	if !strings.Contains(dirLine, "#42") {
		t.Errorf("expected '#42' in dir row, got: %q", dirLine)
	}
	if !strings.Contains(dirLine, "Add the thing") {
		t.Errorf("expected PR title in dir row, got: %q", dirLine)
	}
}

func TestDirRowNoPRWhenNil(t *testing.T) {
	d := &aggregate.Directory{
		Path:   "/p",
		Branch: "feat/xyz",
		PRInfo: nil,
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Name: "n", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})
	if strings.Contains(out, "#") {
		t.Errorf("expected no PR in dir row when PRInfo is nil, got: %q", out)
	}
}

func TestDirRowPRTitleTruncated(t *testing.T) {
	longTitle := strings.Repeat("x", 60)
	d := &aggregate.Directory{
		Path:   "/p",
		Branch: "b",
		PRInfo: &session.PRInfo{Number: 1, Title: longTitle, URL: "u"},
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Name: "n", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})
	// The full 60-char title must not appear verbatim.
	if strings.Contains(out, longTitle) {
		t.Errorf("expected title truncated, but full title appeared in: %q", out)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
go test ./internal/render/ -run "TestDirRow" -v
```

Expected: `FAIL — TestDirRowShowsPRInfo: '#42' not found` (and compile error about `session.PRInfo` not imported in test)

- [ ] **Step 3: Update test imports**

In `internal/render/tree_test.go`, the `session` import is already present. Confirm no change needed by checking the import block at the top.

- [ ] **Step 4: Add `osc8Link` and update `renderDirRow`**

In `internal/render/tree.go`, add the `osc8Link` helper and update `renderDirRow`.

Add after the `truncate` function at the bottom of the file:

```go
// osc8Link wraps text in an OSC 8 terminal hyperlink. Terminals that support
// OSC 8 render text as a clickable link; others display the plain text.
// lipgloss v1.1.0 (charmbracelet/x/ansi) treats OSC sequences as zero-width,
// so width calculations remain correct.
func osc8Link(url, text string) string {
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
}
```

Update `renderDirRow` to append PR info after the branch:

```go
func renderDirRow(d *aggregate.Directory, opts TreeOpts) string {
	rollup := dirRollup(d, opts)
	rowWidth := prefixCols + minLabelWidth + statsBlockCols
	if opts.Width > 0 {
		rowWidth = opts.Width
	}
	branchStr := ""
	if d.Branch != "" {
		branchStr = "  🌿 " + opts.Theme.Branch.Render(d.Branch)
		if d.PRInfo != nil {
			prNum := osc8Link(d.PRInfo.URL, fmt.Sprintf("#%d", d.PRInfo.Number))
			prTitle := truncate(d.PRInfo.Title, 40)
			branchStr += "  →  " + prNum + " " + prTitle
		}
	}
	leftWidth := max(rowWidth-lipgloss.Width(rollup)-1, lipgloss.Width(d.Path))
	pathStyle := opts.Theme.DirRow.Width(leftWidth).Align(lipgloss.Left)
	return pathStyle.Render(d.Path+branchStr) + " " + rollup + "\n"
}
```

The `session` package needs to be imported in `render/tree.go` — check the import block. The `aggregate` package is already imported, and `Directory.PRInfo` is `*session.PRInfo`. Since we access it via `d.PRInfo` (already typed), no direct `session` import is needed in `tree.go`.

- [ ] **Step 5: Run to confirm tests pass**

```bash
go test ./internal/render/ -run "TestDirRow" -v
```

Expected: all `TestDirRow*` tests `PASS`

- [ ] **Step 6: Run all render tests including alignment checks**

```bash
go test ./internal/render/... -v
```

Expected: all pass — specifically `TestDirRowRollupRightAligned` must still pass (confirms OSC 8 sequences are zero-width to lipgloss).

- [ ] **Step 7: Commit**

```bash
git add internal/render/tree.go internal/render/tree_test.go
git commit -m "feat(tui): render PR number and title inline in directory row with OSC 8 link"
```

---

## Task 6: Wire cache into `main.go` and add `gh` to Nix package

**Files:**

- Modify: `cmd/claude-agents-tui/main.go`
- Modify: `default.nix`

- [ ] **Step 1: Wire `PRCache` into `main.go`**

In `cmd/claude-agents-tui/main.go`, after the `ccusageCache` setup block (around line 56), add:

```go
	prCache := session.NewPRCache(session.DefaultPRCachePath())
```

Then in the `p := &poller.Poller{...}` literal, add `PRLookupFn` after `CCUsageStateFn`:

```go
	p := &poller.Poller{
		SessionsDir:      session.DefaultSessionsDir(),
		ClaudeHome:       filepath.Join(home, ".claude"),
		PidAlive:         session.DefaultPidAlive,
		PlanTier:         cfg.PlanTier,
		WorkingThreshold: cfg.WorkingThreshold,
		IdleThreshold:    cfg.IdleThreshold,
		BurnWindowShort:  cfg.BurnWindowShort,
		BurnWindowLong:   cfg.BurnWindowLong,
		Now:              time.Now,
		CCUsageFn:        ccusageCache.Get,
		CCUsageStateFn:   func() (bool, error) { return ccusageCache.Probed(), ccusageCache.LastErr() },
		PRLookupFn:       prCache.Get,
	}
```

- [ ] **Step 2: Build to confirm it compiles**

```bash
go build ./cmd/claude-agents-tui/
```

Expected: `ok`

- [ ] **Step 3: Add `gh` to the Nix package**

In `default.nix`, update the function arguments to include `gh`:

```nix
{
  lib,
  buildGoModule,
  makeWrapper,
  ccusage,
  gh,
}:
```

Update the `wrapProgram` line to include `gh`:

```nix
    wrapProgram $out/bin/claude-agents-tui \
      --prefix PATH : ${lib.makeBinPath [ ccusage gh ]}
```

- [ ] **Step 4: Update the Nix build call site**

In `flake.nix`, find where `claude-agents-tui` package is called and add `gh` to its arguments. Search for the pattern like:

```nix
claude-agents-tui = pkgs.callPackage ./packages/claude-agents-tui { inherit ccusage; };
```

Update to:

```nix
claude-agents-tui = pkgs.callPackage ./packages/claude-agents-tui { inherit ccusage; gh = pkgs.gh; };
```

(The exact form depends on how `callPackage` is invoked — `pkgs.callPackage` auto-resolves `gh` from `pkgs` if `gh` is in `nixpkgs`, so adding it to the function signature in `default.nix` may be sufficient without changing `flake.nix`. Verify with a build.)

- [ ] **Step 5: Build the Nix package**

```bash
nix build .#claude-agents-tui 2>&1 | tail -20
```

Expected: build succeeds

- [ ] **Step 6: Run full test suite**

```bash
go test ./... -v 2>&1 | tail -40
```

Expected: all pass

- [ ] **Step 7: Commit**

```bash
git add cmd/claude-agents-tui/main.go default.nix
git commit -m "feat(tui): wire PRCache into Poller; add gh to Nix package PATH"
```

---

## Self-Review

**Spec coverage check:**

- [x] §1 `PRInfo` struct — Task 1
- [x] §2a `LookupPR` raw call — Task 1
- [x] §2b `PRCache` with XDG path, found/not-found TTL, file-backed — Task 2
- [x] §3a `PRLookupFn` on Poller — Task 4
- [x] §3b `Snapshot` dedup loop — Task 4
- [x] §3c `Build` signature + `d.PRInfo` assignment — Task 3
- [x] §3d `main.go` wiring + `NewPRCache` — Task 6
- [x] §4a PR inline in `renderDirRow`, truncation, OSC 8 — Task 5
- [x] §5 Cache pruning (intentionally none — noted in spec) — no task needed
- [x] §6 Testing for all layers — each task has tests

**Placeholder scan:** No TBD/TODO/placeholder text found.

**Type consistency:**

- `PRCache.Get` returns `(*session.PRInfo, error)` — matches `Poller.PRLookupFn` signature
- `prByDir map[string]*session.PRInfo` flows: Poller → `Build` param → `d.PRInfo = prByDir[d.Path]`
- `Directory.PRInfo *session.PRInfo` — matches render access `d.PRInfo`
- `osc8Link(url, text string) string` — defined Task 5, used Task 5 only

**One risk to note:** `TestDirRowRollupRightAligned` checks `lipgloss.Width(dirLine) == lipgloss.Width(sessionLine)`. After Task 5, if a PR is present the dir line will be wider than in that test — but that test uses `PRInfo: nil` (zero value), so it remains unaffected. The new `TestDirRowShowsPRInfo` test does not check alignment — this is intentional, since the PR text extends past `leftWidth` when present (same behavior as a long branch name).
