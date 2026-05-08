# TUI Help + Legend Modals (Theme E)

**Status:** Draft
**Date:** 2026-05-08
**Scope:** `packages/claude-agents-tui/internal/render/modals.go` (new), `packages/claude-agents-tui/internal/tui/keybindings.go` (new), `packages/claude-agents-tui/internal/tui/update.go`, `packages/claude-agents-tui/internal/tui/model.go`, `packages/claude-agents-tui/internal/tui/view.go`

## Context

Theme B added a `[?]` literal to the controls row but no modal infrastructure. Theme D removed the always-visible legend. Theme E supplies the missing pieces: a `[?]` help modal and an `[l]` legend modal, both centered, bordered, scrollable, dismissable with `[esc]`.

User constraint (2026-05-08 brainstorm): the help text must not duplicate keybindings. A code change to add or change a keybinding must auto-reflect in the help modal — no two-step "update update.go AND help.md" pattern.

This drives the architecture: a single canonical `[]Binding` list in `internal/tui` is consulted by both the dispatch code and the help modal renderer. The legend stays hand-curated (symbol meanings aren't tied to dispatch).

## The contract

> Every key the TUI handles is registered exactly once in `internal/tui.Bindings`. `update.go` dispatches by iterating that list. `HelpModal` renders the same list as a scrollable popup. Adding a new keybinding requires editing `Bindings` and nothing else.

## Architecture

### Single canonical keybinding list

```go
// internal/tui/keybindings.go
package tui

type Binding struct {
    Keys        []string             // bubbletea key strings, e.g. ["down", "j"], ["ctrl+c", "q"]
    Description string               // shown in the [?] modal
    Handle      func(*Model) tea.Cmd // returns a tea.Cmd if any (nil otherwise)
}

var Bindings = []Binding{
    {Keys: []string{"?"},              Description: "Help",                       Handle: handleOpenHelp},
    {Keys: []string{"l"},              Description: "Legend",                     Handle: handleOpenLegend},
    {Keys: []string{"q", "ctrl+c"},    Description: "Quit",                       Handle: handleQuit},
    {Keys: []string{"down", "j"},      Description: "Cursor / scroll down",       Handle: handleDown},
    {Keys: []string{"up", "k"},        Description: "Cursor / scroll up",         Handle: handleUp},
    {Keys: []string{"enter"},          Description: "Open session details",       Handle: handleEnter},
    {Keys: []string{"space", "right"}, Description: "Toggle path-tree collapse",  Handle: handleExpandToggle},
    {Keys: []string{"left", "h"},      Description: "Collapse current path node", Handle: handleCollapse},
    {Keys: []string{"esc"},            Description: "Close detail panel / modal", Handle: handleEsc},
    {Keys: []string{"t"},              Description: "Toggle tokens / cost",       Handle: handleToggleCost},
    {Keys: []string{"a"},              Description: "Toggle active / all",       Handle: handleToggleAll},
    {Keys: []string{"n"},              Description: "Toggle name / id",          Handle: handleToggleID},
    {Keys: []string{"C"},              Description: "Toggle caffeinate",         Handle: handleToggleCaffeinate},
    {Keys: []string{"R"},              Description: "Toggle auto-resume",        Handle: handleToggleAutoResume},
    {Keys: []string{"M"},              Description: "Manually fire resume",      Handle: handleManualResume},
}
```

Each `handleXxx` is a function with body equivalent to today's `update.go` switch case. They live in `keybindings.go` (or a sibling `update_handlers.go`) — the choice of placement is incidental, but they share the same package.

`l` repurposes from today's "expand path node" to "open legend modal." Expand path node continues to work via `right` and `space`. Acceptable because `right` is the more commonly-used alternative and the user has explicitly requested the `[l]` legend keybinding.

### `update.go` dispatch

The existing big `switch msg.String() { case ... }` block in `update.go::Update` is replaced with iteration over `Bindings`:

```go
case tea.KeyMsg:
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
    // No matching binding — no-op.
    return m, nil
```

Each handler accepts `*Model` and returns `tea.Cmd` (or nil). Most handlers mutate `m` in place and return nil; `handleQuit` returns `tea.Quit`; `handleToggleAutoResume` may return a `tea.Batch` of fire/tick commands (today's behavior preserved).

### Modal state in `Model`

```go
type ModalKind int

const (
    ModalNone ModalKind = iota
    ModalHelp
    ModalLegend
)

type Model struct {
    // existing fields...
    activeModal       ModalKind
    modalScrollOffset int
}
```

When `activeModal != ModalNone`, the cursor key handlers (`handleDown`, `handleUp`) scroll the modal instead of moving the row cursor. `handleEsc` closes the modal (priority over closing the detail panel).

### Modal renderers

```go
// internal/render/modals.go

// ModalRow is one (left, right) pair the modal renders as a single line:
//   "<left, padded-right>  <right>"
type ModalRow struct {
    Left  string
    Right string
}

// Modal renders a centered, bordered, scrollable popup.
//
// width, height: terminal dimensions; the popup occupies the full screen as a
// "full-screen takeover" frame, with the bordered box centered inside.
//
// scroll skips the first `scroll` rows. The function clamps so scroll cannot
// hide all content; rows below the visible window are summarized as
// "↓ N more" in the box's last visible line; rows above the visible window
// summarized as "↑ N more" at the top.
//
// Footer hint inside the box: "[esc] close   [↑↓] scroll".
func Modal(title string, rows []ModalRow, width, height, scroll int) string

// HelpModal is a thin wrapper over Modal with a "Help — keybindings" title.
type HelpRow struct {
    Keys        string  // pre-joined: "down | j"
    Description string
}

func HelpModal(rows []HelpRow, width, height, scroll int) string {
    mrows := make([]ModalRow, len(rows))
    for i, r := range rows {
        mrows[i] = ModalRow{Left: r.Keys, Right: r.Description}
    }
    return Modal("Help — keybindings", mrows, width, height, scroll)
}

// LegendModal is a thin wrapper over Modal with a hand-curated symbol list.
func LegendModal(width, height, scroll int) string {
    return Modal("Legend — symbols", legendRows, width, height, scroll)
}

var legendRows = []ModalRow{
    {Left: "●",  Right: "working    actively producing output"},
    {Left: "○",  Right: "idle       waiting for input"},
    {Left: "⏸",  Right: "paused     rate-limited"},
    {Left: "?",  Right: "awaiting   asked for clarification"},
    {Left: "✕",  Right: "dormant    ended (resumable)"},
    {Left: "🤖", Right: "subagents  count of subagent tool uses"},
    {Left: "🐚", Right: "shells     count of subshell tool uses"},
    {Left: "🌿", Right: "branch     directory's git branch"},
}
```

The popup is bordered using lipgloss's `Border()` style with `BorderStyle(lipgloss.RoundedBorder())`. Width is min(width − 4, max content width + padding); height is min(height − 4, content rows + 4). Centered via lipgloss `Place(width, height, lipgloss.Center, lipgloss.Center, ...)`.

### `view.go` dispatch

```go
func (m *Model) View() string {
    if m.width == 0 || m.tree == nil { return "loading…" }

    if m.activeModal != ModalNone {
        return wrap.Block(m.renderModal(), wrap.EffectiveWidth(m.width))
    }

    if m.selected != nil {
        return wrap.Block(RenderDetails(m.selected, m.width), wrap.EffectiveWidth(m.width))
    }

    // ... existing zone-based render unchanged ...
}

func (m *Model) renderModal() string {
    switch m.activeModal {
    case ModalHelp:
        return render.HelpModal(bindingsToHelpRows(), m.width, m.height, m.modalScrollOffset)
    case ModalLegend:
        return render.LegendModal(m.width, m.height, m.modalScrollOffset)
    }
    return ""
}

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

### Drift safeguard

Unit test asserts every binding has a non-empty Description and at least one Key. Adding a new binding without docs fails the test:

```go
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
```

## Migration / commits

Two commits, both independently revertable:

1. **`feat(tui): extract keybindings to a single canonical list`**
   - New `internal/tui/keybindings.go` with `Binding` type, `Bindings` list, and one `handleXxx` function per binding.
   - `update.go::Update` switch replaced with the iteration loop.
   - Behavior identical to today (no modal opening yet — the `?` and `l` handlers are stubs that do nothing in this commit; commit 2 adds the modal opening behavior).
   - Drift safeguard test added.
   - Existing `model_test.go` cursor + key tests continue to pass.

2. **`feat(tui,render): [?] help modal + [l] legend modal`**
   - `internal/render/modals.go` new with `Modal`, `HelpModal`, `LegendModal`.
   - `internal/render/modals_test.go` new.
   - `Model.activeModal` and `Model.modalScrollOffset` fields added.
   - `handleOpenHelp`, `handleOpenLegend` activate the modal; `handleEsc` closes it (priority over detail panel close); `handleDown`/`handleUp` scroll the modal when active.
   - `view.go` dispatches to `m.renderModal()` when active.
   - Tests: modal opens via `?`/`l`, closes via `esc`, content includes all Bindings entries, scroll respects bounds.

## Test plan

### Unit — `internal/tui/keybindings_test.go` (commit 1)

`TestBindingsAllDocumented` (above).

`TestUpdateDispatchesViaBindings` — for each binding's first Key, simulate `tea.KeyMsg` with that key string, assert the corresponding state change occurred (e.g., pressing `t` flips `m.costMode`). Effectively the existing `TestQuitKey` and friends continue to work; this is a sanity replay over a few representative bindings.

### Unit — `internal/render/modals_test.go` (commit 2)

```go
func TestModalRendersTitleAndContent(t *testing.T) {
    rows := []ModalRow{{Left: "?", Right: "Help"}, {Left: "q", Right: "Quit"}}
    out := Modal("Test Title", rows, 80, 30, 0)
    if !strings.Contains(out, "Test Title") {
        t.Errorf("expected title, got:\n%s", out)
    }
    for _, r := range rows {
        if !strings.Contains(out, r.Right) {
            t.Errorf("expected %q, got:\n%s", r.Right, out)
        }
    }
    if !strings.Contains(out, "[esc] close") {
        t.Errorf("expected esc hint, got:\n%s", out)
    }
}

func TestModalScrollOffsetSkipsRows(t *testing.T) {
    rows := make([]ModalRow, 50)
    for i := range rows {
        rows[i] = ModalRow{Left: fmt.Sprintf("k%d", i), Right: fmt.Sprintf("desc%d", i)}
    }
    // height ~10 rows visible; scroll past the first 5
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
        t.Errorf("expected '↑ N more' indicator at scroll=5, got:\n%s", out)
    }
    if !strings.Contains(out, "↓") {
        t.Errorf("expected '↓ N more' indicator with overflow, got:\n%s", out)
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
```

`TestHelpModalContainsAllBindings` (in `view_test.go` since `Bindings` is in `tui` package):

```go
func TestHelpModalContainsAllBindings(t *testing.T) {
    rows := bindingsToHelpRows()
    out := render.HelpModal(rows, 120, 40, 0)
    for _, b := range Bindings {
        if !strings.Contains(out, b.Description) {
            t.Errorf("help modal missing %q (Keys=%v); got:\n%s", b.Description, b.Keys, out)
        }
    }
}
```

`TestLegendModalContainsAllSymbols`:

```go
func TestLegendModalContainsAllSymbols(t *testing.T) {
    out := render.LegendModal(120, 40, 0)
    for _, sym := range []string{"●", "○", "⏸", "?", "✕", "🤖", "🐚", "🌿"} {
        if !strings.Contains(out, sym) {
            t.Errorf("legend modal missing %q; got:\n%s", sym, out)
        }
    }
}
```

### Integration — `internal/tui/view_test.go` (commit 2)

```go
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

func TestLOpensLegendModal(t *testing.T) {
    m := NewModel(Options{Tree: &aggregate.Tree{}})
    m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
    m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
    if m.activeModal != ModalLegend {
        t.Errorf("l should open legend modal, activeModal = %v", m.activeModal)
    }
}

func TestEscClosesModal(t *testing.T) {
    m := NewModel(Options{Tree: &aggregate.Tree{}})
    m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
    m.activeModal = ModalHelp
    m.Update(tea.KeyMsg{Type: tea.KeyEsc})
    if m.activeModal != ModalNone {
        t.Errorf("esc should close modal, got %v", m.activeModal)
    }
}

func TestModalScrollDoesNotMoveCursor(t *testing.T) {
    // Set up with multiple selectable rows; open modal; press down.
    // m.cursor must NOT change; m.modalScrollOffset must increment.
}
```

## Out of scope

- Mouse close-on-outside-click.
- PageUp/PageDown/Home/End scroll.
- Search/filter inside modals.
- Auto-extracted legend (legend stays hand-curated).
- Modal animation (fade-in/out).

## Followups

- Once `Bindings` exists, future themes can compose more advanced helpers (e.g., chord bindings) by wrapping `Handle` with state machines. Theme E gets the foundation in.
- `?` and `l` are added to the `Bindings` list in commit 1 with stub `Handle` functions that do nothing. Commit 2 fills in the modal-opening logic.
