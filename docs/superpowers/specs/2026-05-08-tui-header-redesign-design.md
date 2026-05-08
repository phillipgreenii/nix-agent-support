# TUI Header Redesign (Theme B)

**Status:** Draft
**Date:** 2026-05-08
**Scope:** `packages/claude-agents-tui/internal/render/header.go`, plus new `controls.go`, `block_row.go`, `alerts.go`, `footer.go`, plus `internal/tui/view.go` and `internal/headless/headless.go`

## Context

Today's header is multi-line (controls, auto-resume countdown, 5h block, exhaust sub-line, top-up, burn rate, Updated, optional resuming countdown). The lines vary in count depending on state — exactly the disagreement that drove theme A's row-cut-off bug.

Theme A landed the zone-based skeleton: header zone, body zone, status zone, optional alert zone. Today the header zone holds the entire multi-line `render.Header(...)` output. Theme B splits that into single-row renderers, one per logical row, each tier-aware, so the header zone always contributes exactly two rows (controls + block) plus an optional alert row.

User's stated goals (2026-05-08 brainstorm):

- Top controls always 1 row.
- 5h block always 1 row, prefix `5h` not `5h Block`. Drop priority (most-droppable first): burn → bar → cost → exhaust → reset.
- Move `Updated HH:MM:SS` to bottom-right corner.
- Add `[?]` help affordance to controls (modal arrives in theme E).
- Top-up display moves to alert row.

## The contract

Each renderer in this theme returns exactly **1 line** of output (no trailing `\n`) with `lipgloss.Width(line) <= effectiveWidth` at every tier. Pre-active states (`!CCUsageProbed`, `CCUsageErr != nil`, `ActiveBlock == nil`) keep BlockRow at 1 line.

Theme A's zone height invariant continues to hold: View() returns exactly `m.height` lines.

## Architecture

### Five render packages, four single-row functions

```
internal/render/
├── controls.go        -- new: Controls(opts) string  -- 1 row, tier-aware
├── controls_test.go   -- new
├── block_row.go       -- new: BlockRow(tree, opts) string  -- 1 row, tier-aware
├── block_row_test.go  -- new
├── alerts.go          -- new: Alerts(tree, opts) string  -- "" when none, else pipe-joined
├── alerts_test.go     -- new
├── footer.go          -- new: Footer(width, updatedAt time.Time) string  -- 1 row, left=legend, right=Updated
├── footer_test.go     -- new
├── header.go          -- DELETED at end of theme B
├── header_test.go     -- DELETED
├── header_tier_test.go-- DELETED (subsumed by controls_test.go + block_row_test.go)
├── legend.go          -- unchanged (consumed by Footer)
└── legend_test.go     -- unchanged
```

### Caller changes

```
internal/tui/view.go      -- replaces render.Header() + render.Legend() with
                             Controls / BlockRow / Alerts / Footer; zones list
                             becomes [controls, block, alert?, body, footer].
internal/headless/headless.go -- swaps render.Header() for explicit Controls()
                                 + BlockRow() + Alerts() + render.Tree() chain.
```

### Renderer contracts

**Controls** — `func Controls(opts ControlsOpts) string`
- Single row, no trailing newline.
- `ControlsOpts`: `CaffeinateOn`, `GraceRemaining` (caffeinate countdown if any), `ShowAll`, `CostMode`, `ForceID`, `AutoResume`, `Theme`, `Width`.
- `[?]` literal placed just before `[q]` at every tier.
- Tier shape (active toggle highlighted by `Theme.ActiveToggle.Render`):
  - **WIDE ≥120**: `[C] ● on  [t] tokens · cost  [a] active · all  [n] name · id  [R] ● on  [M] now  [?]  [q]`
  - **NARROW 80–119**: `[C]●  [t] tok · cost  [a] act · all  [n] nm · id  [R]●  [M]now  [?][q]`
  - **TINY <80**: `[C]●  [t]tok  [a]act  [n]nm  [R]●  [M]now  [?][q]` (only the active variant of each pair shows)
- Caffeinate grace countdown shown after `●` at WIDE+NARROW (e.g., `[C] ● on 55s`); dropped at TINY.

**BlockRow** — `func BlockRow(tree *aggregate.Tree, opts BlockRowOpts) string`
- Single row, no trailing newline.
- `BlockRowOpts`: `Now time.Time`, `Width int`.
- Pre-active states (independent of tier): `5h loading…` / `5h unavailable — \`ccusage\` not on PATH` / `5h no active block` / `5h $X.XX  resets HH:MM  (plan cap unknown)` (unknown plan cap fallback).
- Active-block tier shape (drop in this priority order: burn → bar → cost → exhaust → reset):
  - **WIDE**: `5h ████████░░░░░░░░░░ 35%  $30.10  1.2M/m  resets 01:00  ex 22:21 ⚠` (⚠ when projected exhaust before window end)
  - **NARROW**: `5h ████████░░░░░░░░░░ 35%  $30.10  resets 01:00  ex 22:21 ⚠`
  - **TINY**: `5h 35%  resets 01:00`
- Bar width: 18 cols at WIDE and NARROW; bar dropped entirely at TINY. (Today's bar is 30 cols inside `progressBar(pct, 30)`; theme B reduces to 18 to fit the single-row budget.)

**Alerts** — `func Alerts(tree *aggregate.Tree, opts AlertsOpts) string`
- Returns `""` when no alert active. Otherwise single row, no trailing newline.
- `AlertsOpts`: `Now time.Time`, `Width int`, `AutoResume bool`, `WindowResetsAt time.Time`, `AutoResumeDelay time.Duration`, `TopupPoolUSD float64`, `TopupConsumed float64`.
- Active alerts (in priority order, pipe-joined when multiple):
  1. `⚠ rate-limit until HH:MM` — when any session has `RateLimitResetsAt > Now`. (Today this isn't a separate alert in `header.go`; the rate-limited symbol shows on the session row. We keep that. The alert here is about the global window — unchanged from today's "no special header alert" behavior. SKIP this alert in commit B1; it's currently not wired and adding it is out of scope. Reserve the slot.)
  2. `⏸ resuming in N:NN` — when `AutoResume && WindowResetsAt > Now` (active countdown). Replaces today's `header.go:117-127` block.
  3. `Top-up $X / $Y remaining` — when `tree.TopupShouldDisplay() && opts.TopupPoolUSD > 0`. Replaces today's `header.go:102-110` block.
- Tier compaction: NARROW shortens labels; TINY uses glyphs only (`⏸ N:NN` / `T $X/$Y`).

Effective alerts in commit B1: just `⏸ resuming in N:NN` and `Top-up …`. `⚠ rate-limit` slot is documented but not wired (today's code doesn't wire it either; carry the gap forward).

**Footer** — `func Footer(width int, updatedAt time.Time) string`
- Single row, no trailing newline. Two columns: legend (left) and Updated clock (right).
- Width budget:
  - **WIDE** (≥120): right column is `Updated 21:05:43` = 16 cols. `legendWidth = width - 16 - 2 (gap)`.
  - **NARROW** (80–119): right column is `21:05:43` = 8 cols. `legendWidth = width - 8 - 2 (gap)`.
  - **TINY** (<80): right column is empty. `legendWidth = width`.
- Tier behavior:
  - **WIDE**: `<legend left-aligned, padded to legendWidth>  Updated 21:05:43`
  - **NARROW**: `<legend left-aligned, padded to legendWidth>  21:05:43`
  - **TINY**: `<legend, full width>` (no Updated)
- Internally calls `Legend(legendWidth)` so the existing tier-aware legend keeps working.
- Updated rendered with `lipgloss.NewStyle().Width(updatedWidth).Align(lipgloss.Right).Render(...)` so right-edge alignment stays exact.

### `view.go` zone list after theme B

```go
controls := render.Controls(...)
blockRow := render.BlockRow(m.tree, ...)
alerts   := render.Alerts(m.tree, ...)            // "" when none
footer   := render.Footer(m.width, time.Now())

zones := []zoneSpec{
    {name: "controls", content: controls, dropOrder: 1},
    {name: "block",    content: blockRow, dropOrder: 2},
}
if alerts != "" {
    zones = append(zones, zoneSpec{name: "alert", content: alerts, dropOrder: 3})
}
zones = append(zones, []zoneSpec{
    {name: "body",   fill: true, renderFill: m.renderBody},
    {name: "footer", content: footer, dropOrder: 4},
}...)
```

This matches theme A's drop-priority spec: controls (1) drops first, then block (2), then alert (3), then footer (4); body never drops.

### Headless rewrite

`internal/headless/headless.go:35` currently calls `render.Header(tree, render.HeaderOpts{})`. After theme B:

```go
fmt.Fprint(o.Writer, render.Controls(render.ControlsOpts{}))
fmt.Fprint(o.Writer, "\n")
fmt.Fprint(o.Writer, render.BlockRow(tree, render.BlockRowOpts{Now: time.Now()}))
fmt.Fprint(o.Writer, "\n")
if a := render.Alerts(tree, render.AlertsOpts{Now: time.Now()}); a != "" {
    fmt.Fprint(o.Writer, a)
    fmt.Fprint(o.Writer, "\n")
}
fmt.Fprint(o.Writer, render.Tree(tree, render.TreeOpts{}))
```

Headless tests don't check header substrings (verified via grep in theme A); this is a mechanical swap.

## Migration / commits

Three commits, each independently revertable:

1. **`feat(render): tier-aware Controls/BlockRow/Alerts renderers`**
   - Adds the four new files (controls.go, block_row.go, alerts.go, footer.go) with their tests. No callers yet. Old `Header` still in place.
   - Each renderer's tests cover all three tiers and the relevant pre-active states.
   - `wrap.Tier` reused; `wrap.Line` reused for any post-render safety clip.

2. **`feat(tui,headless): wire View() and headless to new renderers`**
   - `view.go`'s zone list becomes 5-zone (controls, block, alert?, body, footer).
   - `headless.go` swaps `render.Header` for the new chain.
   - `render/header.go`, `header_test.go`, `header_tier_test.go` deleted.
   - `view_test.go`'s `TestViewLineWidthInvariant` continues to pass with the same matrix; the line-count==height invariant still holds because each renderer is exactly 1 line.

3. **`feat(render): footer right-aligns Updated clock`**
   - Adds `Footer(width, updatedAt)`. View() switches from passing `render.Legend(width)` directly into the footer zone to calling `render.Footer(m.width, time.Now())`.
   - Footer reserves the right column for Updated; theme D will fill the left half with selection status, theme E will remove the legend.

If commit 2 is too noisy (large diff), it can be split into 2a (View() rewire) and 2b (headless rewire); current plan keeps them together since they share the `header.go` deletion.

## Test plan

### Per-renderer unit tests

Each new renderer has a `_tier_test` table with three rows (WIDE/NARROW/TINY) asserting:

- Output is exactly 1 line (`strings.Count(out, "\n") == 0`).
- `lipgloss.Width(out) <= tierFloor` (60 for TINY, 80 for NARROW, 120 for WIDE).
- Required substrings appear (`[?]`, `[q]`, `5h`, `35%`, `resets 01:00`, etc.).
- Forbidden substrings absent at narrower tiers (`tokens` at TINY, etc.).

Active toggle highlight verified by checking the Theme.ActiveToggle ANSI prefix is present around the active label.

### Active-block tier matrix (BlockRow)

Table-driven test across `(width, blockState) ∈ {WIDE, NARROW, TINY} × {pre-active, no-block, active+normal, active+exhausting}`. Each cell asserts the expected substrings and the no-newline / width invariants.

### Alerts composition

- Empty when no alert active.
- Single segment when only one of `{auto-resume, top-up}` is active.
- Pipe-joined when both active.
- Pre-active state (`tree.ActiveBlock == nil`) hides Top-up regardless of opts.

### Footer

- Renders `Updated HH:MM:SS` right-aligned at WIDE+NARROW.
- Drops Updated at TINY.
- Total width `lipgloss.Width(out) == width` at every tier.
- Legend left half still tier-correct.

### Integration (theme A invariants extend)

`internal/tui/view_test.go`'s `TestViewLineWidthInvariant` passes unchanged. `TestViewNoPhantomBlankRowsBetweenZones` passes unchanged. Add a new test asserting the controls + block lines fit at every tier floor.

### Headless smoke

`internal/headless/headless_test.go` (existing; check it grep'd for "tokens" or other lost-at-tier strings — verified absent in theme A). Should pass without modification.

## Out of scope (theme C–E)

- **Theme C**: tree row column alignment (sessions vs rollups share the same model/%/bar/tokens/burn grid).
- **Theme D**: selection-status content in the footer left side; first-prompt removal from session rows.
- **Theme E**: `[?]` help modal, `[l]` legend modal, indented collapse arrows, `[esc]` modal dismiss, modal vertical scroll. Theme B only adds the `[?]` literal in the controls row — pressing it in theme B is a no-op until theme E lands the modal infrastructure.

## Followups noted from theme A review

- **Empty-content non-fill zone** still produces a phantom blank from `concatZones`. The alert zone in theme B is the first place this could trigger (`Alerts(...) == ""`). View() guards by only adding the alert zone when its content is non-empty (see `view.go` snippet above), so the bug is avoided at the call site. A future hardening of `concatZones` to filter empty non-fill zones is still recommended but not part of theme B.
- **Drop-order policy**: theme B adds two more drop slots (block at 2, alert at 3, footer at 4); the user-facing impact will surface with theme D's selection content. No change needed yet.
