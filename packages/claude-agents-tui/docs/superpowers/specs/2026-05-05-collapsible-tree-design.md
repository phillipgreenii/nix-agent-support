# Collapsible Path Tree for claude-agents-tui

**Status**: Accepted  
**Date**: 2026-05-05

## Overview

Replace the flat directory-per-line session list with a collapsible path tree.
Directories sharing a common ancestor are nested. Collapsed nodes roll up
session stats. Collapse state persists across restarts.

## Design Decisions

- **Tree style**: Common-prefix compression — top-level nodes are the longest
  common ancestors; children show only the unique path suffix relative to their
  parent.
- **Rollup stats**: Mirrors the existing `C` (costMode) toggle — shows either
  total tokens or total USD cost, plus summed burn rate.
- **Keybindings**: `Space` toggles; `Left`/`h` collapses; `Right`/`l` expands.
- **Default state**: All nodes expanded on first run (no cache file present).
- **Intermediate nodes**: Single-child nodes with no direct sessions are
  compressed away (path trie compression). Nodes with no sessions but 2+
  children are kept as branch points.

## Architecture

### New package: `internal/treestate`

Persists collapse state to `~/.cache/claude-agents-tui/tree-state.json`.

```go
type State struct{ collapsed map[string]bool }

func Load(cacheDir string) *State        // empty state on error/missing
func (s *State) IsCollapsed(path string) bool
func (s *State) Toggle(path string)
func (s *State) Save(cacheDir string) error
```

JSON format:
```json
{ "collapsedPaths": ["/Volumes/ziprecruiter/monorepo"] }
```

**Stale path handling**: `State` is a plain string set. `FlattenPathTree` only
consults paths present in the current live tree — stale entries sit inert and
are never surfaced to the user. `Save` writes the full `collapsed` map as-is;
stale paths accumulate harmlessly in the JSON file. No errors or panics from
stale cache entries.

**Save timing**: written synchronously after every `Toggle`.

### New function: `aggregate.BuildPathTree`

```go
type PathNode struct {
    FullPath       string
    DisplayPath    string       // full path for roots; relative suffix for children
    Depth          int          // indentation level (0 = root)
    DirectSessions []*SessionView
    Children       []*PathNode
    // Rollup stats (aggregated from all descendants)
    WorkingN     int
    IdleN        int
    DormantN     int
    TotalTokens  int
    TotalCostUSD float64
    BurnRateSum  float64  // sum of BurnRateShort across all descendant sessions
}

func BuildPathTree(dirs []*Directory) []*PathNode
```

**Algorithm**:

1. Build a path trie from all `Cwd` values in `dirs`.
2. Compress: any node with no direct sessions and exactly one child is elided;
   its child is promoted with a combined display path.
3. Assign `DisplayPath`: root nodes use their full path; child nodes show only
   the suffix relative to their parent (e.g. `finance/partnerships` under
   `/Volumes/ziprecruiter/monorepo`).
4. Compute rollup stats bottom-up during construction.

`aggregate.Tree` and `aggregate.Directory` are unchanged; `BuildPathTree` is
a pure view-layer transformation called from the TUI on each tree refresh.

### Modified: `internal/render`

**New `RowKind` value**: `PathNodeKind`

**New `Row` fields**:
```go
NodePath  string  // PathNodeKind: full path (collapse state key)
Depth     int     // PathNodeKind: indentation level
Collapsed bool    // PathNodeKind: current collapse state
```

**New `FlattenPathTree(nodes []*aggregate.PathNode, state *treestate.State, opts TreeOpts) []Row`**:

- Walks `[]*PathNode` recursively.
- Emits one `PathNodeKind` row per node.
- If the node is collapsed: skips all descendant sessions and children.
- If expanded: recurses into `DirectSessions` (as `SessionKind` rows) then
  `Children`.
- `BlankKind` rows separate top-level nodes.

**New `RenderPathTree`** — dir row format:
```
▼ /Volumes/ziprecruiter/monorepo    ●3 ○1  65.7k tok  3.3k/min
  ▶ finance/partnerships            ●1     5.1k tok   1.3k/min
```

- `▶`/`▼` glyph in the cursor mark column.
- Indentation: `depth * 2` spaces prepended to `DisplayPath`.
- Rollup respects `CostMode` toggle.
- Burn rate column shows `BurnRateSum` (sum across all descendant sessions).

### Modified: `internal/tui/model.go`

New fields:
```go
treeState  *treestate.State
pathNodes  []*aggregate.PathNode  // rebuilt on each tree update
flatRows   []render.Row           // cached; rebuilt when tree or state changes
```

**Cursor becomes row-based**: `cursor int` now indexes into `flatRows` (all
visible rows — dir nodes and sessions). Previously it indexed sessions only.

- `clampCursor()` uses `len(m.flatRows)`.
- `sessionAt` → replaced by `rowAt(idx int) render.Row` returning
  `m.flatRows[idx]`; callers that need a session check `row.Kind == SessionKind`
  before proceeding.
- `flatRows` is rebuilt in `rebuildFlatRows()` called after any tree update or
  state toggle.

`treeState` is loaded at startup via `treestate.Load(cacheDir)`.

### Modified: `internal/tui/update.go`

**New key handlers**:
```
Space         PathNodeKind row → treeState.Toggle(row.NodePath); rebuildFlatRows; save
Left / h      PathNodeKind row → collapse if expanded
Right / l     PathNodeKind row → expand if collapsed
Space         SessionKind row  → no-op
Enter         SessionKind row  → open detail (unchanged)
Enter         PathNodeKind row → no-op (explicit kind guard, no error)
```

`j`/`k`/`Down`/`Up` navigate all visible rows (dir nodes + sessions).

## Non-Goals

- No keyboard shortcut to collapse/expand all at once (can be added later).
- No persistence of scroll position across restarts.
- No animation for expand/collapse transitions.
