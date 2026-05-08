# TUI Selection Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the cursor-selected session's first prompt in the footer's left column (dim style, single line) and drop the per-session prompt continuation row from the tree body.

**Architecture:** `Footer(width, status, updatedAt)` accepts a pre-styled left-column string. A new `FooterLeftWidth(width)` helper exposes the available status budget so the caller can clip + style before passing in. `view.go` computes the status from `m.flatRows[m.cursor]`. `tree.go::renderSession` drops the second-line prompt continuation.

**Tech Stack:** Go, lipgloss, existing `internal/render/wrap` package.

---

## Spec reference

Implements `docs/superpowers/specs/2026-05-08-tui-selection-panel-design.md`.

## File structure

| File | Status | Responsibility |
|------|--------|----------------|
| `packages/claude-agents-tui/internal/render/footer.go` | modify | Signature change + new `FooterLeftWidth` |
| `packages/claude-agents-tui/internal/render/footer_test.go` | modify | Update existing tests to new signature; add status-column tests |
| `packages/claude-agents-tui/internal/render/tree.go` | modify | Drop per-row prompt continuation in `renderSession` |
| `packages/claude-agents-tui/internal/render/tree_test.go` | modify | Add omits-continuation test |
| `packages/claude-agents-tui/internal/tui/view.go` | modify | Build status string from cursor + pass to Footer |
| `packages/claude-agents-tui/internal/tui/view_test.go` | modify | Add footer-shows-prompt test |

---

## Commit 1 — Footer signature change + `FooterLeftWidth`

### Task 1: Update `Footer` to accept a status string + add `FooterLeftWidth`

**Files:**
- Modify: `packages/claude-agents-tui/internal/render/footer.go`
- Modify: `packages/claude-agents-tui/internal/render/footer_test.go`

- [ ] **Step 1: Replace `footer.go`**

Read the current file first to confirm it matches the diff base. Then replace its contents with:

```go
package render

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// FooterLeftWidth returns the available column count for the footer's left
// column (status string) at the given terminal width.
//
//	WIDE   ≥120  width − 16 (Updated HH:MM:SS) − 2 (gap)
//	NARROW 80–119 width − 8 (HH:MM:SS) − 2 (gap)
//	TINY   <80   width  (no clock)
func FooterLeftWidth(width int) int {
	switch wrap.Tier(width) {
	case wrap.TierWide:
		return width - 16 - 2
	case wrap.TierNarrow:
		return width - 8 - 2
	default:
		return width
	}
}

// Footer renders the bottom row of the TUI: status (caller-supplied,
// already-styled, already-clipped) on the left, Updated clock on the right.
// At TINY the clock is dropped and the status is given the full row.
//
// status may be the empty string; the left column is then a blank styled span.
//
//	WIDE   ≥120  <status, padded to width-18>  Updated 21:05:43
//	NARROW 80–119 <status, padded to width-10>  21:05:43
//	TINY   <80   <status, full width>
func Footer(width int, status string, updatedAt time.Time) string {
	tier := wrap.Tier(width)
	if tier == wrap.TierTiny {
		return lipgloss.NewStyle().Width(width).Align(lipgloss.Left).Render(status)
	}

	var rightLabel string
	switch tier {
	case wrap.TierWide:
		rightLabel = "Updated " + updatedAt.Format("15:04:05") // 16 cols
	default: // TierNarrow
		rightLabel = updatedAt.Format("15:04:05") // 8 cols
	}
	rightWidth := lipgloss.Width(rightLabel)
	gap := 2
	leftWidth := width - rightWidth - gap
	if leftWidth < 1 {
		// Fall back: full-width status, no clock.
		return lipgloss.NewStyle().Width(width).Align(lipgloss.Left).Render(status)
	}

	leftStyled := lipgloss.NewStyle().Width(leftWidth).Align(lipgloss.Left).Render(status)
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Align(lipgloss.Right).Render(rightLabel)
	return leftStyled + strings.Repeat(" ", gap) + rightStyled
}
```

Key changes vs today:
- Function signature: `Footer(width, status, updatedAt)` (was `Footer(width, updatedAt)`).
- No more call to `Legend(...)` — status is whatever the caller supplies.
- New exported `FooterLeftWidth(width int) int` helper.

- [ ] **Step 2: Build to confirm compile (will fail at view.go)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build ./...
```

Expected: `view.go:39: not enough arguments in call to render.Footer`. That's expected — Task 3 fixes it.

- [ ] **Step 3: Update existing footer tests for the new signature**

Read `internal/render/footer_test.go` first. Current tests:
- `TestFooterUpdatedRightAligned` — calls `Footer(140, updated)`.
- `TestFooterShortUpdatedAtNarrow` — calls `Footer(100, updated)`.
- `TestFooterDropsUpdatedAtTiny` — calls `Footer(60, updated)`.
- `TestFooterContainsLegend` — asserts legend symbols present.
- `TestFooterSingleLine` — calls `Footer(120, time.Now())`.
- `TestFooterWidthInvariant` — table-drives widths.

Replace each `Footer(w, t)` call with `Footer(w, "", t)` (empty status preserves today's behavior of "blank left, clock right"). Then update `TestFooterContainsLegend` since the legend is no longer auto-included — remove or rewrite to test the new status path:

```go
func TestFooterUpdatedRightAligned(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(140, "", updated)
	if !strings.Contains(out, "Updated 21:05:43") {
		t.Errorf("expected 'Updated 21:05:43' at WIDE, got: %q", out)
	}
	if w := lipgloss.Width(out); w != 140 {
		t.Errorf("Footer(140) width = %d, want 140; got %q", w, out)
	}
	if !strings.HasSuffix(out, "Updated 21:05:43") {
		t.Errorf("Updated must hug right edge, got: %q", out)
	}
}

func TestFooterShortUpdatedAtNarrow(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(100, "", updated)
	if !strings.Contains(out, "21:05:43") {
		t.Errorf("expected '21:05:43' at NARROW, got: %q", out)
	}
	if strings.Contains(out, "Updated 21:05:43") {
		t.Errorf("at NARROW should drop 'Updated' prefix, got: %q", out)
	}
	if !strings.HasSuffix(out, "21:05:43") {
		t.Errorf("clock must hug right edge, got: %q", out)
	}
}

func TestFooterDropsUpdatedAtTiny(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(60, "", updated)
	if strings.Contains(out, "21:") {
		t.Errorf("at TINY should drop Updated entirely, got: %q", out)
	}
}

func TestFooterSingleLine(t *testing.T) {
	out := Footer(120, "", time.Now())
	if strings.Contains(out, "\n") {
		t.Errorf("Footer must be single line, got: %q", out)
	}
}

func TestFooterWidthInvariant(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	for _, w := range []int{60, 80, 100, 119, 120, 140, 200} {
		got := Footer(w, "", updated)
		if width := lipgloss.Width(got); width != w {
			t.Errorf("Footer(%d) width = %d, want %d; got %q", w, width, w, got)
		}
	}
}
```

DELETE `TestFooterContainsLegend` — the test asserted `Legend` symbols which are no longer auto-included. The legend is gone (theme E will reintroduce as a modal).

- [ ] **Step 4: Add new tests for the status column**

Append to `footer_test.go`:

```go
func TestFooterStatusFillsLeftColumn(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(140, "fake-prompt-text", updated)
	if !strings.Contains(out, "fake-prompt-text") {
		t.Errorf("expected status in footer, got: %q", out)
	}
	if !strings.HasSuffix(out, "Updated 21:05:43") {
		t.Errorf("clock should hug right edge: %q", out)
	}
	if w := lipgloss.Width(out); w != 140 {
		t.Errorf("width = %d, want 140", w)
	}
}

func TestFooterEmptyStatusKeepsClockAtRight(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(140, "", updated)
	if !strings.HasSuffix(out, "Updated 21:05:43") {
		t.Errorf("clock should hug right edge with empty status: %q", out)
	}
	if w := lipgloss.Width(out); w != 140 {
		t.Errorf("width = %d, want 140", w)
	}
}

func TestFooterStatusAtTinyWithoutClock(t *testing.T) {
	out := Footer(60, "fake-prompt-text", time.Now())
	if strings.Contains(out, "21:") || strings.Contains(out, "Updated") {
		t.Errorf("TINY should drop clock: %q", out)
	}
	if !strings.Contains(out, "fake-prompt-text") {
		t.Errorf("expected status in TINY output: %q", out)
	}
}

func TestFooterLeftWidth(t *testing.T) {
	cases := []struct {
		width, want int
	}{
		{200, 200 - 16 - 2}, // WIDE
		{120, 120 - 16 - 2}, // WIDE floor
		{100, 100 - 8 - 2},  // NARROW
		{80, 80 - 8 - 2},    // NARROW floor
		{60, 60},            // TINY: full
	}
	for _, c := range cases {
		if got := FooterLeftWidth(c.width); got != c.want {
			t.Errorf("FooterLeftWidth(%d) = %d, want %d", c.width, got, c.want)
		}
	}
}
```

- [ ] **Step 5: Run footer tests**

```bash
go test ./internal/render/... -run "TestFooter" -v -count=1
```

Expected: PASS for the seven tests above (the four updated + three added + width invariant). Old `TestFooterContainsLegend` must not appear (deleted).

- [ ] **Step 6: Don't commit yet — view.go still broken; Task 3 fixes it**

The full suite (`go test ./...`) is currently broken because `view.go:39` calls `Footer(m.width, now)` (two args). Task 3 of this same commit's plan would fix it, but commit D1 is just the renderer signature change. **Update view.go's single call site to compile in this commit**, but leave the status string empty; Task 3 (commit D2) adds the actual cursor-driven content.

In `internal/tui/view.go`, find:

```go
	footer := render.Footer(m.width, now)
```

Replace with:

```go
	footer := render.Footer(m.width, "", now)
```

(One-line edit. The footer just renders blank-left + clock-right, same visual as today's legend-less footer.)

Wait — today's footer DOES render the legend on the left. Replacing with `""` means the legend disappears between commit D1 and commit D2. That's intentional per the spec (the legend is going away anyway in theme E), but call this out: between commits the user sees no legend.

Actually, simpler: bridge by keeping the legend visible until commit D2 by calling `render.Legend(render.FooterLeftWidth(m.width))` here:

```go
	footer := render.Footer(m.width, render.Legend(render.FooterLeftWidth(m.width)), now)
```

This preserves today's visual exactly. Commit D2 then replaces this expression with the cursor-driven prompt computation.

- [ ] **Step 7: Run full suite + vet**

```bash
go test ./... -count=1
go vet ./...
```

Expected: all packages PASS, vet silent. The footer still shows the legend on the left (via the bridging `render.Legend(...)` call), the clock on the right.

- [ ] **Step 8: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/render/footer.go \
        packages/claude-agents-tui/internal/render/footer_test.go \
        packages/claude-agents-tui/internal/tui/view.go
git commit -m "$(cat <<'EOF'
feat(render): footer accepts a selection status string

Footer(width, updatedAt) → Footer(width, status, updatedAt). The status
parameter is the caller-supplied, already-styled left-column content;
empty string renders a blank styled span. New FooterLeftWidth(width)
helper exposes the available status budget so callers can clip + style
their content before passing it in.

view.go currently passes the existing legend through the helper to
preserve today's visuals; the next commit replaces that with cursor-
driven first-prompt content.

Implements docs/superpowers/specs/2026-05-08-tui-selection-panel-design.md
(theme D, commit 1 of 2).
EOF
)"
```

---

## Commit 2 — Selection-driven status + remove per-session prompt line

### Task 2: Build the status string from the cursor in `view.go`

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/view.go`

- [ ] **Step 1: Replace the footer assembly block**

Locate the line in `view.go` that constructs the footer (from Task 1 step 6):

```go
	footer := render.Footer(m.width, render.Legend(render.FooterLeftWidth(m.width)), now)
```

Replace with:

```go
	footer := render.Footer(m.width, m.selectionStatus(), now)
```

And add the helper method to `view.go`:

```go
// selectionStatus returns the dim-styled, single-line status string for the
// footer's left column. Empty when the cursor is not on a session row, or
// the selected session has no FirstPrompt.
func (m *Model) selectionStatus() string {
	if m.cursor < 0 || m.cursor >= len(m.flatRows) {
		return ""
	}
	row := m.flatRows[m.cursor]
	if row.Kind != render.SessionKind || row.Session == nil {
		return ""
	}
	fp := row.Session.SessionEnrichment.FirstPrompt
	if fp == "" {
		return ""
	}
	leftWidth := render.FooterLeftWidth(m.width)
	if leftWidth < 1 {
		return ""
	}
	text := fmt.Sprintf("%q", wrap.Line(fp, leftWidth))
	return m.theme.Prompt.Render(text)
}
```

You'll need to add `"fmt"` to the imports if it isn't already. Verify `render.SessionKind` is the imported render package's constant.

- [ ] **Step 2: Build**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build ./...
```

Expected: clean build.

- [ ] **Step 3: Run TUI tests**

```bash
go test ./internal/tui/... -count=1
```

Expected: PASS (existing tests). The new behavior isn't asserted yet — Task 4 adds the assertion.

---

### Task 3: Drop the per-row first-prompt continuation in `tree.go`

**Files:**
- Modify: `packages/claude-agents-tui/internal/render/tree.go`

- [ ] **Step 1: Locate `renderSession` (around lines 185–225)**

Find the block at the end of the function:

```go
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

- [ ] **Step 2: Delete the `if s.SessionEnrichment.FirstPrompt != ""` block**

Replace with a direct return:

```go
	out := fmt.Sprintf("%s%s %s  %s%s\n",
		cursorMark,
		prefix,
		labelStyle(opts.Width).Render(label),
		renderStatsBlock(col1, pctStr, barStr, amount, burn),
		tail,
	)
	return out
}
```

(One block deleted. The `cont` parameter to `renderSession` may now be unused — keep the signature unchanged for now to avoid touching call sites; it's a single int that costs nothing.)

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: clean build. If `cont` is flagged as unused via vet, leave it for now — it's a function parameter that future themes may use again, and removing it requires updating all callers (`Tree`, `RenderWindowTree`).

- [ ] **Step 4: Run tree tests**

```bash
go test ./internal/render/... -count=1
```

Expected: most PASS. If a test asserted the `↳` continuation appears, it now fails — that's caught by Task 4's added test which asserts the opposite. Look for any tests whose intent was to verify the continuation; either adjust them or delete if their only purpose was the continuation.

Common candidate: any test using a fixture with `FirstPrompt` set and asserting substring `↳` or the prompt text on a separate line. Update by removing the assertion or rewriting to assert the prompt is *absent* from the body.

---

### Task 4: Add tests covering the new behavior

**Files:**
- Modify: `packages/claude-agents-tui/internal/render/tree_test.go`
- Modify: `packages/claude-agents-tui/internal/tui/view_test.go`

- [ ] **Step 1: Add `TestSessionRowOmitsFirstPromptContinuation` to `tree_test.go`**

Append:

```go
// TestSessionRowOmitsFirstPromptContinuation verifies the per-session prompt
// continuation row was removed in theme D. The FirstPrompt is now shown only
// in the footer's selection-status column.
func TestSessionRowOmitsFirstPromptContinuation(t *testing.T) {
	s := &aggregate.SessionView{
		Session: &session.Session{Name: "n", SessionID: "id", Status: session.Working},
		SessionEnrichment: aggregate.SessionEnrichment{
			FirstPrompt:   "this prompt should NOT appear under the row anymore",
			SessionTokens: 100,
			Model:         "claude-opus-4-7",
		},
	}
	d := &aggregate.Directory{Path: "/p", Sessions: []*aggregate.SessionView{s}, WorkingN: 1, TotalTokens: 100}
	out := Tree(&aggregate.Tree{Dirs: []*aggregate.Directory{d}}, TreeOpts{TotalSessionTokens: 100, Width: 120})

	if strings.Contains(out, "↳") {
		t.Errorf("session row should no longer emit the ↳ continuation; got:\n%s", out)
	}
	if strings.Contains(out, "this prompt should NOT appear") {
		t.Errorf("FirstPrompt content leaked into the body:\n%s", out)
	}
}
```

- [ ] **Step 2: Run the new tree test**

```bash
go test ./internal/render/... -run "TestSessionRowOmitsFirstPromptContinuation" -v
```

Expected: PASS.

- [ ] **Step 3: Add `TestViewFooterShowsSelectedSessionFirstPrompt` to `view_test.go`**

Append (or place near the other view tests):

```go
// TestViewFooterShowsSelectedSessionFirstPrompt asserts the footer's left
// column contains the cursor-selected session's first prompt.
func TestViewFooterShowsSelectedSessionFirstPrompt(t *testing.T) {
	sv := &aggregate.SessionView{
		Session: &session.Session{Name: "n", SessionID: "id", Status: session.Working},
		SessionEnrichment: aggregate.SessionEnrichment{
			FirstPrompt:   "selected prompt content",
			SessionTokens: 100,
			Model:         "claude-opus-4-7",
		},
	}
	d := &aggregate.Directory{Path: "/p", Sessions: []*aggregate.SessionView{sv}, WorkingN: 1, TotalTokens: 100}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	// Cursor at first selectable row.
	m.cursor = nextSelectable(m.flatRows, 0, +1)

	out := m.View()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	footer := lines[len(lines)-1]
	if !strings.Contains(footer, "selected prompt content") {
		t.Errorf("footer should show selected session's first prompt; output:\n%s", out)
	}
}

// TestViewFooterBlankWhenCursorOnPathNode asserts the footer's left column is
// blank when the cursor is on a PathNodeKind row (not a SessionKind row).
func TestViewFooterBlankWhenCursorOnPathNode(t *testing.T) {
	// Build a tree with a path node + sessions; locate a PathNodeKind row.
	sv := &aggregate.SessionView{
		Session: &session.Session{Name: "n", SessionID: "id", Status: session.Working},
		SessionEnrichment: aggregate.SessionEnrichment{
			FirstPrompt:   "this should NOT appear when cursor is on path node",
			SessionTokens: 100,
		},
	}
	d := &aggregate.Directory{Path: "/p/sub", Sessions: []*aggregate.SessionView{sv}, WorkingN: 1, TotalTokens: 100}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})

	// Find a PathNodeKind row and put the cursor there.
	pathNodeIdx := -1
	for i, r := range m.flatRows {
		if r.Kind == render.PathNodeKind {
			pathNodeIdx = i
			break
		}
	}
	if pathNodeIdx < 0 {
		t.Skip("fixture produced no path-node row")
	}
	m.cursor = pathNodeIdx

	out := m.View()
	if strings.Contains(out, "this should NOT appear") {
		t.Errorf("path-node cursor should not show prompt in footer; output:\n%s", out)
	}
}
```

You'll need `tea` and `render` and `aggregate` imports — those are already present in `view_test.go`. Check before adding.

- [ ] **Step 4: Run the new view tests**

```bash
go test ./internal/tui/... -run "TestViewFooterShowsSelectedSessionFirstPrompt|TestViewFooterBlankWhenCursorOnPathNode" -v
```

Expected: PASS for both.

- [ ] **Step 5: Run full suite + vet**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./... -count=1
go vet ./...
```

Expected: all packages PASS, vet silent.

If any existing test fails, triage:
- **Test asserts `↳` continuation appears**: legitimate failure of a now-removed feature. Update the test to assert absence (or delete if the test's only purpose was the continuation).
- **Test asserts a specific footer line containing legend symbols**: legitimate failure since legend is gone. Delete or update.
- **Other**: investigate.

---

### Task 5: Verify + commit C2

- [ ] **Step 1: Final sweep**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./... -count=1
go vet ./...
```

Expected: all packages PASS, vet silent.

- [ ] **Step 2: Manual sanity (optional)**

```bash
go build -o /tmp/cat-tui ./cmd/claude-agents-tui
timeout 30 /tmp/cat-tui -wait-until-idle -time-between-checks 5 -maximum-wait 30 -consecutive-idle-checks 0 2>&1 | head -10
```

Expected: header (controls + 5h block) + sessions (one row each, no `↳` continuations) + footer.

- [ ] **Step 3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/tui/view.go \
        packages/claude-agents-tui/internal/tui/view_test.go \
        packages/claude-agents-tui/internal/render/tree.go \
        packages/claude-agents-tui/internal/render/tree_test.go
git commit -m "$(cat <<'EOF'
feat(tui): selection status shows first prompt; drop per-row prompt

view.go's selectionStatus() builds a dim-styled, single-line status from
the cursor-selected session's FirstPrompt, clipped to the footer's left
column budget. Path-node rows and empty-prompt sessions render a blank
status. The legend that previously occupied the footer is gone (theme E
will reintroduce as an [l] modal).

tree.go::renderSession no longer emits the second-line ↳ continuation
for sessions with a FirstPrompt — the prompt now lives in the footer
status only. Halves the per-session vertical real estate.

Implements docs/superpowers/specs/2026-05-08-tui-selection-panel-design.md
(theme D, commit 2 of 2).
EOF
)"
```

---

## Self-Review Checklist (run before handing off)

- [ ] Spec coverage: `Footer(width, status, updatedAt)` signature change (Task 1) + `FooterLeftWidth` (Task 1) + view.go status computation (Task 2) + per-row prompt removal (Task 3) + tests (Task 4). All sections of the spec map to a task.
- [ ] No placeholders.
- [ ] Type / identifier consistency: `Footer`, `FooterLeftWidth`, `selectionStatus` (method on `*Model`).
- [ ] TDD ordering preserved per task.
- [ ] Two commits, both independently revertable. Commit D1 leaves the visual identical to today (legend still showing via bridge); commit D2 swaps to selection status and drops the per-row prompt.
