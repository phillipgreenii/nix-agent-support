# Bounded Session List with Sticky Dir Headers — Design

**Status**: Accepted
**Date**: 2026-04-27

## Problem

When many sessions or directories are active, `View()` renders an unbounded body
string that overflows the terminal. The `viewport.Model` from
`charmbracelet/bubbles` was imported and initialized but never wired into
`View()`, so it has no effect. The fix must constrain the body to the terminal
height, scroll to keep the cursor visible, pin directory headers when their
sessions scroll into view, and show indicators when content is clipped.

## Architecture

The body-render pipeline gains a typed row layer between tree data and terminal
output:

```
tree.Dirs → FlattenRows() → []Row → window slice → render each Row → body string
```

`viewport.Model` (and the `charmbracelet/bubbles` dependency) is removed
entirely.

## Row Model

New file `internal/render/rows.go`:

```go
type RowKind int
const (DirHeaderKind RowKind = iota; SessionKind)

type Row struct {
    Kind      RowKind
    DirIdx    int  // index into tree.Dirs
    SessIdx   int  // index within dir's visible sessions (SessionKind only)
    LineCount int  // 1 for dir headers; 2 for sessions with FirstPrompt, else 1
}
```

`FlattenRows(tree *aggregate.Tree, opts TreeOpts) []Row` iterates non-empty
dirs, emitting one `DirHeaderKind` row then one `SessionKind` row per visible
session. `LineCount` for a session row is 2 when `FirstPrompt != ""`, else 1.

## Scroll State and Cursor Sync

`Model` drops `viewport viewport.Model`, gains `scrollOffset int`.

`bodyHeight` is computed dynamically in `View()`:

```
bodyHeight = m.height - strings.Count(header, "\n") - 1
```

(The legend is always 1 line. The header line count varies by block state, so
it is counted from the rendered string rather than hard-coded.)

After any cursor move, `syncScroll(rows []Row, bodyHeight int)` ensures the
cursor's row stays in the visible window:

- If cursor row index < `scrollOffset` → set `scrollOffset = cursorRowIdx`
- If cursor row index > last visible row → advance `scrollOffset` until the
  cursor row fits, accounting for each row's `LineCount`

`lastVisibleRow(rows, offset, budget int) int` sums `LineCount` from `offset`
forward until the budget is exhausted and returns the last row index that fits.

`scrollOffset` resets to 0 when the tree resets (no active block case).

## Sticky Dir Header

On each render:

1. Find the first `SessionKind` row in the visible window (starting at
   `scrollOffset`, possibly shifted by the top indicator).
2. If that session's parent `DirHeaderKind` row is **above** `scrollOffset`,
   render it first as a pinned header (costs 1 line from `bodyHeight`).
3. The natural dir header position is already off-screen; nothing is doubled.

When the dir header is already within the visible window (the common case), no
pinning occurs and rendering proceeds from `scrollOffset` normally.

## Scroll Indicators

**Top indicator** — shown when `scrollOffset > 0`:

```
  ↑ N sessions
```

Where N = count of `SessionKind` rows with index < `scrollOffset`. Costs 1 line
from `bodyHeight` (rendered before the row window).

**Bottom indicator** — shown when rows extend past the visible window:

```
  ↓ N sessions
```

Where N = count of `SessionKind` rows beyond the last visible row index. Costs 1
line from `bodyHeight` (rendered after the row window).

Both indicators use the existing `theme.Prompt` style (muted). If `bodyHeight`
is too small to show any rows (≤ 2 when both indicators would apply), the
indicators are suppressed and rows fill the space.

## Rendering Pipeline in `View()`

```
1. Render header string
2. Compute bodyHeight = m.height - strings.Count(header, "\n") - 1
3. If bodyHeight <= 0: return header + legend (degenerate terminal)
4. FlattenRows(m.tree, opts) → rows
5. Compute top indicator (scrollOffset > 0?)
6. Compute sticky dir header (first visible session's dir above window?)
7. Budget remaining lines for rows (bodyHeight - indicator lines - sticky lines)
8. Walk rows from scrollOffset, accumulate until budget exhausted
9. Compute bottom indicator (rows remaining after window?)
10. Join: header + "\n" + body + "\n" + legend
```

## File Changes

| File                           | Change                                                                                                                                                   |
| ------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/render/rows.go`      | **New** — `Row` type, `FlattenRows()`                                                                                                                    |
| `internal/render/rows_test.go` | **New** — flatten correctness, LineCount, empty dirs skipped                                                                                             |
| `internal/render/tree.go`      | Extract `renderDirRow` / `renderSession` as exported `RenderDirHeader` / `RenderSession`; `Tree()` becomes a thin wrapper (keeps existing tests passing) |
| `internal/render/tree_test.go` | Add window-render and sticky tests                                                                                                                       |
| `internal/tui/model.go`        | Remove `viewport viewport.Model` field + import; add `scrollOffset int`; remove `bubbles/viewport` dep                                                   |
| `internal/tui/update.go`       | Remove `m.viewport` update; call `m.syncScroll` after cursor moves; add `syncScroll` + `lastVisibleRow` helpers; remove `maxInt` (viewport-only)         |
| `internal/tui/view.go`         | New body pipeline per §Rendering Pipeline                                                                                                                |
| `internal/tui/model_test.go`   | Update: remove any viewport assertions                                                                                                                   |
| `go.mod` / `go.sum`            | Remove `github.com/charmbracelet/bubbles`                                                                                                                |
| `default.nix`                  | Update `vendorHash` after go.mod change                                                                                                                  |

## Out of Scope

- Horizontal scrolling (session rows already truncate via `labelStyle`)
- Page-up / page-down keys (cursor j/k movement + auto-scroll is sufficient)
- Details view scrolling (handled separately via existing `RenderDetails`)
