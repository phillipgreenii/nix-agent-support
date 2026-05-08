# TUI Selection Panel + First-Prompt Removal (Theme D)

**Status:** Draft
**Date:** 2026-05-08
**Scope:** `packages/claude-agents-tui/internal/render/footer.go`, `packages/claude-agents-tui/internal/render/footer_test.go`, `packages/claude-agents-tui/internal/render/tree.go`, `packages/claude-agents-tui/internal/render/tree_test.go`, `packages/claude-agents-tui/internal/tui/view.go`

## Context

After themes A–C the TUI's footer renders `<legend>  <Updated HH:MM:SS>`. The legend was a placeholder; theme E is removing it entirely (replaced by an `[l]` modal). Theme D fills the left column with **selection status content** — when the cursor is on a session row, the footer shows that session's first prompt in dim style, occupying the full width up to the right-aligned Updated clock.

Once the selection panel exists, each session row no longer needs its own per-row first-prompt line (today's `↳ "first prompt …"` block under each session). Removing that line halves the vertical real estate per session and roughly doubles the visible session count per frame.

User intent (2026-05-08 brainstorm):

- "use the bottom row as a selection status area. for now it will be blank, unless the selector is on a session. when on a session, i will display the first prompt using the same dim display as the session row does today."
- "with this implemented, we will remove the first prompt line for the sessions. this will save a lot of vertical space."

Empty-prompt behavior (clarified): blank. No fallback to session id/name; no italicized hint.

## The contract

> The footer renders one line. The right column always shows Updated (per theme B's tier rules). The left column shows: the cursor-selected session's `FirstPrompt` in dim style when present, or blank otherwise. Session rows in the body emit exactly one line each (no per-row prompt continuation).

## Architecture

### Footer signature change

Today (post-theme B): `Footer(width int, updatedAt time.Time) string`.
After theme D: `Footer(width int, status string, updatedAt time.Time) string`.

`status` is pre-styled left-column content. Footer is a pure layout function — it does not consult themes, sessions, or models. The caller is responsible for formatting + clipping `status` to the available left-column width.

### Width budget (unchanged from theme B)

| Tier | Right column | Left column |
|------|--------------|-------------|
| WIDE ≥120 | `Updated 21:05:43` (16 cols) | `width − 16 − 2 (gap)` |
| NARROW 80–119 | `21:05:43` (8 cols) | `width − 8 − 2 (gap)` |
| TINY <80 | empty | `width` |

When `status == ""`, the left column is rendered as a blank styled span of the appropriate width (so the right column still hugs the right edge).

### `view.go` builds the status string

```go
status := ""
leftWidth := render.FooterLeftWidth(m.width)  // available cols for the status string
if 0 <= m.cursor && m.cursor < len(m.flatRows) {
    row := m.flatRows[m.cursor]
    if row.Kind == render.SessionKind && row.Session != nil {
        fp := row.Session.SessionEnrichment.FirstPrompt
        if fp != "" {
            // Single-line, dim style, clipped to the available left-column width.
            text := fmt.Sprintf("%q", wrap.Line(fp, leftWidth))
            status = m.theme.Prompt.Render(text)
        }
    }
}
footer := render.Footer(m.width, status, time.Now())
```

`FooterLeftWidth(width)` is a new exported helper in `internal/render/footer.go` that returns the available column count for the status string at the given tier. Both Footer and view.go consult it so the math is shared:

```go
// internal/render/footer.go
func FooterLeftWidth(width int) int {
    switch wrap.Tier(width) {
    case wrap.TierWide:
        return width - 16 - 2  // "Updated 21:05:43" + 2-col gap
    case wrap.TierNarrow:
        return width - 8 - 2   // "21:05:43" + 2-col gap
    default:
        return width  // TINY: full row, no clock
    }
}
```

### Session row no longer emits the prompt line

`internal/render/tree.go::renderSession` today ends with:

```go
out := fmt.Sprintf("%s%s %s  %s%s\n", ...)
if s.SessionEnrichment.FirstPrompt != "" {
    out += fmt.Sprintf("  %s    ↳ %s\n", cont,
        opts.Theme.Prompt.Render(fmt.Sprintf("%q", wrap.Line(s.SessionEnrichment.FirstPrompt, 80))))
}
return out
```

After theme D:

```go
return fmt.Sprintf("%s%s %s  %s%s\n", ...)
```

The `FirstPrompt`-conditional block is deleted. Sessions are exactly one line.

This affects `cont` (the continuation glyph used by the deleted line). `cont` is no longer used by `renderSession`; check call sites in `tree.go::Tree` and `internal/render/window.go::RenderWindowTree` to drop the parameter if it's now unused. (Confirm by `grep` — if `cont` is still used elsewhere, leave its plumbing alone and only remove the unused inner block.)

### Tests

**Footer tests (commit D1):**

- `TestFooterStatusFillsLeftColumn` — passing `status="fake-prompt"` at width=140 produces `<...fake-prompt...>  Updated HH:MM:SS`, total width=140, status content present.
- `TestFooterEmptyStatusKeepsClockAtRight` — passing `status=""` at width=140 still yields width=140 with clock right-aligned.
- `TestFooterStatusClipsAtTinyWithoutClock` — at width=60 (TINY), full width given to status; no clock.
- `TestFooterLeftWidthMatchesTier` — table-drives `footerLeftWidth(60)`, `footerLeftWidth(80)`, `footerLeftWidth(120)` and asserts the documented values.

**Session row tests (commit D2):**

- Existing `TestSessionRowShowsTail` and similar substring tests should pass unchanged since they assert presence of subagent/shell glyphs on the main session line — independent of FirstPrompt.
- Add `TestSessionRowOmitsFirstPromptContinuation` — render a session with a FirstPrompt, assert the output has exactly one `\n` (just the row terminator) and does NOT contain `↳`.
- View-level: extend an existing test fixture to put the cursor on a session-with-prompt and assert the footer line contains the prompt text.

## Migration / commits

Two commits:

1. **`feat(render): footer accepts selection status; expose footerLeftWidth`**
   - `Footer(width, status, updatedAt)` signature.
   - New exported `footerLeftWidth(width)` (or `LeftWidth(width)`) function.
   - All existing call sites (currently only `view.go` after theme B) updated to pass `""` for `status`.
   - Footer tests extended for the new column.
   - `renderSession` unchanged; per-row prompt line still rendered.

2. **`feat(tui): selection status shows first prompt; remove per-session prompt line`**
   - `view.go` computes the status string from cursor position and passes to `Footer`.
   - `renderSession` drops the per-row prompt continuation.
   - `tree_test.go` adjusted: a test that previously expected the `↳` continuation should now assert its absence (or be deleted if its only purpose was the continuation).
   - View-level test asserts the footer reflects the cursor's prompt.

## Test plan

### Unit — `internal/render/footer_test.go` (commit D1)

```go
func TestFooterStatusFillsLeftColumn(t *testing.T) {
    out := Footer(140, "fake-prompt-text", time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC))
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
    out := Footer(140, "", time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC))
    if !strings.HasSuffix(out, "Updated 21:05:43") {
        t.Errorf("clock should hug right edge with empty status: %q", out)
    }
    if w := lipgloss.Width(out); w != 140 {
        t.Errorf("width = %d, want 140", w)
    }
}

func TestFooterStatusClipsAtTinyWithoutClock(t *testing.T) {
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
        {200, 200 - 16 - 2}, // WIDE: 182
        {120, 120 - 16 - 2}, // WIDE floor: 102
        {100, 100 - 8 - 2},  // NARROW: 90
        {80,  80 - 8 - 2},   // NARROW floor: 70
        {60,  60},           // TINY: full
    }
    for _, c := range cases {
        if got := FooterLeftWidth(c.width); got != c.want {
            t.Errorf("FooterLeftWidth(%d) = %d, want %d", c.width, got, c.want)
        }
    }
}
```

### Unit — `internal/render/tree_test.go` (commit D2)

```go
func TestSessionRowOmitsFirstPromptContinuation(t *testing.T) {
    s := &aggregate.SessionView{
        Session: &session.Session{Name: "n", SessionID: "id", Status: session.Working},
        SessionEnrichment: aggregate.SessionEnrichment{
            FirstPrompt: "this prompt should NOT appear under the row anymore",
            SessionTokens: 100,
            Model: "claude-opus-4-7",
        },
    }
    d := &aggregate.Directory{Path: "/p", Sessions: []*aggregate.SessionView{s}, WorkingN: 1}
    out := Tree(&aggregate.Tree{Dirs: []*aggregate.Directory{d}}, TreeOpts{TotalSessionTokens: 100, Width: 120})

    if strings.Contains(out, "↳") {
        t.Errorf("session row should no longer emit the ↳ continuation; got:\n%s", out)
    }
    if strings.Contains(out, "this prompt should NOT appear") {
        t.Errorf("FirstPrompt content leaked into the body:\n%s", out)
    }
}
```

### Integration — `internal/tui/view_test.go` (commit D2)

```go
func TestViewFooterShowsSelectedSessionFirstPrompt(t *testing.T) {
    sv := &aggregate.SessionView{
        Session: &session.Session{Name: "n", SessionID: "id", Status: session.Working},
        SessionEnrichment: aggregate.SessionEnrichment{
            FirstPrompt: "selected prompt content",
            SessionTokens: 100,
            Model: "claude-opus-4-7",
        },
    }
    d := &aggregate.Directory{Path: "/p", Sessions: []*aggregate.SessionView{sv}, WorkingN: 1}
    m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
    m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
    m.cursor = nextSelectable(m.flatRows, 0, +1)  // first selectable row

    out := m.View()

    // Footer is the last line. Confirm it contains the prompt text.
    lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
    footer := lines[len(lines)-1]
    if !strings.Contains(footer, "selected prompt content") {
        t.Errorf("footer should show selected session's first prompt:\n%s", out)
    }
}

func TestViewFooterBlankWhenCursorOnPathNode(t *testing.T) {
    // Build a tree with a path node + session; put cursor on the path node row.
    // Assert the footer left side has no prompt content (only the clock).
    // (Implementation detail: nextSelectable already lands on PathNodeKind for
    // the first row of a multi-level tree. Test verifies that case.)
}
```

`TestViewLineWidthInvariant` continues to pass — Footer width invariant unchanged. `TestViewNoPhantomBlankRowsBetweenZones` continues to pass — no new zones added.

### Integration — `internal/tui/details_test.go` (no change needed)

Theme D doesn't touch the detail panel. Existing details tests stay green.

## Out of scope (theme E / future)

- `[?]` help modal and `[l]` legend modal (theme E).
- Multi-line first-prompt overflow with explicit ellipsis customization.
- Selecting a path-node row should show aggregate metadata in the footer (e.g., directory branch, PR title). Could be a follow-up after theme E.

## Followups noted

- The legend is no longer rendered anywhere after theme D. If theme E is delayed, users lose the per-symbol cheatsheet entirely. Consider adding a stub `[l]` keybinding in theme D that shows a TUI message ("legend moved to [l] in theme E") to bridge the gap. **Decision**: don't bridge — theme E is the next theme and ships the modal infrastructure. Symbol legend disappears for one theme cycle, then reappears as a popup. The TUI's six glyphs (●○⏸?✕) are recognizable enough that brief absence is acceptable.
- After theme D, the only consumer of `render.Legend` is theme E's modal (when it lands). Until then, `render.Legend` becomes dead code with one consumer (footer) calling it indirectly through `render.Footer`. Wait — Footer no longer calls `Legend` after theme D. Verify: the Footer signature change in commit D1 means `Footer` no longer knows about the legend. So `render.Legend` has zero callers between theme D and theme E. Decision: keep `Legend` and its tests; theme E will consume them via the modal.
