# claude-agents-tui: PR Display in Directory Header

**Date:** 2026-04-27

## Summary

Show the GitHub PR associated with a directory's current branch inline in the directory
header row. Uses a file-backed, write-through cache following XDG conventions to avoid
slamming the GitHub API on startup or re-polls.

---

## 1. Data model

### 1a. New `PRInfo` type (`internal/session/pr.go`)

```go
type PRInfo struct {
    Number int
    Title  string
    URL    string
}
```

### 1b. `Directory` gains one field (`internal/aggregate/tree.go`)

```go
type Directory struct {
    Path         string
    Branch       string
    PRInfo       *session.PRInfo // nil when branch has no open PR
    Sessions     []*SessionView
    ...
}
```

`PRInfo` is pointer-typed so the zero value cleanly represents "no PR found / not yet looked up".

---

## 2. PR lookup

### 2a. Raw lookup (`internal/session/pr.go`)

```go
func LookupPR(ctx context.Context, cwd, branch string) (PRInfo, bool, error)
```

Runs `gh pr view --head <branch> --json number,title,url` with working directory set to
`cwd`. Parses the JSON response. Returns `false` when `gh` exits non-zero (no PR found).
Returns an error only on unexpected failures (exec failure, malformed JSON).

### 2b. File-backed cache (`internal/session/pr_cache.go`)

```go
type PRCache struct {
    path    string
    mu      sync.Mutex
    entries map[string]cacheEntry
}

type cacheEntry struct {
    PRInfo    *PRInfo   `json:"prInfo"`    // nil = not found
    FetchedAt time.Time `json:"fetchedAt"`
}
```

**Cache path:** `${XDG_CACHE_HOME:-$HOME/.cache}/claude-agents-tui/pr-cache.json`

**Key:** `cwd + "\x00" + branch` — the null byte is not valid in either field, ensuring no
collisions.

**TTL policy:**

- Found entries (non-nil `PRInfo`): no expiry — PR number, title, and URL do not change for
  a given branch.
- Not-found entries: expire after 5 minutes so a PR created while the TUI is running is
  picked up on the next cache miss.

**Lifecycle:**

- `NewPRCache(path string) *PRCache` — creates the struct, creates parent directories,
  loads the file if it exists. Missing file is not an error.
- `Get(ctx, cwd, branch string) (*PRInfo, error)` — returns the cached entry if valid;
  otherwise calls `LookupPR`, updates the in-memory map, and writes the file.
- Write is full-file rewrite (marshal + `os.WriteFile`) after each update. Cache is small
  (one entry per `cwd:branch` pair ever seen) so full rewrite is fine.

---

## 3. Poller wiring

### 3a. New field on `Poller`

```go
type Poller struct {
    ...
    PRLookupFn func(ctx context.Context, cwd, branch string) (*session.PRInfo, error)
}
```

### 3b. `Snapshot()` changes

After the existing per-session loop (which already populates `s.Branch` via `GitBranch`),
collect unique `(cwd, branch)` pairs and look up PRs once per directory:

```go
// Collect unique (cwd, branch) pairs — one lookup per directory.
type cwdBranch struct{ cwd, branch string }
seen := map[cwdBranch]bool{}
prByDir := map[string]*session.PRInfo{}
if p.PRLookupFn != nil {
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

Pass `prByDir` to `aggregate.Build`.

### 3c. `aggregate.Build` signature change

```go
func Build(
    sessions []*session.Session,
    enriched map[string]SessionEnrichment,
    prByDir map[string]*session.PRInfo,
    block *ccusage.Block,
    planTier string,
) *Tree
```

Inside `Build`, after setting `d.Branch`:

```go
if pr, ok := prByDir[d.Path]; ok {
    d.PRInfo = pr
}
```

### 3d. `main.go` wiring

```go
cache := session.NewPRCache(session.DefaultPRCachePath())
poller.PRLookupFn = cache.Get
```

`DefaultPRCachePath()` (defined in `pr_cache.go`) returns the XDG-compliant path:

```go
func DefaultPRCachePath() string {
    base := os.Getenv("XDG_CACHE_HOME")
    if base == "" {
        home, _ := os.UserHomeDir()
        base = filepath.Join(home, ".cache")
    }
    return filepath.Join(base, "claude-agents-tui", "pr-cache.json")
}
```

---

## 4. Render

### 4a. `renderDirRow` (`internal/render/tree.go`)

Current output:

```
/path/to/dir  🌿 my-feature                          2● · 15.3k tok
```

New output when PR exists:

```
/path/to/dir  🌿 my-feature  →  #42 Add the thing   2● · 15.3k tok
```

The PR portion is appended to `branchStr` in `renderDirRow`. The PR number is rendered as an
OSC 8 terminal hyperlink so it is clickable in supporting terminals (iTerm2, Kitty, WezTerm,
GNOME Terminal ≥3.26):

```go
func osc8Link(url, text string) string {
    return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
}
```

Title is truncated to 40 characters before appending to prevent overflow into the rollup
column. Truncation uses the existing `truncate()` helper.

### 4b. Theme

No new theme styles needed. The PR text renders plain (no color) to avoid visual noise —
the branch already has a color style. The hyperlink underline comes from the terminal's OSC 8
rendering, not from lipgloss.

---

## 5. Cache pruning

No active pruning. Entries accumulate per unique `(cwd, branch)` pair. In practice this is
a small set (one per project branch ever worked on). If the file grows large the user can
delete it.

---

## 6. Testing

| File                                   | Tests                                                                    |
| -------------------------------------- | ------------------------------------------------------------------------ |
| `internal/session/pr_cache_test.go`    | Found/not-found TTL, key collision, file round-trip, XDG path resolution |
| `internal/session/pr_test.go`          | `LookupPR` with a fake `gh` binary via `PATH` override                   |
| `internal/aggregate/aggregate_test.go` | `Build` with `prByDir` — verify `d.PRInfo` assignment                    |
| `internal/render/tree_test.go`         | `renderDirRow` with non-nil `PRInfo` — verify PR text in output          |
| `internal/poller/poller_test.go`       | Inject mock `PRLookupFn`, verify called once per unique cwd              |

---

## Affected files

| File                                   | Change                                                                     |
| -------------------------------------- | -------------------------------------------------------------------------- |
| `internal/session/pr.go`               | New: `PRInfo` struct, `LookupPR`                                           |
| `internal/session/pr_cache.go`         | New: `PRCache`, `NewPRCache`, `Get`, `DefaultPRCachePath`, file read/write |
| `internal/session/pr_test.go`          | New: tests for `LookupPR`                                                  |
| `internal/session/pr_cache_test.go`    | New: cache tests                                                           |
| `internal/aggregate/tree.go`           | Add `PRInfo *session.PRInfo` to `Directory`                                |
| `internal/aggregate/aggregate.go`      | Accept `prByDir` param, assign `d.PRInfo`                                  |
| `internal/aggregate/aggregate_test.go` | Update call sites, add `prByDir` tests                                     |
| `internal/poller/poller.go`            | Add `PRLookupFn` field, per-dir lookup in `Snapshot`                       |
| `internal/poller/poller_test.go`       | Inject mock `PRLookupFn`, verify deduplication                             |
| `internal/render/tree.go`              | `renderDirRow`: append PR to branch string, OSC 8 link                     |
| `internal/render/tree_test.go`         | Add PR render tests                                                        |
| `cmd/claude-agents-tui/main.go`        | Wire `NewPRCache` + `cache.Get` into Poller                                |
