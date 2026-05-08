# TUI Tree Row Column Alignment + Indented Glyphs (Theme C)

**Status:** Draft
**Date:** 2026-05-08
**Scope:** `packages/claude-agents-tui/internal/aggregate/`, `packages/claude-agents-tui/internal/render/tree.go`

## Context

Today's tree body has two row shapes that don't share a column grid:

- **Session rows** (`renderSession`): `<label>  model(10)  pct(5)  bar(5)  burn(7)  amount(8)  tail`. Stats are right-aligned via per-column lipgloss styles.
- **Rollup rows** (`dirRollup`, `nodeRollup`): a single `strings.Join` blob like `3● · 1○ · 320.0k tok` (no fixed column widths, no alignment with sessions).

User report (2026-05-08): "the collapsed calculations aren't aligning with the session level values… all 'columns' should be aligned." The same row also calls out (a) the `tokens` column being blank in token mode, (b) the column order they want (`model | % | bar | tokens-or-cost | burn`, with `model` swapped for counts on rollups), and (c) the path-tree collapse glyphs all sitting at column 0 instead of indenting with depth.

Theme C ships:

1. Unified column grid for sessions and rollups.
2. Column reorder: `col1 | % | bar | tokens-or-cost | burn`.
3. `tokens-or-cost` column always populated (today's amount column is empty in token mode).
4. Rollup `%` and `bar` columns reflect aggregate share of total session tokens.
5. Indented `▼/▶` glyph on path-node rows.

## The contract

> Every tree row — whether session or rollup — emits exactly one main line whose stats columns sit at the same screen positions. Right-edge of every stat column is identical across row types within a frame.

## Architecture

### Aggregate state (commit C1)

`Directory.TotalTokens` and `PathNode.TotalTokens` already sum `SessionEnrichment.SessionTokens` across descendants — the field theme C needs for the rollup `%` calculation already exists. `PathNode.BurnRateSum` also already exists.

The single missing field is `Directory.BurnRateSum`:

```go
type Directory struct {
    // existing fields...
    BurnRateSum float64 // NEW: sum of SessionEnrichment.BurnRateShort across children
}
```

This is added so directory rollups can show a burn-rate column (today's `dirRollup` has no burn column at all; theme C adds it). Populated during `aggregate.Build` by accumulating `en.BurnRateShort` alongside the existing `d.TotalTokens += en.SessionTokens`.

The percentage shown on a rollup row is `100 * TotalTokens / TotalSessionTokens` (where `TotalSessionTokens` is the existing global sum already passed into `TreeOpts`).

### Column grid (commit C2)

Five right-aligned stat columns. The combined width of all five equals today's `statsBlockCols`.

```go
// internal/render/tree.go (new constants — replace today's statsBlockCols + per-column styles)

const (
    col1Width   = 10  // model name (sessions) or counts (rollups)
    colPctWidth = 5   // "100%"
    colBarWidth = 5   // 5-cell bar
    colAmtWidth = 10  // tokens (FmtTok) or "$cost.cc"
    colBurnWidth = 7  // "1.2M/m"
)

var (
    styleCol1  = lipgloss.NewStyle().Width(col1Width).Align(lipgloss.Right)
    stylePct   = lipgloss.NewStyle().Width(colPctWidth).Align(lipgloss.Right)
    styleBar   = lipgloss.NewStyle().Width(colBarWidth).Align(lipgloss.Right)
    styleAmt   = lipgloss.NewStyle().Width(colAmtWidth).Align(lipgloss.Right)
    styleBurn  = lipgloss.NewStyle().Width(colBurnWidth).Align(lipgloss.Right)
)

// statsBlockCols is the total width of the right-side stats block including
// the single space between each column.
const statsBlockCols = col1Width + 1 + colPctWidth + 1 + colBarWidth + 1 + colAmtWidth + 1 + colBurnWidth
```

Note `statsBlockCols` numerically: 10+1+5+1+5+1+10+1+7 = 41 (same as today by coincidence — today's `model(10)+space+pct(5)+space+bar(5)+space+burn(7)+space+amount(8)` = 41 too). The `prefixCols` and `minLabelWidth` constants are unchanged.

A small helper formats one stats block uniformly:

```go
// renderStatsBlock returns the five-column stats string for any row.
// All five values are pre-formatted strings; the helper applies the
// fixed-width right-alignment styling and joins with single spaces.
func renderStatsBlock(col1, pct, bar, amount, burn string) string {
    return fmt.Sprintf("%s %s %s %s %s",
        styleCol1.Render(col1),
        stylePct.Render(pct),
        styleBar.Render(bar),
        styleAmt.Render(amount),
        styleBurn.Render(burn),
    )
}
```

`renderSession` and `renderDirRow` and `RenderPathNode` all call `renderStatsBlock` so they share one source of truth for column layout.

### Per-row content rules

**Session row:**

- `col1` = `shortModel(s.SessionEnrichment.Model)` (today's behavior)
- `pct` = `fmt.Sprintf("%.0f%%", sessionSharePct(SessionTokens, TotalSessionTokens))`
- `bar` = `progressBar(pct, 5)`
- `amount` = `FmtTok(SessionTokens)` when `!CostMode`; `fmt.Sprintf("$%.2f", CostUSD)` when `CostMode`
- `burn` = `fmt.Sprintf("%sk/m", fmtK(BurnRateShort))`
- Tail (subagents/shells) appended **after** the stats block on the session row only.

**Directory rollup row (today's `dirRollup`):**

- `col1` = compact counts: `<workingN>● <idleN>○ <dormantN>✕` (each only included when > 0; dormant only when `ShowAll`).
- `pct` = `fmt.Sprintf("%.0f%%", 100 * d.TotalTokens / TotalSessionTokens)` (rollup share)
- `bar` = `progressBar(rollupPct, 5)`
- `amount` = `FmtTok(d.TotalTokens)` or `$d.TotalCostUSD` per toggle
- `burn` = sum of children burn rates → `fmt.Sprintf("%sk/m", fmtK(burnSum))`. (Today `dirRollup` shows no burn at all; theme C adds it. Today's `nodeRollup` already does this.)

**Path-node rollup row (today's `RenderPathNode` + `nodeRollup`):**

- Same as directory rollup — same five columns, same content rules. `n.WorkingN`, `n.IdleN`, `n.DormantN`, `n.TotalTokens`, `n.TotalCostUSD`, `n.BurnRateSum` all already exist on `PathNode` today. No new field needed.

### Indented collapse glyph (commit C2)

Today's `RenderPathNode` builds the label as:

```go
glyph := "▼" // or "▶"
indent := strings.Repeat("  ", n.Depth)
label := glyph + " " + indent + n.DisplayPath
```

After theme C:

```go
indent := strings.Repeat("  ", n.Depth)
label := indent + glyph + " " + n.DisplayPath
```

The glyph now sits at column `2*depth` instead of column 0. Cursor mark stays at column 0 (constant 2 cols, same as today). Session rows already indent their `prefix + cont` via `indent + prefix` in `RenderWindowTree` — unchanged.

The label column's width budget shrinks by `2*depth` for nested rows. `labelStyle(opts.Width)` already accounts for label width via `opts.Width - prefixCols - statsBlockCols`; for path-node rows the depth indent is part of the rendered label, so it just subtracts visually from the path text room. No change needed in `labelStyle` since the styled width is the **outer** width (label column + stats); the indent appears inside the label column's allotted space.

## Migration / commits

Two commits, both independently revertable:

1. **`feat(aggregate): add Directory.BurnRateSum`**
   - `aggregate.Build` populates `Directory.BurnRateSum` by accumulating `en.BurnRateShort`.
   - Unit test in `aggregate_test.go` asserts the sum equals the sum of child session burn rates.
   - No renderer change. The new field is unused; build still passes.

2. **`feat(render): unified column grid + indented glyphs`**
   - `tree.go` introduces `renderStatsBlock`, the new column-width constants and styles.
   - `renderSession`, `renderDirRow`, `RenderPathNode`, `dirRollup`, `nodeRollup` rewritten to share the column layout.
   - `tokens-or-cost` column always populated.
   - `RenderPathNode` re-orders `indent + glyph + " " + label`.
   - `tree_test.go` adds column-alignment assertions: parse output, find each stats column's left edge per row, assert all rows match.
   - Existing tests adjust where they relied on today's specific text (e.g., `TestDirRowPRTitleTruncated` uses substring match — fine; `TestTreeStatsAreRightAligned` may need adjustment if its current alignment check differs from the new one).

## Test plan

### Unit — `aggregate_test.go` (commit C1)

```go
func TestBuildPopulatesDirectoryBurnRateSum(t *testing.T) {
    tree := Build(...)
    for _, d := range tree.Dirs {
        var sum float64
        for _, sv := range d.Sessions {
            sum += sv.SessionEnrichment.BurnRateShort
        }
        if d.BurnRateSum != sum {
            t.Errorf("dir %q BurnRateSum = %.2f, want %.2f", d.Path, d.BurnRateSum, sum)
        }
    }
}
```

### Unit — `tree_test.go` (commit C2)

`TestTreeColumnAlignment`: build a tree with one path-node parent + 3 session children. Render. Split output by `\n`. For each row, find the **right edge** of each of the five stat columns. Assert all rows agree on each edge's position.

```go
func TestTreeColumnAlignment(t *testing.T) {
    rows := strings.Split(out, "\n")
    edges := make([][5]int, 0, len(rows))
    for _, r := range rows {
        if isStatsRow(r) {
            edges = append(edges, statsColumnEdges(r))
        }
    }
    for i := 1; i < len(edges); i++ {
        if edges[i] != edges[0] {
            t.Errorf("row %d edges %v != row 0 edges %v", i, edges[i], edges[0])
        }
    }
}
```

`statsColumnEdges` is a small test helper: locate each column's right edge by reading from the right end (burn column is rightmost) and walking back over the known column widths. The fixed-width design makes this deterministic.

`TestTreeRollupShowsAggregatePct`: build a tree where children's `SessionTokens` sum to 600 of total 1000. Assert the rollup row's `%` column reads `60%` and bar shows ~3 filled cells of 5.

`TestTreeIndentedGlyph`: build a 2-level path-tree. Render. Assert the inner node's `▼` or `▶` glyph appears at column `2`, not column `0`.

`TestTreeTokensColumnPopulatedInTokenMode`: build a tree with `CostMode: false`. Render. Assert the session row's `tokens-or-cost` column shows `FmtTok(SessionTokens)` (e.g., `342.0k`) — not blank, not a dollar sign.

### Integration — existing tests stay green

`TestSessionRowShowsTail`, `TestDirRowPRTitleTruncated`, `TestDirRowShowsBranch`, etc. Their substring assertions should survive since the new layout still emits all the same fields (just with a `tokens` value in the column previously left blank, and burn rate added to dir rollup which previously omitted it). If `TestTreeStatsAreRightAligned` checked specific column widths, update it to read from the new constants.

`view_test.go`'s `TestViewLineWidthInvariant` continues to pass: stats block width stays at 41 cols.

## Out of scope (theme D / E / future)

- Selection-status content in footer (theme D).
- First-prompt removal from session rows (theme D).
- `[?]` help modal, `[l]` legend modal (theme E).
- Tail icon restyling.
- Configurable column visibility / per-tier column dropping (could become its own follow-up if narrow terminals make 41 cols of stats too tight).

## Followups noted

- Once theme D removes the per-session first-prompt line (saves one body row per session), the alignment win compounds — denser session list per screen.
- Aggregating burn rate at directory level (today only `nodeRollup` had it) gives users a "directory hotspot" signal alongside the per-directory token total.
