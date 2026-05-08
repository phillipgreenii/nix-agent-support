# TUI Help + Legend Modals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract all key dispatch into a single canonical `[]Binding` list and add scrollable, centered Help/Legend modal renderers driven by that same list (so adding a key updates both dispatch and docs in one edit).

**Architecture:** `internal/tui/keybindings.go` owns `Binding` (Keys, Description, Handle) and `Bindings`. `update.go::Update` iterates `Bindings` instead of switching on `msg.String()`. `internal/render/modals.go` exposes `Modal` (centered, bordered, scrollable popup), `HelpModal` (rendered from caller-supplied rows), and `LegendModal` (hand-curated symbol list). View() short-circuits to the modal renderer when `m.activeModal != ModalNone`.

**Tech Stack:** Go, bubbletea, lipgloss.

---

## Spec reference

Implements `docs/superpowers/specs/2026-05-08-tui-modals-design.md`.

## File structure

| File | Status | Responsibility |
|------|--------|----------------|
| `packages/claude-agents-tui/internal/tui/keybindings.go` | new | `Binding` type + `Bindings` list + per-binding `handleXxx` functions |
| `packages/claude-agents-tui/internal/tui/keybindings_test.go` | new | Drift safeguard + dispatch sanity |
| `packages/claude-agents-tui/internal/tui/update.go` | modify | Switch replaced by `Bindings` iteration |
| `packages/claude-agents-tui/internal/tui/model.go` | modify | Add `activeModal ModalKind` + `modalScrollOffset int` fields |
| `packages/claude-agents-tui/internal/tui/view.go` | modify | Dispatch to `m.renderModal()` when active; bindingsToHelpRows helper |
| `packages/claude-agents-tui/internal/tui/view_test.go` | modify | Modal open/close/scroll integration tests |
| `packages/claude-agents-tui/internal/render/modals.go` | new | `Modal`, `HelpModal`, `LegendModal`, `ModalRow`, `HelpRow`, `legendRows` |
| `packages/claude-agents-tui/internal/render/modals_test.go` | new | Modal title/content/scroll/dimension tests |

---

## Commit 1 — Single dispatch table

### Task 1: Create `keybindings.go` with `Binding` + handlers

**Files:**
- Create: `packages/claude-agents-tui/internal/tui/keybindings.go`

- [ ] **Step 1: Write `keybindings.go`**

```go
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

// Binding registers one keybinding with its display description and handler.
// All TUI dispatch flows through Bindings — adding a new key means appending
// here, and the [?] help modal picks it up automatically.
type Binding struct {
	Keys        []string             // bubbletea key strings; e.g. ["down", "j"], ["ctrl+c", "q"]
	Description string               // shown in [?] modal
	Handle      func(*Model) tea.Cmd // returns a tea.Cmd if any (nil otherwise)
}

// Bindings is the canonical, ordered keybinding list. Order matters: first
// match wins in dispatch, and rows render in this order in the help modal.
var Bindings = []Binding{
	{Keys: []string{"?"}, Description: "Help", Handle: handleOpenHelp},
	{Keys: []string{"l"}, Description: "Legend", Handle: handleOpenLegend},
	{Keys: []string{"q", "ctrl+c"}, Description: "Quit", Handle: handleQuit},
	{Keys: []string{"down", "j"}, Description: "Cursor / scroll down", Handle: handleDown},
	{Keys: []string{"up", "k"}, Description: "Cursor / scroll up", Handle: handleUp},
	{Keys: []string{"enter"}, Description: "Open session details", Handle: handleEnter},
	{Keys: []string{"space", "right"}, Description: "Toggle path-tree collapse", Handle: handleExpandToggle},
	{Keys: []string{"left", "h"}, Description: "Collapse current path node", Handle: handleCollapse},
	{Keys: []string{"esc"}, Description: "Close detail panel / modal", Handle: handleEsc},
	{Keys: []string{"t"}, Description: "Toggle tokens / cost", Handle: handleToggleCost},
	{Keys: []string{"a"}, Description: "Toggle active / all", Handle: handleToggleAll},
	{Keys: []string{"n"}, Description: "Toggle name / id", Handle: handleToggleID},
	{Keys: []string{"C"}, Description: "Toggle caffeinate", Handle: handleToggleCaffeinate},
	{Keys: []string{"R"}, Description: "Toggle auto-resume", Handle: handleToggleAutoResume},
	{Keys: []string{"M"}, Description: "Manually fire resume", Handle: handleManualResume},
}

// --- Handlers (one per Binding) ---

func handleOpenHelp(m *Model) tea.Cmd {
	// Stub for commit 1; commit 2 wires modal state.
	return nil
}

func handleOpenLegend(m *Model) tea.Cmd {
	// Stub for commit 1; commit 2 wires modal state.
	return nil
}

func handleQuit(m *Model) tea.Cmd {
	return tea.Quit
}

func handleDown(m *Model) tea.Cmd {
	start := m.cursor + 1
	if start < len(m.flatRows) {
		m.cursor = nextSelectable(m.flatRows, start, +1)
	}
	m.clampCursor()
	m.syncScroll()
	return nil
}

func handleUp(m *Model) tea.Cmd {
	start := m.cursor - 1
	if start >= 0 {
		m.cursor = nextSelectable(m.flatRows, start, -1)
	}
	m.clampCursor()
	m.syncScroll()
	return nil
}

func handleEnter(m *Model) tea.Cmd {
	if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.SessionKind {
		m.selected = row.Session
	}
	return nil
}

func handleExpandToggle(m *Model) tea.Cmd {
	if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind {
		m.treeState.Toggle(row.NodePath)
		if m.cacheDir != "" {
			_ = m.treeState.Save(m.cacheDir)
		}
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
	}
	return nil
}

func handleCollapse(m *Model) tea.Cmd {
	if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind && !row.Collapsed {
		m.treeState.Toggle(row.NodePath)
		if m.cacheDir != "" {
			_ = m.treeState.Save(m.cacheDir)
		}
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
	}
	return nil
}

func handleEsc(m *Model) tea.Cmd {
	m.selected = nil
	return nil
}

func handleToggleCost(m *Model) tea.Cmd {
	m.costMode = !m.costMode
	return nil
}

func handleToggleAll(m *Model) tea.Cmd {
	m.showAll = !m.showAll
	m.rebuildFlatRows()
	m.clampCursor()
	m.syncScroll()
	return nil
}

func handleToggleID(m *Model) tea.Cmd {
	m.forceID = !m.forceID
	return nil
}

func handleToggleCaffeinate(m *Model) tea.Cmd {
	m.caffeinateOn = !m.caffeinateOn
	return nil
}

func handleToggleAutoResume(m *Model) tea.Cmd {
	m.autoResume = !m.autoResume
	if m.autoResume && !m.tree.WindowResetsAt.IsZero() && !m.autoResumeFired {
		fireAt := m.tree.WindowResetsAt.Add(m.autoResumeDelay)
		cmds := []tea.Cmd{autoResumeFireCmd(fireAt)}
		if !m.countdownTick {
			m.countdownTick = true
			cmds = append(cmds, countdownTickCmd())
		}
		return tea.Batch(cmds...)
	}
	return nil
}

func handleManualResume(m *Model) tea.Cmd {
	m.signalNonWorking("manual-resume")
	return nil
}
```

- [ ] **Step 2: Build to confirm compile**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build ./...
```

Expected: success. The new file co-exists with the existing `update.go` switch — no duplicate dispatch yet (that's Task 2).

---

### Task 2: Replace `update.go`'s key switch with `Bindings` iteration

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/update.go`

- [ ] **Step 1: Replace the `tea.KeyMsg` case body**

Locate around `update.go:18-100`. Replace the entire `case tea.KeyMsg:` block with:

```go
	case tea.KeyMsg:
		if isQuit(msg) {
			return m, tea.Quit
		}
		s := msg.String()
		for _, b := range Bindings {
			for _, k := range b.Keys {
				if k == s {
					if cmd := b.Handle(m); cmd != nil {
						return m, cmd
					}
					return m, nil
				}
			}
		}
		// No matching binding — no-op fall-through.
```

The existing `isQuit(msg)` early-return stays as a defensive fast path for `q`/`ctrl+c` (also covered by `Bindings`, but `isQuit` is the long-standing source). Keep it as-is.

The rest of the `Update` function (`tea.WindowSizeMsg`, `pollTickMsg`, `pollResultMsg`, etc.) is untouched.

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 3: Run TUI tests**

```bash
go test ./internal/tui/... -count=1
```

Expected: PASS. Existing key tests (`TestQuitKey`, `TestDownArrowMovesCursor`, `TestSyncScrollAdvancesOffsetWhenCursorExitsViewport`, etc.) should pass identically — they fire `tea.KeyMsg` and observe state changes; the dispatch path is the only thing that changed.

If a test fails, common cause: the `tea.KeyMsg` for keys like `?`, `space`, `enter` may serialize as different `msg.String()` values than expected. Run with `-v` and inspect what `msg.String()` returns for the failing case. Adjust the `Bindings` `Keys` list if a string differs (e.g., `tea.KeySpace` may render as `" "` not `"space"`). Most bubbletea string forms are documented at https://github.com/charmbracelet/bubbletea/blob/master/key.go.

---

### Task 3: Drift safeguard + dispatch sanity tests

**Files:**
- Create: `packages/claude-agents-tui/internal/tui/keybindings_test.go`

- [ ] **Step 1: Write tests**

```go
package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
)

func TestBindingsAllDocumented(t *testing.T) {
	for i, b := range Bindings {
		if len(b.Keys) == 0 {
			t.Errorf("Bindings[%d] has no Keys", i)
		}
		if b.Description == "" {
			t.Errorf("Bindings[%d] (Keys=%v) missing Description", i, b.Keys)
		}
		if b.Handle == nil {
			t.Errorf("Bindings[%d] (Keys=%v) missing Handle", i, b.Keys)
		}
	}
}

func TestDispatchTViaBindings(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	want := !m.costMode
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if m.costMode != want {
		t.Errorf("pressing t should toggle costMode to %v, got %v", want, m.costMode)
	}
}

func TestDispatchAViaBindings(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	want := !m.showAll
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.showAll != want {
		t.Errorf("pressing a should toggle showAll to %v, got %v", want, m.showAll)
	}
}

func TestDispatchQViaBindings(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Errorf("pressing q should return tea.Quit cmd, got nil")
	}
}
```

- [ ] **Step 2: Run tests**

```bash
go test ./internal/tui/... -run "TestBindings|TestDispatch" -v -count=1
```

Expected: all PASS.

- [ ] **Step 3: Run full suite + vet**

```bash
go test ./... -count=1
go vet ./...
```

Expected: all packages PASS, vet silent.

- [ ] **Step 4: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/tui/keybindings.go \
        packages/claude-agents-tui/internal/tui/keybindings_test.go \
        packages/claude-agents-tui/internal/tui/update.go
git commit -m "$(cat <<'EOF'
feat(tui): single canonical keybinding list

Adds internal/tui.Bindings — a list of {Keys, Description, Handle}
entries that is the single source of truth for all key dispatch in the
TUI. update.go's switch is replaced by iteration over Bindings; one
handler function per binding holds the body that previously lived in
each switch case.

The next commit consumes Bindings to render the [?] help modal so
adding a new key requires editing exactly one place.

[?] and [l] bindings are added with stub Handle functions in this
commit; commit 2 fills them in to open the help and legend modals.

Implements docs/superpowers/specs/2026-05-08-tui-modals-design.md
(theme E, commit 1 of 2).
EOF
)"
```

---

## Commit 2 — Modal infrastructure + activation

### Task 4: Create `render/modals.go` + tests

**Files:**
- Create: `packages/claude-agents-tui/internal/render/modals.go`
- Create: `packages/claude-agents-tui/internal/render/modals_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/render/modals_test.go
package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestModalRendersTitleAndContent(t *testing.T) {
	rows := []ModalRow{{Left: "?", Right: "Help"}, {Left: "q", Right: "Quit"}}
	out := Modal("Test Title", rows, 80, 30, 0)
	if !strings.Contains(out, "Test Title") {
		t.Errorf("expected title in output, got:\n%s", out)
	}
	for _, r := range rows {
		if !strings.Contains(out, r.Right) {
			t.Errorf("expected %q in output, got:\n%s", r.Right, out)
		}
	}
	if !strings.Contains(out, "[esc] close") {
		t.Errorf("expected esc hint in output, got:\n%s", out)
	}
}

func TestModalScrollOffsetSkipsRows(t *testing.T) {
	rows := make([]ModalRow, 50)
	for i := range rows {
		rows[i] = ModalRow{Left: fmt.Sprintf("k%d", i), Right: fmt.Sprintf("desc%d", i)}
	}
	out := Modal("t", rows, 80, 15, 5)
	if strings.Contains(out, "k0") {
		t.Errorf("k0 should be scrolled past, got:\n%s", out)
	}
	if !strings.Contains(out, "k5") {
		t.Errorf("k5 should be visible after scroll=5, got:\n%s", out)
	}
}

func TestModalShowsScrollIndicators(t *testing.T) {
	rows := make([]ModalRow, 50)
	for i := range rows {
		rows[i] = ModalRow{Left: "k", Right: "d"}
	}
	out := Modal("t", rows, 80, 15, 5)
	if !strings.Contains(out, "↑") {
		t.Errorf("expected '↑' indicator at scroll=5, got:\n%s", out)
	}
	if !strings.Contains(out, "↓") {
		t.Errorf("expected '↓' indicator with overflow, got:\n%s", out)
	}
}

func TestModalDimensionsClampToTerminal(t *testing.T) {
	out := Modal("t", []ModalRow{{Left: "x", Right: "y"}}, 60, 20, 0)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 20 {
		t.Errorf("expected 20 lines (height), got %d", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("line %d width %d > 60: %q", i, w, l)
		}
	}
}

func TestLegendModalContainsAllSymbols(t *testing.T) {
	out := LegendModal(120, 40, 0)
	for _, sym := range []string{"●", "○", "⏸", "?", "✕", "🤖", "🐚", "🌿"} {
		if !strings.Contains(out, sym) {
			t.Errorf("legend modal missing %q; got:\n%s", sym, out)
		}
	}
}

func TestHelpModalRendersGivenRows(t *testing.T) {
	rows := []HelpRow{
		{Keys: "down | j", Description: "Cursor down"},
		{Keys: "esc", Description: "Close"},
	}
	out := HelpModal(rows, 120, 40, 0)
	for _, r := range rows {
		if !strings.Contains(out, r.Keys) {
			t.Errorf("missing keys %q in output:\n%s", r.Keys, out)
		}
		if !strings.Contains(out, r.Description) {
			t.Errorf("missing description %q in output:\n%s", r.Description, out)
		}
	}
}
```

- [ ] **Step 2: Run tests (verify failure)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/render/... -run "TestModal|TestLegendModal|TestHelpModal" -v
```

Expected: FAIL with `Modal undefined`, `ModalRow undefined`, `LegendModal undefined`, `HelpModal undefined`, `HelpRow undefined`.

- [ ] **Step 3: Write `modals.go`**

```go
// internal/render/modals.go
package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ModalRow is one (left, right) pair displayed as a single line inside the modal.
type ModalRow struct {
	Left  string
	Right string
}

// HelpRow is a {keys, description} pair the help modal renders.
type HelpRow struct {
	Keys        string
	Description string
}

// Modal renders a centered, bordered, scrollable popup. The popup occupies
// the full screen as a "full-screen takeover" frame; the bordered box sits
// centered inside.
//
// Returns exactly `height` newline-separated lines, each clipped to `width`.
//
// scroll skips the first `scroll` content rows. Indicators appear on the
// box's first/last visible content line when content extends above/below
// the visible window:
//
//   ↑ N more
//   ↓ N more
//
// The box's footer always shows: "[esc] close   [↑↓] scroll".
func Modal(title string, rows []ModalRow, width, height, scroll int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	// Box dimensions: ~80% of available, with a minimum.
	boxWidth := width - 4
	if boxWidth > 80 {
		boxWidth = 80
	}
	if boxWidth < 20 {
		boxWidth = width
	}
	boxHeight := height - 4
	if boxHeight < 5 {
		boxHeight = height
	}

	// Inner area (inside border): width-2, height-2 for borders + 2 reserved
	// rows (title + footer hint).
	contentWidth := boxWidth - 2
	contentHeight := boxHeight - 4 // top border + title + bottom border + footer hint
	if contentHeight < 1 {
		contentHeight = 1
	}

	// Clamp scroll.
	if scroll < 0 {
		scroll = 0
	}
	maxScroll := len(rows) - contentHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	// Render visible rows.
	var visibleRows []string
	hasMoreAbove := scroll > 0
	hasMoreBelow := scroll+contentHeight < len(rows)

	for i := scroll; i < scroll+contentHeight && i < len(rows); i++ {
		r := rows[i]
		// Left column right-padded so right column starts at a fixed offset.
		// Left budget: 12 cols (most key combos fit; longer ones still render but
		// shift the right column).
		leftCol := lipgloss.NewStyle().Width(12).Render(r.Left)
		visibleRows = append(visibleRows, leftCol+r.Right)
	}

	// Replace first / last visible row with scroll indicator if applicable.
	if hasMoreAbove && len(visibleRows) > 0 {
		visibleRows[0] = fmt.Sprintf("↑ %d more", scroll)
	}
	if hasMoreBelow && len(visibleRows) > 0 {
		below := len(rows) - (scroll + contentHeight)
		visibleRows[len(visibleRows)-1] = fmt.Sprintf("↓ %d more", below)
	}

	// Pad to contentHeight.
	for len(visibleRows) < contentHeight {
		visibleRows = append(visibleRows, "")
	}

	// Compose the box content: title + blank + rows + footer hint.
	titleStyled := lipgloss.NewStyle().Bold(true).Render(title)
	footerHint := "[esc] close   [↑↓] scroll"

	var content strings.Builder
	content.WriteString(titleStyled)
	content.WriteString("\n")
	for _, r := range visibleRows {
		// Clip each row to contentWidth to avoid overflow.
		if lipgloss.Width(r) > contentWidth {
			// ANSI-aware: Modal callers don't use ANSI in left/right today,
			// so simple rune-aware slice via lipgloss.Width is fine. For
			// future-proofing we could route through wrap.Line.
			r = r[:contentWidth]
		}
		content.WriteString(r)
		content.WriteString("\n")
	}
	content.WriteString(footerHint)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(contentWidth).
		Render(content.String())

	// Center the box inside the full screen.
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// HelpModal is a thin wrapper over Modal with a "Help — keybindings" title.
// Caller passes pre-built HelpRow slice (typically derived from tui.Bindings).
func HelpModal(rows []HelpRow, width, height, scroll int) string {
	mrows := make([]ModalRow, len(rows))
	for i, r := range rows {
		mrows[i] = ModalRow{Left: r.Keys, Right: r.Description}
	}
	return Modal("Help — keybindings", mrows, width, height, scroll)
}

// legendRows is the hand-curated symbol list shown by LegendModal.
var legendRows = []ModalRow{
	{Left: "●", Right: "working    actively producing output"},
	{Left: "○", Right: "idle       waiting for input"},
	{Left: "⏸", Right: "paused     rate-limited"},
	{Left: "?", Right: "awaiting   asked for clarification"},
	{Left: "✕", Right: "dormant    ended (resumable)"},
	{Left: "🤖", Right: "subagents  count of subagent tool uses"},
	{Left: "🐚", Right: "shells     count of subshell tool uses"},
	{Left: "🌿", Right: "branch     directory's git branch"},
}

// LegendModal renders the hand-curated symbol legend.
func LegendModal(width, height, scroll int) string {
	return Modal("Legend — symbols", legendRows, width, height, scroll)
}
```

- [ ] **Step 4: Run tests (verify pass)**

```bash
go test ./internal/render/... -run "TestModal|TestLegendModal|TestHelpModal" -v -count=1
```

Expected: PASS.

If `TestModalDimensionsClampToTerminal` fails because `lipgloss.Place` produces fewer or more lines than `height`, adjust the Place call to use `lipgloss.NewStyle().Width(width).Height(height).Align(lipgloss.Center, lipgloss.Center).Render(box)` instead of `lipgloss.Place`.

If `TestModalScrollOffsetSkipsRows` fails because content rows don't fit at height=15 (box is too small), increase the test's height to 20 or accept fewer content rows.

---

### Task 5: Add `Model.activeModal` + `ModalKind` enum

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/model.go`

- [ ] **Step 1: Add `ModalKind` enum and Model fields**

In `model.go`, add after the imports:

```go
// ModalKind selects which full-screen modal is currently open.
type ModalKind int

const (
	ModalNone ModalKind = iota
	ModalHelp
	ModalLegend
)
```

Then in the `Model` struct, add the two fields (alongside the existing ones):

```go
type Model struct {
	// existing fields...
	activeModal       ModalKind
	modalScrollOffset int
}
```

Place them logically — e.g., near `selected` since they relate to view mode.

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: clean build (the new fields are unused; that's OK at this step).

---

### Task 6: Wire modal handlers — open/close + scroll

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/keybindings.go`

- [ ] **Step 1: Replace stub `handleOpenHelp` and `handleOpenLegend`**

Locate the stub bodies in `keybindings.go` and replace with:

```go
func handleOpenHelp(m *Model) tea.Cmd {
	m.activeModal = ModalHelp
	m.modalScrollOffset = 0
	return nil
}

func handleOpenLegend(m *Model) tea.Cmd {
	m.activeModal = ModalLegend
	m.modalScrollOffset = 0
	return nil
}
```

- [ ] **Step 2: Modify `handleEsc` to close modal first, fall through to detail panel**

Replace `handleEsc`:

```go
func handleEsc(m *Model) tea.Cmd {
	if m.activeModal != ModalNone {
		m.activeModal = ModalNone
		return nil
	}
	m.selected = nil
	return nil
}
```

- [ ] **Step 3: Modify `handleDown` and `handleUp` to scroll modal when active**

Replace `handleDown`:

```go
func handleDown(m *Model) tea.Cmd {
	if m.activeModal != ModalNone {
		m.modalScrollOffset++
		// Caller (Modal renderer) clamps; the model's offset is allowed to
		// overshoot temporarily.
		return nil
	}
	start := m.cursor + 1
	if start < len(m.flatRows) {
		m.cursor = nextSelectable(m.flatRows, start, +1)
	}
	m.clampCursor()
	m.syncScroll()
	return nil
}
```

Replace `handleUp`:

```go
func handleUp(m *Model) tea.Cmd {
	if m.activeModal != ModalNone {
		if m.modalScrollOffset > 0 {
			m.modalScrollOffset--
		}
		return nil
	}
	start := m.cursor - 1
	if start >= 0 {
		m.cursor = nextSelectable(m.flatRows, start, -1)
	}
	m.clampCursor()
	m.syncScroll()
	return nil
}
```

- [ ] **Step 4: Build**

```bash
go build ./...
```

Expected: clean build.

---

### Task 7: View() dispatches to the modal renderer

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/view.go`

- [ ] **Step 1: Add modal short-circuit + helper**

In `view.go`'s `View()` function, after the `m.tree == nil` check and before the detail-panel check, add:

```go
	if m.activeModal != ModalNone {
		return wrap.Block(m.renderModal(), wrap.EffectiveWidth(m.width))
	}
```

Then add the helper methods at the end of the file:

```go
// renderModal returns the full-screen modal content for the active modal.
func (m *Model) renderModal() string {
	switch m.activeModal {
	case ModalHelp:
		return render.HelpModal(bindingsToHelpRows(), m.width, m.height, m.modalScrollOffset)
	case ModalLegend:
		return render.LegendModal(m.width, m.height, m.modalScrollOffset)
	}
	return ""
}

// bindingsToHelpRows converts Bindings into the (Keys, Description) pairs the
// help modal renders. Keys are " | "-joined for display.
func bindingsToHelpRows() []render.HelpRow {
	out := make([]render.HelpRow, 0, len(Bindings))
	for _, b := range Bindings {
		out = append(out, render.HelpRow{
			Keys:        strings.Join(b.Keys, " | "),
			Description: b.Description,
		})
	}
	return out
}
```

You'll need to add `"strings"` to the imports if not present.

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: clean build.

---

### Task 8: Integration tests for modal open/close/scroll

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/view_test.go`

- [ ] **Step 1: Add tests at the end of `view_test.go`**

```go
// TestQuestionMarkOpensHelpModal — pressing ? sets activeModal=ModalHelp.
func TestQuestionMarkOpensHelpModal(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.activeModal != ModalHelp {
		t.Errorf("? should open help modal, activeModal = %v", m.activeModal)
	}
	out := m.View()
	if !strings.Contains(out, "Help — keybindings") {
		t.Errorf("view should render help title:\n%s", out)
	}
}

// TestLOpensLegendModal — pressing l sets activeModal=ModalLegend.
func TestLOpensLegendModal(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if m.activeModal != ModalLegend {
		t.Errorf("l should open legend modal, activeModal = %v", m.activeModal)
	}
	out := m.View()
	if !strings.Contains(out, "Legend — symbols") {
		t.Errorf("view should render legend title:\n%s", out)
	}
}

// TestEscClosesModalBeforeDetailPanel — esc closes modal first.
func TestEscClosesModalBeforeDetailPanel(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.activeModal = ModalHelp
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.activeModal != ModalNone {
		t.Errorf("esc should close modal, got %v", m.activeModal)
	}
}

// TestModalScrollDoesNotMoveCursor — when modal active, j/k scroll modal not cursor.
func TestModalScrollDoesNotMoveCursor(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
		},
		WorkingN: 2,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.activeModal = ModalHelp

	cursorBefore := m.cursor
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.cursor != cursorBefore {
		t.Errorf("cursor moved while modal active: was %d, now %d", cursorBefore, m.cursor)
	}
	if m.modalScrollOffset != 2 {
		t.Errorf("modalScrollOffset = %d, want 2", m.modalScrollOffset)
	}
}

// TestHelpModalContainsAllBindings — every binding's description appears in [?] modal.
func TestHelpModalContainsAllBindings(t *testing.T) {
	rows := bindingsToHelpRows()
	out := render.HelpModal(rows, 200, 60, 0)
	for _, b := range Bindings {
		if !strings.Contains(out, b.Description) {
			t.Errorf("help modal missing %q (Keys=%v); got:\n%s", b.Description, b.Keys, out)
		}
	}
}
```

You'll need imports for `tea` and `render` and `aggregate` and `session` and `strings` — check the file's existing imports first; theme A and theme D added many of these already.

- [ ] **Step 2: Run new integration tests**

```bash
go test ./internal/tui/... -run "TestQuestionMark|TestLOpens|TestEsc|TestModalScroll|TestHelpModalContains" -v -count=1
```

Expected: PASS.

If `TestQuestionMarkOpensHelpModal` fails because the `?` key string doesn't match `Bindings[0].Keys`, debug by printing `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}.String()`. Adjust either the test message construction or the `Bindings` Key string to match.

---

### Task 9: Verify suite + commit

- [ ] **Step 1: Full sweep**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./... -count=1
go vet ./...
```

Expected: all packages PASS, vet silent.

- [ ] **Step 2: Manual sanity (optional)**

Build and try the interactive TUI in your terminal. Press `?` to see the help modal, `esc` to close, `l` for legend, arrow keys to scroll. Press `q` to quit.

```bash
go build -o /tmp/cat-tui ./cmd/claude-agents-tui
/tmp/cat-tui
```

Expected: modals appear centered, scroll on arrow keys, dismiss on esc.

- [ ] **Step 3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/render/modals.go \
        packages/claude-agents-tui/internal/render/modals_test.go \
        packages/claude-agents-tui/internal/tui/model.go \
        packages/claude-agents-tui/internal/tui/keybindings.go \
        packages/claude-agents-tui/internal/tui/view.go \
        packages/claude-agents-tui/internal/tui/view_test.go
git commit -m "$(cat <<'EOF'
feat(tui,render): [?] help modal + [l] legend modal

Adds full-screen takeover modals for help and legend, both centered,
bordered, and scrollable via arrow keys. [esc] dismisses; cursor
navigation is suspended while a modal is active and resumes on close.

The help modal renders from internal/tui.Bindings (introduced in commit
1) so it auto-reflects every keybinding without duplicating content.
The legend modal is hand-curated since symbol meanings are not
dispatch-driven.

Implements docs/superpowers/specs/2026-05-08-tui-modals-design.md
(theme E, commit 2 of 2).
EOF
)"
```

---

## Self-Review Checklist (run before handing off)

- [ ] Spec coverage:
  - Single canonical `Bindings` (Tasks 1–3, commit 1).
  - Modal infrastructure `Modal` / `HelpModal` / `LegendModal` (Task 4).
  - `Model.activeModal` + `modalScrollOffset` (Task 5).
  - Modal open/close/scroll handlers (Task 6).
  - View dispatch (Task 7).
  - Integration tests (Task 8).
- [ ] No placeholders.
- [ ] Type / identifier consistency: `Binding`, `Bindings`, `ModalKind`, `ModalNone`, `ModalHelp`, `ModalLegend`, `Modal`, `ModalRow`, `HelpModal`, `HelpRow`, `LegendModal`, `bindingsToHelpRows`, `renderModal`. All consistent.
- [ ] TDD ordering preserved per task.
- [ ] Two commits, both independently revertable. Commit 1 leaves the TUI behaving identically (handlers extracted, `?` and `l` are no-ops); commit 2 activates the modals.
