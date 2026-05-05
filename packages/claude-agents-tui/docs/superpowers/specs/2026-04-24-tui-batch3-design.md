# claude-agents-tui: Batch 3 — Cursor, Colors, Toggles, Housekeeping

**Date:** 2026-04-24

## Summary

Six targeted fixes: transcript disambiguation for multi-session directories, cursor visibility in the tree, Stylix-compatible color system with graceful degradation, toggle state indicators in the header, and legend cleanup (remove dead `w` key and duplicate `q`).

---

## 1. Transcript disambiguation — bug: all sessions in a directory show same data

**Problem:** `ResolveTranscript` falls back to the newest transcript in the directory when `Session.Name` is empty. When multiple unnamed sessions share the same `Cwd`, all receive the same transcript path, so their model, prompt, context %, and burn rate are identical.

**Root cause:** `internal/session/transcript.go` line ~70: `return cands[0], ...` — the "newest fallback" runs before any session-specific match attempt.

**Solution:** Between the title-match block and the newest-fallback, insert an exact `SessionID` match:

```go
// Try exact SessionID match before generic newest-fallback.
for _, c := range cands {
    if filepath.Base(c.path) == s.SessionID+".jsonl" {
        return c.path, c.mtime, true
    }
}
// Fallback: newest transcript in directory.
return cands[0].path, cands[0].mtime, true
```

**Trade-off:** A resumed session whose original `.jsonl` still exists on disk will resolve to the stale original rather than the newer post-resume file. Accepted: (a) resumed sessions normally have a `Name` set (caught by title-match first), and (b) multi-session-same-directory showing unique stale data is better than all showing the same newest data.

**Files:** `internal/session/transcript.go`

---

## 2. Cursor visibility — bug: no visual indicator of selected session

**Problem:** `model.cursor` is tracked in `tui/model.go` but never passed to the render layer. `render.TreeOpts` has no cursor field, so the selected row looks identical to all others.

**Solution:**

### 2a. `TreeOpts` extension

Add two fields to `render.TreeOpts`:

```go
type TreeOpts struct {
    ShowAll   bool
    ForceID   bool
    CostMode  bool
    Width     int
    Cursor    int  // flat 0-based index of highlighted row across all visible sessions
    HasCursor bool // false when detail view is open (cursor not shown)
}
```

### 2b. Prefix column reservation

Always render a 2-char cursor prefix before the tree-branch glyph — `"> "` for the selected row, `"  "` otherwise. This means columns never shift when the cursor moves.

Update `prefixCols` constant from 3 to 5 to account for the added 2 chars.

Example:

```
/path/to/dir                          [rollup]
  ├─ ● session1   sonnet  12%  ░░░   0k/m
> ├─ ○ session2   opus     5%  ░░░   0k/m
  └─ ✕ session3   haiku    0%  ░░░   0k/m
```

### 2c. `view.go` wiring

Pass cursor through when in tree view:

```go
body := render.Tree(m.tree, render.TreeOpts{
    ShowAll:   m.showAll,
    ForceID:   m.forceID,
    CostMode:  m.costMode,
    Width:     m.width,
    Cursor:    m.cursor,
    HasCursor: m.selected == nil,
})
```

### 2d. Render logic

In `render/tree.go` `Tree()`, maintain a `rowIdx int` counter that increments for each visible session row. In `renderSession`, accept a `selected bool` param; when true, prefix `"> "` instead of `"  "`.

**Files:** `internal/render/tree.go`, `internal/tui/view.go`

---

## 3. Color system — Stylix-compatible, graceful degradation

### 3a. Detection

New function `render.DetectColors() bool`:

```go
import "github.com/charmbracelet/colorprofile"

func DetectColors() bool {
    p := colorprofile.Detect(os.Stdout, os.Environ())
    return p != colorprofile.NoTTY && p != colorprofile.Ascii
}
```

Covers: `NO_COLOR` env var, `TERM=dumb`, non-TTY stdout. Already available — `charmbracelet/colorprofile` is an indirect dep via lipgloss v1.1.

### 3b. Theme struct

New file `internal/render/theme.go`:

```go
type Theme struct {
    Working      lipgloss.Style // ● status
    Idle         lipgloss.Style // ○ status
    Awaiting     lipgloss.Style // ? status
    Dormant      lipgloss.Style // ✕ status
    Cursor       lipgloss.Style // > marker
    DirRow       lipgloss.Style // directory path line
    Branch       lipgloss.Style // [branch-name]
    Prompt       lipgloss.Style // ↳ "first prompt"
    ActiveToggle lipgloss.Style // underline on current toggle selection
}

func NewTheme(hasColors bool) Theme
```

Color assignments use terminal palette indices (0–15). Stylix maps these via base16 at the terminal level, so theming is automatic without any app-side config:

| Style        | With colors               | Without colors    |
| ------------ | ------------------------- | ----------------- |
| Working      | `Color("2")` green        | plain             |
| Idle         | `Color("3")` yellow       | plain             |
| Awaiting     | `Color("5")` magenta      | plain             |
| Dormant      | `Color("8")` bright-black | `Faint(true)`     |
| Cursor `>`   | `Bold(true)`              | `Bold(true)`      |
| Dir row      | `Bold(true)`              | `Bold(true)`      |
| Branch       | `Color("6")` cyan         | `Faint(true)`     |
| Prompt `↳`   | `Color("8")` bright-black | `Faint(true)`     |
| ActiveToggle | `Underline(true)`         | `Underline(true)` |

Note: Cursor, DirRow, and ActiveToggle use bold/underline only — these work on all terminals including `TERM=dumb`. Color styles are additive enhancements.

### 3c. Theme propagation

`Theme` is constructed once inside `tui.NewModel` via `render.NewTheme(render.DetectColors())` and stored on `Model`. It is passed into `TreeOpts` and `HeaderOpts` on each render call. `main.go` does not need to know about the theme. The headless render path (`headless/headless.go`) uses `render.TreeOpts{}` with zero-value `Theme`, which renders all styles as plain text — correct for headless use.

```go
type TreeOpts struct {
    ...
    Theme Theme
}

type HeaderOpts struct {
    ...
    Theme Theme
}
```

Render functions apply styles from the theme. The existing inline `lipgloss.NewStyle()` calls for `styleModel`, `stylePct`, etc. (which are layout styles, not color styles) remain as-is.

**Files:** `internal/render/theme.go` (new), `internal/render/tree.go`, `internal/render/header.go`, `internal/tui/view.go`

---

## 4. Toggle indicators — bug: no visual feedback on current selection

**Problem:** The header line `[t] tokens · cost  |  [a] active · all  |  [n] name · id` shows toggle options with no indication of which is active.

**Solution:** Apply `theme.ActiveToggle` (underline) to the currently selected option. Works with and without colors.

### 4a. `HeaderOpts` extension

```go
type HeaderOpts struct {
    CaffeinateOn   bool
    GraceRemaining time.Duration
    TopupPoolUSD   float64
    TopupConsumed  float64
    Now            time.Time
    ShowAll        bool // for toggle indicator
    CostMode       bool // for toggle indicator
    ForceID        bool // for toggle indicator
    Theme          Theme
}
```

### 4b. Header render

Replace the static toggle string with dynamic rendering:

```
[t] <active: tokens> · <inactive: cost>  |  [a] <active: active> · <inactive: all>  |  [n] <active: name> · <inactive: id>
```

Where "active" means `theme.ActiveToggle.Render(text)` and "inactive" means plain text.

### 4c. `view.go` wiring

```go
header := render.Header(m.tree, render.HeaderOpts{
    CaffeinateOn: m.caffeinateOn,
    ShowAll:      m.showAll,
    CostMode:     m.costMode,
    ForceID:      m.forceID,
    Theme:        m.theme,
})
```

`m.theme` is stored on `Model` (set in `NewModel`).

**Files:** `internal/render/header.go`, `internal/tui/model.go`, `internal/tui/view.go`

---

## 5 & 6. Legend cleanup

**Problem 5:** `[q] quit` appears in both the header line and the bottom legend — redundant.

**Problem 6:** `[w] wait` is in the bottom legend but has no key handler in `update.go`. Pressing `w` does nothing.

**Solution:** Replace the bottom legend string in `internal/tui/view.go`:

Before:

```
● working  ○ idle  ? awaiting  ✕ dormant   🤖 subagents  🐚 shells  🌿 branch       [↑↓] nav [enter] details [w] wait [q] quit
```

After:

```
● working  ○ idle  ? awaiting  ✕ dormant   🤖 subagents  🐚 shells  🌿 branch       [↑↓] nav  [enter] details
```

**Files:** `internal/tui/view.go`

---

## Affected files

| File                                  | Changes                                                                                 |
| ------------------------------------- | --------------------------------------------------------------------------------------- |
| `internal/session/transcript.go`      | SessionID exact match before newest-fallback                                            |
| `internal/render/theme.go`            | New: `Theme` struct, `NewTheme()`, `DetectColors()`                                     |
| `internal/render/tree.go`             | `Theme` in `TreeOpts`, cursor prefix, color on symbols/branch/prompt, `prefixCols` += 2 |
| `internal/render/header.go`           | `Theme`+toggle fields in `HeaderOpts`, underline active toggle                          |
| `internal/tui/model.go`               | Add `theme render.Theme` field; init in `NewModel`                                      |
| `internal/tui/view.go`                | Pass cursor, theme, toggle states; update legend                                        |
| `internal/session/transcript_test.go` | Test SessionID match for multi-session directory                                        |
| `internal/render/tree_test.go`        | Test cursor prefix output                                                               |
