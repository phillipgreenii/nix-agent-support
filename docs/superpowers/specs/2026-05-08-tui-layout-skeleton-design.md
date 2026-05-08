# TUI Layout Skeleton + Drop-Priority + Bug Fixes (Theme A)

**Status:** Draft
**Date:** 2026-05-08
**Scope:** `packages/claude-agents-tui/internal/tui/view.go` and adjacent renderers

## Context

The TUI's `View()` currently composes header + body + legend with `strings.Join` and computes the body's row budget from `visualLineCount(header, width)`. Because `visualLineCount` counts terminal-wrap rows but the actual emit path uses raw newline counting, the two disagree by ±1 when the header contains a wrapped line (Updated timestamp, exhaust calculation, top-up, etc.). The disagreement causes:

- The top body row is cut off and reappears as the user drags the window edge by single rows. (Reported by user 2026-05-08.)
- The footer is intermittently absent when the body uses one too many rows.

Cursor navigation (`j` / `Down`) advances past the last *selectable* row because `clampCursor` clamps against `len(m.flatRows)`, which includes blank separator rows the cursor should skip. (Reported by user 2026-05-08.)

This design replaces the ad-hoc layout math with an explicit zone-based skeleton: each zone has a known natural height, the body fills the remainder, and the terminal-too-short case degrades by a defined drop priority.

This is the first of five planned spec/plan cycles (themes A–E). Themes B–E build on the skeleton landed here:

- B — Header redesign (single-row 5h block, controls compaction, Updated→corner)
- C — Tree row alignment (sessions and rollups share the same column grid)
- D — Selection panel (footer slot shows first prompt + Updated clock)
- E — Modal overlays (`[l]` legend, `[?]` help)

Themes B–E are out of scope for this document.

## The contract

> Every line returned from `(*Model).View()` is split on `\n`, and each line's ANSI-aware visible width is `<= effectiveWidth(m.width)`. The total number of `\n`-separated lines is exactly `m.height` whenever `m.height > 0` and `m.width > 0`.

Width invariant is already enforced by `wrap.Block` in `view.go:67`. Height invariant is new in this theme.

## Zone model

Five zones, top-to-bottom:

| Zone | Natural height | Drop priority (1 = drops first) |
|------|----------------|---------------------------------|
| Controls | 1 row | 1 |
| 5h Block | 1 row | 2 |
| Alert | 0 rows when no alert active; 1 row when ≥ 1 alert active | 3 |
| Body (sessions) | fills remainder | — (never drops) |
| Status (footer) | 1 row | 4 |

"Alert" is a single composed line that pipe-joins all currently active alert segments (`⏸ resuming…`, `⚠ rate-limit…`, `Top-up …`). Composition lives behind a tier-aware function in theme A's scope only insofar as the row exists; the actual content shape is current behavior copied as-is and is reworked in later themes if needed.

Drop algorithm (pseudocode, deterministic):

```
desired := [Controls, 5hBlock, Alert?, Body, Status]   // Alert? present iff alerts active
nonBody := desired \ {Body}
sortedByDropPriorityAsc := [Controls, 5hBlock, Alert, Status]   // Body never appears here

for h <- m.height; sum(z.height for z in nonBody) >= h; {
    drop the next zone in sortedByDropPriorityAsc that is still in nonBody
}
bodyHeight := h - sum(z.height for z in nonBody), clamped >= 0
```

Examples (no alert active):

| `m.height` | Zones rendered | Body height |
|------------|----------------|-------------|
| ≥ 4 | Controls, 5hBlock, Body, Status | h − 3 |
| 3 | 5hBlock, Body, Status | 1 |
| 2 | Body, Status | 1 |
| 1 | Body | 1 |
| 0 | none (test/headless mode — no body cap) | n/a |

Examples (alert active):

| `m.height` | Zones rendered | Body height |
|------------|----------------|-------------|
| ≥ 5 | Controls, 5hBlock, Alert, Body, Status | h − 4 |
| 4 | 5hBlock, Alert, Body, Status | 1 |
| 3 | Alert, Body, Status | 1 |
| 2 | Body, Status | 1 |
| 1 | Body | 1 |

The "Body never drops" invariant means at `h = 1` the user sees only the session list (truncated to one row). At `h = 0` the View defers to test/headless behavior (no row cap).

## Architecture

### Zone abstraction

Introduce a small layout helper, scoped to package `tui`:

```go
// zone is one logical row group in the View.
type zone struct {
    name       string                  // "controls", "block", "alert", "body", "status"
    minHeight  int                     // natural height; 0 disables (alert when no alerts)
    fill       bool                    // true means: take whatever h is left
    dropOrder  int                     // 1 = drops first; 0 reserved for body (never drops)
    render     func(width, height int) string
}

// layoutZones decides each zone's allocated height and renders top-to-bottom.
func layoutZones(zones []zone, width, height int) string
```

`layoutZones`:

1. Filter `zones` to those with `minHeight > 0` or `fill`.
2. Compute the body's allocated height by subtracting other zones' `minHeight` from `height` and dropping zones in `dropOrder` ascending until `bodyHeight >= 1` (or until only the body remains, in which case `bodyHeight = max(height, 1)`).
3. For each surviving zone (in source order), call `render(width, allocatedHeight)`. A zone whose render produces fewer rows is bottom-padded with empty lines; more rows are clipped via `wrap.Block` (existing) plus a row-count truncation.
4. Concatenate with `\n`.

The function returns a string with exactly `height` `\n`-separated lines whenever `height > 0`.

### `View()` rewrite

```go
func (m *Model) View() string {
    if m.width == 0 { return "loading…" }
    if m.tree == nil { return "loading…" }
    if m.selected != nil {
        return wrap.Block(RenderDetails(m.selected, m.width), wrap.EffectiveWidth(m.width))
    }

    alerts := composeAlerts(m)              // "" when none
    zones := []zone{
        {name: "controls", minHeight: 1, dropOrder: 1, render: m.renderControls},
        {name: "block",    minHeight: 1, dropOrder: 2, render: m.render5hBlock},
        {name: "alert",    minHeight: boolToInt(alerts != ""), dropOrder: 3, render: func(w, _ int) string { return wrap.Line(alerts, w) }},
        {name: "body",     fill: true,   dropOrder: 0, render: m.renderBody},
        {name: "status",   minHeight: 1, dropOrder: 4, render: m.renderStatus},
    }
    if m.height == 0 {
        return wrap.Block(m.renderUncappedFallback(), wrap.EffectiveWidth(m.width))
    }
    out := layoutZones(zones, m.width, m.height)
    return wrap.Block(out, wrap.EffectiveWidth(m.width))
}
```

`renderControls`, `render5hBlock`, `renderStatus` start as **trivial wrappers around today's `render.Header(...)` output** sliced into the right line ranges. They become real functions in theme B / D. `renderBody` calls `render.RenderWindowTree(..., bodyHeight, ...)` exactly as today; the only change is `bodyHeight` is now correct.

`renderUncappedFallback` preserves today's `m.height == 0` behavior (used by some tests).

### `visualLineCount` removal

The function and its 1 call site in `View()` are deleted. Tests that imported it (none currently — confirmed via `grep`) are unaffected.

### Cursor overrun fix

`render.RenderWindowTree` (window.go:174, 184) treats `opts.Cursor` as an index into `flatRows` — i.e., the highlighted row is `flatRows[opts.Cursor]`. The bug is that `keyDown` advances `m.cursor` by 1 unconditionally, so the cursor can park on a `BlankKind` separator row (which renders as a blank line with no visible marker), giving the user the impression that "the cursor went past the last session".

Fix keeps the cursor index space as `flatRows` index (so the renderer is unchanged) and adds a `nextSelectable` helper that skips non-selectable rows during navigation:

```go
// selectable returns true for rows the cursor is allowed to land on.
func selectable(r render.Row) bool { return r.Kind != render.BlankKind }

// nextSelectable returns the index of the next selectable row at or after
// from in the given direction (+1 or -1). Returns from when no selectable
// row exists in that direction (caller decides what to do — typically clamp).
func nextSelectable(rows []render.Row, from, dir int) int {
    for i := from; i >= 0 && i < len(rows); i += dir {
        if selectable(rows[i]) { return i }
    }
    return from
}
```

Key handlers:

- `keyDown`: `m.cursor = nextSelectable(rows, m.cursor + 1, +1)` — if no selectable row exists below, cursor stays put (so press past last selectable is a no-op, not an overrun).
- `keyUp`: `m.cursor = nextSelectable(rows, m.cursor - 1, -1)` — symmetric.
- `keyG` (top): `m.cursor = nextSelectable(rows, 0, +1)`.
- `keyShiftG` (bottom): `m.cursor = nextSelectable(rows, len(rows) - 1, -1)`.

`clampCursor` is updated to snap to the nearest selectable row when the model state changes (poll result, collapse toggle):

```go
func (m *Model) clampCursor() {
    n := len(m.flatRows)
    if n == 0 { m.cursor = 0; return }
    if m.cursor >= n { m.cursor = n - 1 }
    if m.cursor < 0  { m.cursor = 0 }
    if !selectable(m.flatRows[m.cursor]) {
        m.cursor = nextSelectable(m.flatRows, m.cursor, -1)
        if !selectable(m.flatRows[m.cursor]) {
            m.cursor = nextSelectable(m.flatRows, m.cursor, +1)
        }
    }
}
```

Renderer code in `internal/render/window.go` is unchanged: `i == opts.Cursor` still highlights the right row because `m.cursor` is always a selectable `flatRows` index.

## Test plan

### Unit — layout zones (`internal/tui/layout_test.go`, new)

- `layoutZones` returns exactly `height` lines for `height ∈ {1, 4, 5, 30, 100}`.
- Drop priority: at `height = 3` (no alert) only `5hBlock + Body + Status` render; controls absent. At `height = 2` only `Body + Status`. Etc.
- With alert: at `height = 4` only `5hBlock + Alert + Body + Status`. At `height = 3` only `Alert + Body + Status`.
- Body is always present (any `height >= 1`).
- A zone whose `render` returns fewer lines is padded; a zone returning more lines is truncated.

### Unit — cursor clamping (`internal/tui/model_test.go`, additions)

- Tree with N selectable rows: pressing `Down` more than N times leaves `m.cursor == N-1`.
- Tree with mixed selectable + blank rows: `m.cursor` after navigation never lands on a blank row.

### Integration — view invariants (`internal/tui/view_test.go`, extend existing)

Extend the existing parameterized `TestViewLineWidthInvariant` to also assert **line count == height**:

```go
got := strings.Count(out, "\n") + 1
if w > 0 && h > 0 && got != h {
    t.Errorf("line count = %d, want %d (fixture=%q, w=%d, h=%d)", got, h, fx.name, w, h)
}
```

Run across `widths × heights × fixtures` where `heights = {1, 2, 3, 4, 5, 10, 30, 100}`.

### Integration — top-row cut-off regression

Specific test that drove the bug: long Updated timestamp + tall enough body that the today's miscount exposed. Resize from h=H to h=H-1 to h=H, assert the top body row is present in all three frames. Achieved via direct `Update(tea.WindowSizeMsg{...})` calls.

## Migration / rollout

Single commit per zone abstraction is tempting but risky — the zone refactor and the cursor fix are independent. Two commits:

1. `feat(tui): layout zones — fixed-height controls/5h-block/alert/status` — introduces zone system, removes `visualLineCount`, adds layout tests + view-invariant extension. Body content unchanged.
2. `fix(tui): clamp cursor to selectable rows` — cursor bug fix + tests.

Both can land in either order; neither changes existing renderer outputs.

## Out of scope (theme B–E)

- Tier-aware compaction of controls / 5h block / alert content.
- Burn rate, Updated clock placement.
- Selection status content (first prompt, dim style).
- `[l]` legend modal, `[?]` help modal.
- Indented collapse arrows.
- Sessions/rollups column alignment.

These each get their own design doc.
