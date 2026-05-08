# TUI Tree Row Column Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unify the column grid for session and rollup rows so all stats columns line up at the same screen positions, switch the per-row tokens-or-cost column to always render a value, and indent path-node `▼/▶` glyphs so the collapse structure is visible.

**Architecture:** A single `renderStatsBlock(col1, pct, bar, amount, burn) string` helper applied to all row types. Column widths are constants in one place. `Directory` gains a `BurnRateSum` field so its rollup row can show burn just like `PathNode` already does. `RenderPathNode` reorders the glyph to sit after the depth indent.

**Tech Stack:** Go, lipgloss.

---

## Spec reference

Implements `docs/superpowers/specs/2026-05-08-tui-tree-alignment-design.md`.

## File structure

| File | Status | Responsibility |
|------|--------|----------------|
| `packages/claude-agents-tui/internal/aggregate/tree.go` | modify | Add `BurnRateSum float64` to `Directory` |
| `packages/claude-agents-tui/internal/aggregate/aggregate.go` | modify | Populate `d.BurnRateSum` in `Build` |
| `packages/claude-agents-tui/internal/aggregate/aggregate_test.go` | modify | Test for new sum |
| `packages/claude-agents-tui/internal/render/tree.go` | modify | New column constants + helper + rewritten row renderers |
| `packages/claude-agents-tui/internal/render/tree_test.go` | modify | Column-alignment, indent-glyph, tokens-column tests |

---

## Commit 1 — `Directory.BurnRateSum`

### Task 1: Add field + populate + test

**Files:**
- Modify: `packages/claude-agents-tui/internal/aggregate/tree.go`
- Modify: `packages/claude-agents-tui/internal/aggregate/aggregate.go`
- Modify: `packages/claude-agents-tui/internal/aggregate/aggregate_test.go`

- [ ] **Step 1: Write the failing test (`aggregate_test.go`)**

Add at end of file:

```go
func TestBuildPopulatesDirectoryBurnRateSum(t *testing.T) {
	sessions := []*session.Session{
		{SessionID: "a", Cwd: "/p1"},
		{SessionID: "b", Cwd: "/p1"},
		{SessionID: "c", Cwd: "/p2"},
	}
	enriched := map[string]SessionEnrichment{
		"a": {BurnRateShort: 100},
		"b": {BurnRateShort: 50},
		"c": {BurnRateShort: 200},
	}
	tree := Build(sessions, enriched, nil, nil, "")

	byPath := map[string]*Directory{}
	for _, d := range tree.Dirs {
		byPath[d.Path] = d
	}

	if got := byPath["/p1"].BurnRateSum; got != 150 {
		t.Errorf("/p1 BurnRateSum = %.0f, want 150", got)
	}
	if got := byPath["/p2"].BurnRateSum; got != 200 {
		t.Errorf("/p2 BurnRateSum = %.0f, want 200", got)
	}
}
```

- [ ] **Step 2: Run test (verify failure)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/aggregate/... -run "TestBuildPopulatesDirectoryBurnRateSum" -v
```

Expected: FAIL with `BurnRateSum undefined` or `=== RUN ... 0, want 150`.

- [ ] **Step 3: Add `BurnRateSum` to `Directory`**

In `internal/aggregate/tree.go`, locate the `Directory` struct (around line 24-34) and add the new field:

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
	BurnRateSum  float64 // NEW: sum of children's BurnRateShort
}
```

- [ ] **Step 4: Populate in `Build`**

In `internal/aggregate/aggregate.go`, locate the per-session loop (around line 16-40). Add `d.BurnRateSum += en.BurnRateShort` next to the existing `d.TotalTokens += en.SessionTokens`:

```go
		d.Sessions = append(d.Sessions, sv)
		if d.Branch == "" && s.Branch != "" {
			d.Branch = s.Branch
		}
		d.TotalTokens += en.SessionTokens
		d.BurnRateSum += en.BurnRateShort   // NEW
		switch s.Status {
```

- [ ] **Step 5: Run test (verify pass)**

```bash
go test ./internal/aggregate/... -run "TestBuildPopulatesDirectoryBurnRateSum" -v
```

Expected: PASS.

- [ ] **Step 6: Run full suite + vet**

```bash
go test ./... -count=1
go vet ./...
```

Expected: all green, vet silent.

- [ ] **Step 7: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/aggregate/tree.go \
        packages/claude-agents-tui/internal/aggregate/aggregate.go \
        packages/claude-agents-tui/internal/aggregate/aggregate_test.go
git commit -m "$(cat <<'EOF'
feat(aggregate): add Directory.BurnRateSum

Sums each session's BurnRateShort so directory rollup rows can show
a burn-rate column like path-node rollups already do. Used by the
upcoming render/tree.go column-alignment work.

Implements docs/superpowers/specs/2026-05-08-tui-tree-alignment-design.md
(theme C, commit 1 of 2).
EOF
)"
```

---

## Commit 2 — Renderer rewrite (column grid + indented glyph)

### Task 2: Add column constants + `renderStatsBlock` helper

**Files:**
- Modify: `packages/claude-agents-tui/internal/render/tree.go`

- [ ] **Step 1: Replace today's per-column styles with the new five-column set**

In `internal/render/tree.go`, find the existing column-style block (around lines 13-19):

```go
var (
	styleModel  = lipgloss.NewStyle().Width(10).Align(lipgloss.Right)
	stylePct    = lipgloss.NewStyle().Width(5).Align(lipgloss.Right)
	styleBar    = lipgloss.NewStyle().Width(5).Align(lipgloss.Right)
	styleBurn   = lipgloss.NewStyle().Width(7).Align(lipgloss.Right)
	styleAmount = lipgloss.NewStyle().Width(8).Align(lipgloss.Right)
)
```

Replace with:

```go
const (
	col1Width    = 10 // model name (sessions) or counts (rollups)
	colPctWidth  = 5  // "100%"
	colBarWidth  = 5  // 5-cell bar
	colAmtWidth  = 10 // FmtTok(...) or "$X.XX"
	colBurnWidth = 7  // "1.2M/m"
)

var (
	styleCol1 = lipgloss.NewStyle().Width(col1Width).Align(lipgloss.Right)
	stylePct  = lipgloss.NewStyle().Width(colPctWidth).Align(lipgloss.Right)
	styleBar  = lipgloss.NewStyle().Width(colBarWidth).Align(lipgloss.Right)
	styleAmt  = lipgloss.NewStyle().Width(colAmtWidth).Align(lipgloss.Right)
	styleBurn = lipgloss.NewStyle().Width(colBurnWidth).Align(lipgloss.Right)
)
```

- [ ] **Step 2: Update `statsBlockCols` to use the new constants**

Find the existing `statsBlockCols` constant (around line 47). Replace:

```go
const statsBlockCols = 41
```

with:

```go
// statsBlockCols is the total width of the right-side stats block including
// the single space between each of the five columns:
//   col1(10) + sp + pct(5) + sp + bar(5) + sp + amount(10) + sp + burn(7) = 43
const statsBlockCols = col1Width + 1 + colPctWidth + 1 + colBarWidth + 1 + colAmtWidth + 1 + colBurnWidth
```

Note the actual numeric value changes from 41 to 43 because `colAmtWidth` grows from 8 to 10. `labelStyle(termWidth)` and any caller that uses `statsBlockCols` will pick up the new width automatically.

- [ ] **Step 3: Add `renderStatsBlock` helper**

Anywhere in `tree.go` (suggested: just after the style block, around the new `statsBlockCols` constant). Add:

```go
// renderStatsBlock returns the five-column stats string applied to any row.
// All five values are pre-formatted strings; this helper applies the
// right-alignment styling and joins with single spaces. The total visible
// width equals statsBlockCols.
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

- [ ] **Step 4: Build to verify compile**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build ./...
```

Expected: errors mentioning `styleModel undefined` or `styleAmount undefined` from existing callers (`renderSession`). Tasks 3-5 fix these.

---

### Task 3: Rewrite `renderSession` to use the new helper + tokens-or-cost column

**Files:**
- Modify: `packages/claude-agents-tui/internal/render/tree.go`

- [ ] **Step 1: Replace `renderSession`**

Locate `renderSession` (around lines 133-176 today). Replace its body with:

```go
func renderSession(s *aggregate.SessionView, opts TreeOpts, prefix, cont string, selected bool) string {
	cursorMark := "  "
	if selected {
		cursorMark = opts.Theme.Cursor.Render(">") + " "
	}
	sym := symbol(s.Status, s.SessionEnrichment.AwaitingInput, !s.SessionEnrichment.RateLimitResetsAt.IsZero(), opts.Theme)
	var label string
	if !s.SessionEnrichment.RateLimitResetsAt.IsZero() {
		resetStr := s.SessionEnrichment.RateLimitResetsAt.Local().Format("15:04")
		label = fmt.Sprintf("%s %s %s", sym, resetStr, s.Label(opts.ForceID))
	} else {
		label = fmt.Sprintf("%s %s", sym, s.Label(opts.ForceID))
	}

	col1 := shortModel(s.SessionEnrichment.Model)
	pct := sessionSharePct(s.SessionEnrichment.SessionTokens, opts.TotalSessionTokens)
	pctStr := fmt.Sprintf("%.0f%%", pct)
	barStr := progressBar(pct, colBarWidth)
	amount := FmtTok(s.SessionEnrichment.SessionTokens)
	if opts.CostMode {
		amount = fmt.Sprintf("$%.2f", s.SessionEnrichment.CostUSD)
	}
	burn := fmt.Sprintf("%sk/m", fmtK(s.SessionEnrichment.BurnRateShort))

	tail := ""
	if s.SessionEnrichment.SubagentCount > 0 {
		tail += fmt.Sprintf(" %d🤖", s.SessionEnrichment.SubagentCount)
	}
	if s.SessionEnrichment.SubshellCount > 0 {
		tail += fmt.Sprintf(" %d🐚", s.SessionEnrichment.SubshellCount)
	}

	out := fmt.Sprintf("%s%s %s  %s%s\n",
		cursorMark,
		prefix,
		labelStyle(opts.Width).Render(label),
		renderStatsBlock(col1, pctStr, barStr, amount, burn),
		tail,
	)
	if s.SessionEnrichment.FirstPrompt != "" {
		out += fmt.Sprintf("  %s    ↳ %s\n", cont, opts.Theme.Prompt.Render(fmt.Sprintf("%q", wrap.Line(s.SessionEnrichment.FirstPrompt, 80))))
	}
	return out
}
```

Key behavioral changes vs today:
- Tokens-or-cost column always populated (`FmtTok(SessionTokens)` when not in cost mode).
- All five stat columns flow through `renderStatsBlock`.
- Format string after the label collapsed: `"  %s%s\n"` → stats block + tail + newline. The single `"  "` before `%s` keeps the two-space gap between the label and the stats block (today the format string had a single space before model + four single spaces between columns; the new helper handles inter-column spacing internally).

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: still compile errors from `renderDirRow` and `RenderPathNode` (they reference removed style names). Tasks 4-5 fix these.

---

### Task 4: Rewrite `dirRollup` + `renderDirRow` for the unified grid

**Files:**
- Modify: `packages/claude-agents-tui/internal/render/tree.go`

- [ ] **Step 1: Replace `dirRollup`**

Locate `dirRollup` (around lines 94-111). Replace with:

```go
// dirRollupCols formats the five stat columns for a directory rollup row.
// Counts go into col1; %, bar, amount, and burn share the same grid as session rows.
func dirRollupCols(d *aggregate.Directory, opts TreeOpts) (col1, pct, bar, amount, burn string) {
	col1 = countsString(d.WorkingN, d.IdleN, d.DormantN, opts.ShowAll)

	rollupPct := 0.0
	if opts.TotalSessionTokens > 0 {
		rollupPct = 100 * float64(d.TotalTokens) / float64(opts.TotalSessionTokens)
	}
	pct = fmt.Sprintf("%.0f%%", rollupPct)
	bar = progressBar(rollupPct, colBarWidth)

	amount = FmtTok(d.TotalTokens)
	if opts.CostMode {
		amount = fmt.Sprintf("$%.2f", d.TotalCostUSD)
	}
	burn = fmt.Sprintf("%sk/m", fmtK(d.BurnRateSum))
	return
}

// countsString formats the rollup counts column ("3● 1○ 1✕"), omitting zero
// counts and dormant unless ShowAll is on.
func countsString(workingN, idleN, dormantN int, showAll bool) string {
	parts := []string{}
	if workingN > 0 {
		parts = append(parts, fmt.Sprintf("%d●", workingN))
	}
	if idleN > 0 {
		parts = append(parts, fmt.Sprintf("%d○", idleN))
	}
	if dormantN > 0 && showAll {
		parts = append(parts, fmt.Sprintf("%d✕", dormantN))
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 2: Replace `renderDirRow`**

Locate `renderDirRow` (around lines 113-131). Replace with:

```go
func renderDirRow(d *aggregate.Directory, opts TreeOpts) string {
	col1, pct, bar, amount, burn := dirRollupCols(d, opts)
	stats := renderStatsBlock(col1, pct, bar, amount, burn)

	rowWidth := prefixCols + minLabelWidth + statsBlockCols
	if opts.Width > 0 {
		rowWidth = opts.Width
	}

	branchStr := ""
	if d.Branch != "" {
		branchStr = "  🌿 " + opts.Theme.Branch.Render(d.Branch)
		if d.PRInfo != nil {
			prNum := osc8Link(d.PRInfo.URL, fmt.Sprintf("#%d", d.PRInfo.Number))
			prTitle := wrap.Line(d.PRInfo.Title, 40)
			branchStr += "  →  " + prNum + " " + prTitle
		}
	}

	leftWidth := max(rowWidth-lipgloss.Width(stats)-1, lipgloss.Width(d.Path))
	pathStyle := opts.Theme.DirRow.Width(leftWidth).Align(lipgloss.Left)
	return pathStyle.Render(d.Path+branchStr) + " " + stats + "\n"
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: still errors for `RenderPathNode` and `nodeRollup`. Task 5 fixes those.

---

### Task 5: Rewrite `nodeRollup` + `RenderPathNode` (with indented glyph)

**Files:**
- Modify: `packages/claude-agents-tui/internal/render/tree.go`

- [ ] **Step 1: Replace `RenderPathNode`**

Locate `RenderPathNode` (around lines 234-263). Replace with:

```go
// RenderPathNode renders one PathNode row with collapse glyph, indentation, and rollup stats.
// selected controls the cursor mark prefix. collapsed controls the ▶/▼ glyph.
func RenderPathNode(n *aggregate.PathNode, opts TreeOpts, selected, collapsed bool) string {
	cursorMark := "  "
	if selected {
		cursorMark = opts.Theme.Cursor.Render(">") + " "
	}
	glyph := "▼"
	if collapsed {
		glyph = "▶"
	}
	indent := strings.Repeat("  ", n.Depth)
	// Glyph sits AFTER the depth indent so the collapse structure mirrors the tree.
	label := indent + glyph + " " + n.DisplayPath

	col1, pct, bar, amount, burn := nodeRollupCols(n, opts)
	stats := renderStatsBlock(col1, pct, bar, amount, burn)

	rowWidth := prefixCols + minLabelWidth + statsBlockCols
	if opts.Width > 0 {
		rowWidth = opts.Width
	}
	available := rowWidth - 2 // subtract cursor-mark width
	leftWidth := max(available-lipgloss.Width(stats)-1, lipgloss.Width(label))
	pathStyle := opts.Theme.DirRow.Width(leftWidth).Align(lipgloss.Left)
	return cursorMark + pathStyle.Render(label) + " " + stats + "\n"
}
```

- [ ] **Step 2: Replace `nodeRollup` with `nodeRollupCols`**

Locate `nodeRollup` (around lines 265-285). Replace with:

```go
// nodeRollupCols formats the five stat columns for a path-node rollup row.
func nodeRollupCols(n *aggregate.PathNode, opts TreeOpts) (col1, pct, bar, amount, burn string) {
	col1 = countsString(n.WorkingN, n.IdleN, n.DormantN, opts.ShowAll)

	rollupPct := 0.0
	if opts.TotalSessionTokens > 0 {
		rollupPct = 100 * float64(n.TotalTokens) / float64(opts.TotalSessionTokens)
	}
	pct = fmt.Sprintf("%.0f%%", rollupPct)
	bar = progressBar(rollupPct, colBarWidth)

	amount = FmtTok(n.TotalTokens)
	if opts.CostMode {
		amount = fmt.Sprintf("$%.2f", n.TotalCostUSD)
	}
	burn = fmt.Sprintf("%sk/m", fmtK(n.BurnRateSum))
	return
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: success. All callers now route through `renderStatsBlock`.

- [ ] **Step 4: Run existing tests**

```bash
go test ./... -count=1
```

Expected: most PASS. Some `tree_test.go` tests may fail because they relied on today's specific text. Triage:

- `TestSessionRowShowsTail`: substring assertions for `🤖` / `🐚` survive — tail is preserved.
- `TestDirRowPRTitleTruncated`: looks for absence of full long title; survives.
- `TestDirRowShowsBranch`: substring for branch name; survives.
- `TestTreeStatsAreRightAligned`: this test specifically checks alignment via fixed character widths — it may need updating to use the new column constants. **Read the test first**, see what it asserts, and update its expected widths if needed. Don't change the test's intent.

If a test breaks because of a real regression (column missing, alignment wrong), fix the renderer. If it breaks because the test asserts old widths, update the test.

---

### Task 6: Add column-alignment + indent + tokens-column tests

**Files:**
- Modify: `packages/claude-agents-tui/internal/render/tree_test.go`

- [ ] **Step 1: Append three new tests**

Add at end of file:

```go
// TestTreeColumnAlignment asserts that all stats-bearing rows in the tree
// agree on the right edge of each of the five stat columns.
func TestTreeColumnAlignment(t *testing.T) {
	d := &aggregate.Directory{
		Path:        "/p",
		TotalTokens: 600,
		BurnRateSum: 30,
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{Name: "a", SessionID: "id-a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7", SessionTokens: 200, BurnRateShort: 10,
				},
			},
			{
				Session: &session.Session{Name: "b", SessionID: "id-b", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-sonnet-4-6", SessionTokens: 400, BurnRateShort: 20,
				},
			},
		},
		WorkingN: 2,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{TotalSessionTokens: 1000, Width: 120})

	// Find the right edge of the burn column on every non-empty line that
	// looks like a stats row. With renderStatsBlock the line ends with the
	// burn column (modulo any trailing tail). We assert all stats rows agree.
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var edges []int
	for _, r := range rows {
		// Skip continuation rows ("    ↳ ...") and blank rows.
		if r == "" || strings.Contains(r, "    ↳ ") {
			continue
		}
		// The burn column ends just before any tail glyphs (🤖/🐚) or end-of-line.
		// Find its right edge by trimming any tail and measuring visible width.
		core := r
		for _, glyph := range []string{" 🤖", " 🐚"} {
			if i := strings.Index(core, glyph); i >= 0 {
				core = core[:i]
			}
		}
		edges = append(edges, lipgloss.Width(core))
	}

	if len(edges) < 2 {
		t.Fatalf("expected at least 2 stats rows, got %d:\n%s", len(edges), out)
	}
	for i := 1; i < len(edges); i++ {
		if edges[i] != edges[0] {
			t.Errorf("row %d burn-column edge = %d, want %d (matching row 0); rows:\n%s", i, edges[i], edges[0], out)
		}
	}
}

// TestTreeRollupShowsAggregatePct verifies that the directory rollup's % column
// equals the directory's share of total session tokens.
func TestTreeRollupShowsAggregatePct(t *testing.T) {
	d := &aggregate.Directory{
		Path:        "/p",
		TotalTokens: 600,
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{Name: "a", SessionID: "id-a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7", SessionTokens: 600,
				},
			},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{TotalSessionTokens: 1000})
	if !strings.Contains(out, "60%") {
		t.Errorf("expected '60%%' on rollup row (600/1000), got:\n%s", out)
	}
}

// TestTreeIndentedPathNodeGlyph verifies that the ▼/▶ glyph sits after the
// depth indent on a nested PathNode row, not at column 0.
func TestTreeIndentedPathNodeGlyph(t *testing.T) {
	node := &aggregate.PathNode{
		DisplayPath: "leaf",
		Depth:       2, // indent = "    " (4 spaces)
	}
	out := RenderPathNode(node, TreeOpts{}, false, false)
	// The glyph "▼" should be preceded by 2*Depth = 4 spaces (after the
	// 2-col cursor mark "  ").
	prefix := "    " // cursor mark (2) + indent (4) = 6, but indent is part of label
	if !strings.HasPrefix(out, "  "+prefix+"▼ ") {
		t.Errorf("expected glyph at column 6 ('  ' cursor + '    ' indent + '▼'), got: %q", out)
	}
}

// TestTreeTokensColumnPopulatedInTokenMode verifies that the tokens-or-cost
// column shows a token figure (e.g., "342.0k") when CostMode is false, not
// blank space.
func TestTreeTokensColumnPopulatedInTokenMode(t *testing.T) {
	d := &aggregate.Directory{
		Path:        "/p",
		TotalTokens: 342_000,
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{Name: "a", SessionID: "id-a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7", SessionTokens: 342_000,
				},
			},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{TotalSessionTokens: 342_000, CostMode: false})
	if !strings.Contains(out, "342.0k") {
		t.Errorf("expected '342.0k' in tokens column, got:\n%s", out)
	}
}
```

You may need to ensure imports cover `lipgloss`, `strings`, `aggregate`, `session`. Existing `tree_test.go` likely has these; verify before pasting.

- [ ] **Step 2: Run new tests**

```bash
go test ./internal/render/... -run "TestTreeColumnAlignment|TestTreeRollupShowsAggregatePct|TestTreeIndentedPathNodeGlyph|TestTreeTokensColumnPopulatedInTokenMode" -v
```

Expected: PASS for all four.

If `TestTreeColumnAlignment` fails because dir-rollup row has different right-edge than session rows, the `dirRollup`/`session` rendering is off. Common cause: dir row appended a trailing `branchStr` that pushed the stats block off-line. The new `renderDirRow` puts stats at the end and still pads the label column to match — re-read the code carefully.

If `TestTreeIndentedPathNodeGlyph` fails, double-check `RenderPathNode`'s `label := indent + glyph + " " + n.DisplayPath` ordering.

---

### Task 7: Verify whole suite + commit C2

- [ ] **Step 1: Full sweep**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./... -count=1
go vet ./...
```

Expected: all packages PASS, vet silent.

- [ ] **Step 2: Manual sanity (optional)**

```bash
go build -o /tmp/cat-tui ./cmd/claude-agents-tui
timeout 30 /tmp/cat-tui -wait-until-idle -time-between-checks 5 -maximum-wait 30 -consecutive-idle-checks 0 2>&1 | head -20
```

Expected: rollup rows and session rows visibly line up at the right edge. Path-tree glyphs indent under their parents.

- [ ] **Step 3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/render/tree.go \
        packages/claude-agents-tui/internal/render/tree_test.go
git commit -m "$(cat <<'EOF'
feat(render): unified tree column grid + indented path-node glyphs

All tree rows (sessions and rollups) share a single five-column right-aligned
stats grid via a new renderStatsBlock helper:

  col1(10)  pct(5)  bar(5)  amount(10)  burn(7)

Sessions show the model name in col1; rollups show counts (e.g. "3● 1○ 1✕").
The amount column is now always populated — FmtTok(SessionTokens) when in
token mode, "$X.XX" when in cost mode (today's amount column was blank in
token mode). Directory rollups gain a burn column matching path-node rollups.
Aggregate % on rollups is the directory/node's share of total session tokens.

RenderPathNode now puts the ▼/▶ glyph after the depth indent so the collapse
structure visually mirrors the tree (today the glyph always sat at column 0).

Implements docs/superpowers/specs/2026-05-08-tui-tree-alignment-design.md
(theme C, commit 2 of 2).
EOF
)"
```

---

## Self-Review Checklist (run before handing off)

- [ ] Spec coverage: `Directory.BurnRateSum` (Task 1) + column grid + tokens-or-cost + indented glyph (Tasks 2-5) + tests (Task 6). All sections of the spec map to a task.
- [ ] No placeholders: every step has runnable commands, complete code, expected output.
- [ ] Type / identifier consistency: `BurnRateSum`, `renderStatsBlock`, `dirRollupCols`, `nodeRollupCols`, `countsString`, `col1Width`, `colPctWidth`, `colBarWidth`, `colAmtWidth`, `colBurnWidth`. Used consistently.
- [ ] TDD ordering preserved per task.
- [ ] Two commits, each independently revertable. Commit 1 leaves `BurnRateSum` populated but unused (build passes). Commit 2 consumes it.
