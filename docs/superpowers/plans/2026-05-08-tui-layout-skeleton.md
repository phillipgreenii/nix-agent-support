# TUI Layout Skeleton + Bug Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `View()`'s ad-hoc `strings.Join` body-budget math with an explicit zone-based layout that always returns exactly `m.height` lines, and fix the cursor-overrun bug that lets the cursor land on blank separator rows.

**Architecture:** Three named zones (`header`, `body`, `status`) each carry a `dropOrder`. The body fills whatever rows remain; non-body zones drop in priority order when the terminal is too short. A single `layoutZones` helper computes the body's height, calls each renderer, and pads/truncates to exact height. Cursor navigation gains a `nextSelectable` helper so `Down`/`Up` skip `BlankKind` rows.

**Tech Stack:** Go, bubbletea, lipgloss, existing `internal/render/wrap` package.

---

## Spec reference

Implements `docs/superpowers/specs/2026-05-08-tui-layout-skeleton-design.md`.

The spec describes 5 zones (controls / 5h-block / alert / body / status). Theme A only ships **3** zones — `header` (covers today's `render.Header` multi-line output), `body`, and `status`. Splitting `header` into `controls` + `5hBlock` + `alert` is theme B's job (single-row tier-aware content); the `layoutZones` API stays identical so theme B is a content swap, not an architectural change.

## File structure

| File | Status | Responsibility |
|------|--------|----------------|
| `packages/claude-agents-tui/internal/tui/layout.go` | new | `zoneSpec` type + `layoutZones` helper |
| `packages/claude-agents-tui/internal/tui/layout_test.go` | new | Unit tests: height invariant, drop priority, padding/truncation |
| `packages/claude-agents-tui/internal/tui/view.go` | modify | Replace `strings.Join` + `visualLineCount` with `layoutZones` call |
| `packages/claude-agents-tui/internal/tui/view_test.go` | modify | Extend `TestViewLineWidthInvariant` to assert line count == height |
| `packages/claude-agents-tui/internal/tui/model.go` | modify | Add `selectable` + `nextSelectable` helpers; update `clampCursor` |
| `packages/claude-agents-tui/internal/tui/update.go` | modify | `down`/`j`/`up`/`k` use `nextSelectable` |
| `packages/claude-agents-tui/internal/tui/model_test.go` | modify | Cursor-overrun + skip-blank-rows tests |

---

## Commit 1 — Layout zones + `View()` rewrite

### Task 1: Create the `zoneSpec` type and `layoutZones` helper

**Files:**
- Create: `packages/claude-agents-tui/internal/tui/layout.go`

- [ ] **Step 1: Write `layout.go`**

```go
package tui

import (
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// zoneSpec describes one row group in (*Model).View()'s output.
//
// Non-fill zones contribute a fixed pre-rendered string. The fill zone (there
// must be exactly one) is rendered last with whatever rows remain after all
// surviving non-fill zones have claimed their share.
//
// dropOrder is consulted only for non-fill zones. When the terminal is too
// short to fit the desired layout (sum(non-fill heights) >= height), zones
// with the smallest dropOrder are removed first until the fill zone has at
// least 1 row, or until only the fill zone remains.
type zoneSpec struct {
	name       string
	content    string             // for non-fill zones; ignored when fill=true
	fill       bool               // exactly one zone must set this
	dropOrder  int                // smaller = drops first; ignored when fill=true
	renderFill func(height int) string
}

// lineCount returns the number of "\n"-separated lines a zone contributes.
// Non-fill: count "\n" + 1 in content. Fill: not used (caller supplies height).
func (z zoneSpec) lineCount() int {
	if z.fill || z.content == "" {
		return 0
	}
	return strings.Count(z.content, "\n") + 1
}

// layoutZones returns a string with exactly `height` "\n"-separated lines
// when height > 0. When height == 0 the function returns the zones
// concatenated in source order with no padding or truncation (test/headless
// mode — caller is expected to bypass for headless rendering).
//
// Width is forwarded to wrap.Block as a final per-line clip so every emitted
// line satisfies lipgloss.Width(line) <= width.
func layoutZones(zones []zoneSpec, width, height int) string {
	if height == 0 {
		return concatZones(zones, 0, width)
	}

	survivors := append([]zoneSpec(nil), zones...)
	bodyHeight := computeBodyHeight(survivors, height)
	for bodyHeight < 1 {
		idx := highestPriorityNonFill(survivors)
		if idx < 0 {
			break // only fill zone left; let body claim whatever height is
		}
		survivors = append(survivors[:idx], survivors[idx+1:]...)
		bodyHeight = computeBodyHeight(survivors, height)
	}
	if bodyHeight < 1 {
		bodyHeight = height // only fill zone remains; let it consume everything
	}

	out := concatZones(survivors, bodyHeight, width)
	return padOrTruncate(out, height)
}

// computeBodyHeight = height - sum(lineCount for non-fill survivors). May go negative.
func computeBodyHeight(zones []zoneSpec, height int) int {
	used := 0
	for _, z := range zones {
		if !z.fill {
			used += z.lineCount()
		}
	}
	return height - used
}

// highestPriorityNonFill returns the index in zones of the surviving non-fill
// zone with the smallest dropOrder, or -1 if none.
func highestPriorityNonFill(zones []zoneSpec) int {
	idx := -1
	for i, z := range zones {
		if z.fill {
			continue
		}
		if idx < 0 || z.dropOrder < zones[idx].dropOrder {
			idx = i
		}
	}
	return idx
}

// concatZones joins surviving zones in source order. The fill zone is
// rendered with bodyHeight. Width is forwarded to wrap.Block as the final
// per-line clip.
func concatZones(zones []zoneSpec, bodyHeight, width int) string {
	parts := make([]string, 0, len(zones))
	for _, z := range zones {
		var s string
		switch {
		case z.fill:
			if z.renderFill != nil && bodyHeight > 0 {
				s = z.renderFill(bodyHeight)
			}
		default:
			s = z.content
		}
		parts = append(parts, s)
	}
	joined := strings.Join(parts, "\n")
	if width > 0 {
		joined = wrap.Block(joined, width)
	}
	return joined
}

// padOrTruncate returns s with exactly height "\n"-separated lines.
// Shorter: pads with empty lines at the bottom. Longer: keeps the first
// `height` lines and discards the rest.
func padOrTruncate(s string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 2: Build to verify the file compiles**

Run: `go build ./...` from `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui`
Expected: no output (success).

---

### Task 2: Unit tests for `layoutZones`

**Files:**
- Create: `packages/claude-agents-tui/internal/tui/layout_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package tui

import (
	"strings"
	"testing"
)

// fixed produces a zoneSpec carrying `n` lines of static content.
func fixedZone(name string, n int, dropOrder int) zoneSpec {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = name + "-line-" + string(rune('A'+i))
	}
	return zoneSpec{name: name, content: strings.Join(lines, "\n"), dropOrder: dropOrder}
}

func bodyZone(label string) zoneSpec {
	return zoneSpec{
		name: "body",
		fill: true,
		renderFill: func(h int) string {
			lines := make([]string, h)
			for i := range lines {
				lines[i] = label + "-row"
			}
			return strings.Join(lines, "\n")
		},
	}
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func TestLayoutZonesReturnsExactHeight(t *testing.T) {
	zones := []zoneSpec{
		fixedZone("header", 3, 1),
		bodyZone("body"),
		fixedZone("status", 1, 2),
	}
	for _, h := range []int{4, 5, 10, 30, 100} {
		out := layoutZones(zones, 80, h)
		if got := countLines(out); got != h {
			t.Errorf("height=%d: got %d lines, want %d\n%s", h, got, h, out)
		}
	}
}

func TestLayoutZonesDropPriority(t *testing.T) {
	zones := []zoneSpec{
		fixedZone("header", 3, 1), // drops first
		bodyZone("body"),
		fixedZone("status", 1, 2), // drops second
	}
	cases := []struct {
		height       int
		wantHeader   bool
		wantStatus   bool
		wantBodyMin  int
	}{
		{height: 5, wantHeader: true, wantStatus: true, wantBodyMin: 1},
		{height: 4, wantHeader: true, wantStatus: false, wantBodyMin: 1}, // drops status (sum non-fill = 4 == h, body would be 0)
		{height: 3, wantHeader: false, wantStatus: false, wantBodyMin: 1}, // drops both: only body + (no chrome) — wait revisit
		{height: 2, wantHeader: false, wantStatus: false, wantBodyMin: 1},
		{height: 1, wantHeader: false, wantStatus: false, wantBodyMin: 1},
	}
	for _, c := range cases {
		out := layoutZones(zones, 80, c.height)
		if got := strings.Contains(out, "header-line-A"); got != c.wantHeader {
			t.Errorf("h=%d header presence = %v, want %v\n%s", c.height, got, c.wantHeader, out)
		}
		if got := strings.Contains(out, "status-line-A"); got != c.wantStatus {
			t.Errorf("h=%d status presence = %v, want %v\n%s", c.height, got, c.wantStatus, out)
		}
		if got := strings.Count(out, "body-row"); got < c.wantBodyMin {
			t.Errorf("h=%d body rows = %d, want >= %d\n%s", c.height, got, c.wantBodyMin, out)
		}
	}
}

func TestLayoutZonesPadsShortContent(t *testing.T) {
	// Header advertises 3 lines but the renderer (here: static content)
	// produces exactly 3, body produces exactly 1, status 1. Total = 5.
	// Asking for height 8 must pad with 3 blank lines.
	zones := []zoneSpec{
		fixedZone("header", 3, 1),
		bodyZone("body"),
		fixedZone("status", 1, 2),
	}
	out := layoutZones(zones, 80, 8)
	if got := countLines(out); got != 8 {
		t.Errorf("lines = %d, want 8\n%s", got, out)
	}
	// Last 3 lines should be empty.
	lines := strings.Split(out, "\n")
	for i := 5; i < 8; i++ {
		if lines[i] != "" {
			t.Errorf("expected blank line at %d, got %q", i, lines[i])
		}
	}
}

func TestLayoutZonesTruncatesOverlongContent(t *testing.T) {
	// Body renderFill produces 10 rows but only 4 fit (height=8 - header 3 - status 1).
	zones := []zoneSpec{
		fixedZone("header", 3, 1),
		{
			name: "body", fill: true,
			renderFill: func(_ int) string {
				lines := make([]string, 10)
				for i := range lines {
					lines[i] = "body-row"
				}
				return strings.Join(lines, "\n")
			},
		},
		fixedZone("status", 1, 2),
	}
	out := layoutZones(zones, 80, 8)
	if got := countLines(out); got != 8 {
		t.Errorf("lines = %d, want 8\n%s", got, out)
	}
}

func TestLayoutZonesHeightZeroBypass(t *testing.T) {
	zones := []zoneSpec{
		fixedZone("header", 2, 1),
		bodyZone("body"),
		fixedZone("status", 1, 2),
	}
	out := layoutZones(zones, 80, 0)
	if !strings.Contains(out, "header-line-A") || !strings.Contains(out, "status-line-A") {
		t.Errorf("h=0 should emit zones unmodified for headless mode:\n%s", out)
	}
}
```

- [ ] **Step 2: Run tests; confirm they pass**

Run: `go test ./internal/tui/... -run "TestLayoutZones" -v` from `packages/claude-agents-tui`
Expected: PASS for all five tests.

If `TestLayoutZonesDropPriority` fails on the `h=4` row (wantStatus=false), the algorithm should be: with header(3) + status(1) + body, sum non-fill = 4, so body = 0. The drop loop removes status (lower dropOrder than header — wait, header has dropOrder 1 and status 2; smaller drops first, so header would drop). Re-examine: at h=4, dropping header gives sum=1, body=3 ≥ 1, OK. So header drops first. Update test to: `h=4 → wantHeader=false, wantStatus=true`. Adjust the expectations table accordingly:

```go
{height: 5, wantHeader: true,  wantStatus: true,  wantBodyMin: 1},
{height: 4, wantHeader: false, wantStatus: true,  wantBodyMin: 3}, // header drops, status stays, body=3
{height: 2, wantHeader: false, wantStatus: true,  wantBodyMin: 1}, // header dropped, body=1
{height: 1, wantHeader: false, wantStatus: false, wantBodyMin: 1}, // both dropped
```

Re-run after the fix.

---

### Task 3: Rewrite `View()` to use `layoutZones`

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/view.go`

- [ ] **Step 1: Replace `View()` body and remove `visualLineCount`**

Read the current `view.go` first to confirm it matches the diff base. Then replace its contents with:

```go
package tui

import (
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	if m.tree == nil {
		return "loading…"
	}
	if m.selected != nil {
		return wrap.Block(RenderDetails(m.selected, m.width), wrap.EffectiveWidth(m.width))
	}

	header := render.Header(m.tree, render.HeaderOpts{
		CaffeinateOn:    m.caffeinateOn,
		ShowAll:         m.showAll,
		CostMode:        m.costMode,
		ForceID:         m.forceID,
		Theme:           m.theme,
		AutoResume:      m.autoResume,
		WindowResetsAt:  m.tree.WindowResetsAt,
		AutoResumeDelay: m.autoResumeDelay,
		Width:           m.width,
	})
	status := render.Legend(m.width)

	zones := []zoneSpec{
		{name: "header", content: header, dropOrder: 1},
		{
			name: "body",
			fill: true,
			renderFill: func(h int) string {
				return m.renderBody(h)
			},
		},
		{name: "status", content: status, dropOrder: 2},
	}

	return layoutZones(zones, wrap.EffectiveWidth(m.width), m.height)
}

// renderBody returns up to `height` rows of session list content.
// When height is 0 (test/headless), all rows render.
func (m *Model) renderBody(height int) string {
	if len(m.flatRows) == 0 {
		return "No active sessions."
	}
	totalTok := 0
	for _, d := range m.tree.Dirs {
		totalTok += d.TotalTokens
	}
	opts := render.TreeOpts{
		ShowAll:            m.showAll,
		ForceID:            m.forceID,
		CostMode:           m.costMode,
		Width:              m.width,
		Cursor:             m.cursor,
		HasCursor:          m.selected == nil,
		Theme:              m.theme,
		TotalSessionTokens: totalTok,
	}
	if height <= 0 {
		return render.RenderWindowTree(m.pathNodes, m.flatRows, 0, 10000, opts)
	}
	return render.RenderWindowTree(m.pathNodes, m.flatRows, m.scrollOffset, height, opts)
}
```

- [ ] **Step 2: Build to verify compile**

Run: `go build ./...` from `packages/claude-agents-tui`
Expected: no errors.

If the compiler complains about an unused `lipgloss` or `strings` import, remove the offending line.

---

### Task 4: Extend `view_test.go` to assert exact line count

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/view_test.go`

- [ ] **Step 1: Add a height parameter + line-count check to `TestViewLineWidthInvariant`**

Locate the existing function. Replace the inner loop body with:

```go
	widths := []int{0, 30, 60, 80, 120, 200}
	heights := []int{0, 1, 2, 3, 4, 5, 10, 30}
	fixtures := []struct {
		name string
		make func() *Model
	}{
		{"no sessions", fixtureNoSessions},
		{"many sessions", fixtureManySessions},
		{"paused (rate-limited)", fixturePaused},
		{"detail panel open", fixtureDetailOpen},
		{"CJK first prompt", fixtureCJK},
		{"long PR title", fixtureLongPR},
	}

	for _, fx := range fixtures {
		for _, w := range widths {
			for _, h := range heights {
				name := fmt.Sprintf("%s @ width=%d height=%d", fx.name, w, h)
				t.Run(name, func(t *testing.T) {
					m := fx.make()
					if w > 0 {
						m.Update(tea.WindowSizeMsg{Width: w, Height: h})
					}
					out := m.View()

					if w == 0 {
						if out != "loading…" {
							t.Errorf("width=0 should defer; got %q", out)
						}
						return
					}

					ew := wrap.EffectiveWidth(w)
					for i, line := range strings.Split(out, "\n") {
						if got := lipgloss.Width(line); got > ew {
							t.Errorf("line %d width = %d, want <= %d (fixture=%q, w=%d, h=%d): %q",
								i, got, ew, fx.name, w, h, line)
						}
					}

					// Line count == height invariant. Detail panel is a single
					// non-zone string today and is exempt — theme A only owns
					// the main-tree path. h=0 is the headless bypass.
					if h > 0 && fx.name != "detail panel open" {
						if got := strings.Count(out, "\n") + 1; got != h {
							t.Errorf("line count = %d, want %d (fixture=%q, w=%d, h=%d):\n%s",
								got, h, fx.name, w, h, out)
						}
					}
				})
			}
		}
	}
```

- [ ] **Step 2: Run the extended test**

Run: `go test ./internal/tui/... -run "TestViewLineWidthInvariant" -count=1` from `packages/claude-agents-tui`
Expected: PASS.

Investigate any failure: print the offending output and verify it's not a renderer producing extra trailing `\n` characters. `padOrTruncate` should normalize, but a renderer that returns `"foo\n"` (trailing newline) becomes `["foo", ""]` after Split, which will inflate the line count by 1. If `render.Header` ends in `"\n"`, trim it before passing into `zoneSpec.content`.

If trimming is needed, modify `view.go` to strip the trailing newline:

```go
header := strings.TrimRight(render.Header(...), "\n")
status := strings.TrimRight(render.Legend(m.width), "\n")
```

(`render.Legend` does not currently return a trailing newline; `render.Header` does. The trim makes both predictable.)

---

### Task 5: Verify whole suite + commit

- [ ] **Step 1: Full test sweep**

Run from `packages/claude-agents-tui`:
```bash
go test ./... -count=1
go vet ./...
```
Expected: all packages PASS, vet silent.

- [ ] **Step 2: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/tui/layout.go \
        packages/claude-agents-tui/internal/tui/layout_test.go \
        packages/claude-agents-tui/internal/tui/view.go \
        packages/claude-agents-tui/internal/tui/view_test.go
git commit -m "$(cat <<'EOF'
feat(tui): zone-based layout returns exact height per frame

Replaces View()'s strings.Join + visualLineCount math with three named
zones (header, body, status) and a layoutZones helper that pads or
truncates the assembled output to exactly m.height lines whenever
m.width and m.height are both > 0. Drop priority resolves narrow
terminals: header drops first, then status; body never drops.

Implements docs/superpowers/specs/2026-05-08-tui-layout-skeleton-design.md
(theme A). Themes B–E (single-row controls/5h-block, alert split,
selection panel, modals) plug into the same helper without API changes.

Fixes the "top body row appears/disappears as the window edge is
dragged" report by eliminating the line-count disagreement between
visualLineCount and the actual emit path.
EOF
)"
```

---

## Commit 2 — Cursor selectable fix

### Task 6: Add `selectable` and `nextSelectable` helpers

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/model.go`

- [ ] **Step 1: Add helpers near `clampCursor`**

Insert this block right above `clampCursor` (around line 104):

```go
// selectable reports whether the cursor is allowed to land on a row.
// Blank separator rows are not selectable; sessions and path-tree nodes are.
func selectable(r render.Row) bool {
	return r.Kind != render.BlankKind
}

// nextSelectable scans rows starting at `from` in direction `dir` (+1 or -1)
// and returns the index of the first selectable row encountered. If no
// selectable row exists in that direction within bounds, it returns from
// unchanged so the caller's "stay put when at the edge" semantics work.
func nextSelectable(rows []render.Row, from, dir int) int {
	for i := from; i >= 0 && i < len(rows); i += dir {
		if selectable(rows[i]) {
			return i
		}
	}
	return from
}
```

- [ ] **Step 2: Update `clampCursor` to snap onto a selectable row**

Replace the existing `clampCursor` body with:

```go
func (m *Model) clampCursor() {
	n := len(m.flatRows)
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if !selectable(m.flatRows[m.cursor]) {
		// Try moving up first (preserves "stay close to where you were"); fall
		// back to scanning down if nothing selectable exists above.
		up := nextSelectable(m.flatRows, m.cursor, -1)
		if selectable(m.flatRows[up]) && up != m.cursor {
			m.cursor = up
			return
		}
		down := nextSelectable(m.flatRows, m.cursor, +1)
		if selectable(m.flatRows[down]) {
			m.cursor = down
		}
	}
}
```

- [ ] **Step 3: Build to confirm compile**

Run: `go build ./...` from `packages/claude-agents-tui`
Expected: no errors.

---

### Task 7: Update `down`/`j` and `up`/`k` to skip blank rows

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/update.go`

- [ ] **Step 1: Replace the `down`/`j` and `up`/`k` cases**

Locate around `update.go:49-57`. Replace:

```go
		case "down", "j":
			m.cursor++
			m.clampCursor()
			m.syncScroll()
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.syncScroll()
```

with:

```go
		case "down", "j":
			start := m.cursor + 1
			if start < len(m.flatRows) {
				m.cursor = nextSelectable(m.flatRows, start, +1)
			}
			m.clampCursor()
			m.syncScroll()
		case "up", "k":
			start := m.cursor - 1
			if start >= 0 {
				m.cursor = nextSelectable(m.flatRows, start, -1)
			}
			m.clampCursor()
			m.syncScroll()
```

`nextSelectable` returns `from` when nothing matches. When `start = m.cursor + 1` is out of bounds, the caller skips the call entirely so the cursor stays put — that's the "no selectable row below" behavior. `clampCursor` afterwards is the safety net (e.g., if `flatRows` shrank between events).

- [ ] **Step 2: Build**

Run: `go build ./...` from `packages/claude-agents-tui`
Expected: no errors.

---

### Task 8: Tests for cursor-overrun + skip-blank

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/model_test.go`

- [ ] **Step 1: Add the regression tests near the existing cursor tests**

Add at end of file:

```go
// TestCursorDownStopsAtLastSelectable verifies that pressing Down past the
// last selectable row leaves the cursor on that row (does not roll past or
// land on a BlankKind separator).
func TestCursorDownStopsAtLastSelectable(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "s1", Status: session.Working}},
			{Session: &session.Session{SessionID: "s2", Status: session.Working}},
			{Session: &session.Session{SessionID: "s3", Status: session.Working}},
		},
		WorkingN: 3,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})

	for range 50 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	if m.cursor < 0 || m.cursor >= len(m.flatRows) {
		t.Fatalf("cursor out of bounds: %d (len=%d)", m.cursor, len(m.flatRows))
	}
	if m.flatRows[m.cursor].Kind == render.BlankKind {
		t.Errorf("cursor parked on BlankKind row at idx=%d", m.cursor)
	}
}

// TestCursorUpStopsAtFirstSelectable verifies the symmetric Up case.
func TestCursorUpStopsAtFirstSelectable(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "s1", Status: session.Working}},
			{Session: &session.Session{SessionID: "s2", Status: session.Working}},
		},
		WorkingN: 2,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.cursor = len(m.flatRows) - 1
	m.clampCursor() // ensure starting cursor is on a selectable row

	for range 50 {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}

	if m.flatRows[m.cursor].Kind == render.BlankKind {
		t.Errorf("cursor parked on BlankKind row at idx=%d", m.cursor)
	}
}

// TestClampCursorSnapsOffBlankRow verifies that if external state mutates
// the cursor onto a BlankKind row, clampCursor moves it to the nearest
// selectable row.
func TestClampCursorSnapsOffBlankRow(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "s1", Status: session.Working}},
		},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})

	// Find a BlankKind row index, force cursor onto it, then clamp.
	blankIdx := -1
	for i, r := range m.flatRows {
		if r.Kind == render.BlankKind {
			blankIdx = i
			break
		}
	}
	if blankIdx < 0 {
		t.Skip("fixture produced no blank row; clampCursor snap behavior cannot be exercised here")
	}
	m.cursor = blankIdx
	m.clampCursor()

	if m.flatRows[m.cursor].Kind == render.BlankKind {
		t.Errorf("clampCursor failed to snap off blank row; cursor=%d", m.cursor)
	}
}
```

The existing tests in `model_test.go` already import `aggregate`, `session`, `render` — verify and add `tea` (`bubbletea`) if not present. Check the file's existing imports before adding.

- [ ] **Step 2: Run the new tests**

Run: `go test ./internal/tui/... -run "TestCursor|TestClampCursor" -v -count=1` from `packages/claude-agents-tui`
Expected: PASS for the three new tests.

If any fail, print the flatRows kinds (`fmt.Printf("%v\n", r.Kind)`) at the top of the test to see the actual layout the fixture produces.

---

### Task 9: Verify whole suite + commit

- [ ] **Step 1: Full test sweep**

Run from `packages/claude-agents-tui`:
```bash
go test ./... -count=1
go vet ./...
```
Expected: all PASS, vet silent.

- [ ] **Step 2: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/tui/model.go \
        packages/claude-agents-tui/internal/tui/update.go \
        packages/claude-agents-tui/internal/tui/model_test.go
git commit -m "$(cat <<'EOF'
fix(tui): cursor skips blank separator rows on Up/Down

down/j and up/k now use a nextSelectable helper that scans flatRows
in the requested direction and stops at the first non-BlankKind row.
clampCursor snaps off a blank row to the nearest selectable neighbor
so external state changes (poll result, tree rebuild) cannot leave
the cursor parked on an unrenderable row.

Fixes the "if I keep pressing Down it goes beyond the last session"
report. The cursor index space remains flatRows-relative, so the
renderer at internal/render/window.go is unchanged.
EOF
)"
```

---

## Self-Review Checklist (run before handing off)

- [ ] Spec coverage: every numbered subsection of the design doc has a task that delivers it (zone model → Tasks 1-3; height invariant → Task 4; visualLineCount removal → Task 3; cursor fix → Tasks 6-8).
- [ ] No placeholders (no "TBD", "TODO", "fill in", or vague hand-waves).
- [ ] All file paths absolute under `packages/claude-agents-tui/`.
- [ ] Type/identifier names consistent across tasks: `zoneSpec`, `layoutZones`, `selectable`, `nextSelectable`, `clampCursor`.
- [ ] Each task has TDD ordering (failing test → impl → passing test → commit) where the change is testable; layout helper tests are upfront in Task 2 because the helper is brand-new and useful to validate before the View rewrite consumes it.
- [ ] Two commits, both independently revertable.
