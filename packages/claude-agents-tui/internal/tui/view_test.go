package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

// TestViewLineWidthInvariant is the contract test for the View boundary clip:
// every line of (*Model).View() output is <= effectiveWidth(width). At width=0
// the model must defer rendering and return the literal "loading…".
func TestViewLineWidthInvariant(t *testing.T) {
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
}

// TestViewNoPhantomBlankRowsBetweenZones asserts that the output of View()
// does not contain consecutive empty lines between non-empty zones. This
// guards against a recurrence of the trailing-newline drift where
// render.Header's terminating "\n" produced "\n\n" after the layout join.
func TestViewNoPhantomBlankRowsBetweenZones(t *testing.T) {
	m := fixtureManySessions()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	out := m.View()

	lines := strings.Split(out, "\n")
	// A run of consecutive empty lines longer than 1 indicates a phantom row.
	for i := 1; i < len(lines)-1; i++ {
		if lines[i] == "" && lines[i+1] == "" {
			// Allow trailing padding only — last lines may be intentional padding.
			// A phantom blank lands somewhere in the middle.
			if i < len(lines)/2 {
				t.Errorf("phantom blank row at lines %d-%d:\n%s", i, i+1, out)
				return
			}
		}
	}
}

func fixtureNoSessions() *Model {
	tree := &aggregate.Tree{}
	return NewModel(Options{Tree: tree})
}

func fixtureManySessions() *Model {
	d := &aggregate.Directory{Path: "/some/long/project/path"}
	for i := range 20 {
		d.Sessions = append(d.Sessions, &aggregate.SessionView{
			Session: &session.Session{
				SessionID: fmt.Sprintf("session-id-%d", i),
				Name:      fmt.Sprintf("session-name-%d", i),
				Status:    session.Working,
			},
			SessionEnrichment: aggregate.SessionEnrichment{
				Model:         "claude-opus-4-7",
				ContextTokens: 50_000,
				BurnRateShort: 30_000,
				FirstPrompt:   "do the thing with the very long description that runs on",
			},
		})
		d.WorkingN++
	}
	return NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
}

func fixturePaused() *Model {
	resetsAt := time.Date(2026, 5, 7, 18, 0, 0, 0, time.UTC)
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{SessionID: "s1", Name: "paused", Status: session.Idle},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model:             "claude-sonnet-4-6",
					RateLimitResetsAt: resetsAt,
				},
			},
		},
	}
	return NewModel(Options{Tree: &aggregate.Tree{
		Dirs:           []*aggregate.Directory{d},
		WindowResetsAt: resetsAt,
	}})
}

func fixtureDetailOpen() *Model {
	sv := &aggregate.SessionView{
		Session: &session.Session{
			SessionID:    "abc-123",
			Name:         "selected-session",
			PID:          42,
			Cwd:          "/some/working/directory/with/some/depth",
			Kind:         "interactive",
			TerminalHost: "tmux",
		},
		SessionEnrichment: aggregate.SessionEnrichment{
			Model:         "claude-opus-4-7",
			ContextTokens: 80_000,
			FirstPrompt:   "first prompt that should display under the rule",
		},
	}
	d := &aggregate.Directory{Path: "/p", Sessions: []*aggregate.SessionView{sv}}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.selected = sv
	return m
}

func fixtureCJK() *Model {
	d := &aggregate.Directory{
		Path: "/cjk",
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{SessionID: "j1", Name: "日本語セッション", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model:       "claude-opus-4-7",
					FirstPrompt: "日本語のテストプロンプトでありますとても長くなるかもしれない文字列",
				},
			},
		},
		WorkingN: 1,
	}
	return NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
}

// TestViewRendersSessionsWhenCCUsageReportsNoActiveBlock guards against the
// regression where the body suppresses session rows once ccusage successfully
// probes and reports no active 5h block. Session data is primary; the missing
// 5h block is metadata already shown in the header.
func TestViewRendersSessionsWhenCCUsageReportsNoActiveBlock(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{SessionID: "id-1", Name: "alpha-session", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7",
				},
			},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{
		Dirs:          []*aggregate.Directory{d},
		CCUsageProbed: true, // probe completed
		ActiveBlock:   nil,  // ccusage reports no active block
		CCUsageErr:    nil,  // no error — it just reports empty
	}
	m := NewModel(Options{Tree: tree})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	out := m.View()

	if strings.Contains(out, "Sessions not shown") {
		t.Errorf("session list was suppressed by no-active-block gate; output:\n%s", out)
	}
	if !strings.Contains(out, "alpha-session") {
		t.Errorf("session name not rendered when ActiveBlock is nil; output:\n%s", out)
	}
	// The header should still surface the metadata so the user knows the block
	// is empty — that's the legitimate signal channel for this state.
	if !strings.Contains(out, "no active block") {
		t.Errorf("header should still show 'no active block' metadata; output:\n%s", out)
	}
}

// TestPollResultDoesNotResetCursorWhenBlockEmpty pairs with the gate fix:
// poll results with ActiveBlock=nil must not zero the user's cursor / selection.
func TestPollResultDoesNotResetCursorWhenBlockEmpty(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
			{Session: &session.Session{SessionID: "c", Status: session.Working}},
		},
		WorkingN: 3,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	m := NewModel(Options{Tree: tree})
	m.cursor = 2

	// Simulate a poll result with ccusage probed but no active block.
	probedTree := &aggregate.Tree{
		Dirs:          []*aggregate.Directory{d},
		CCUsageProbed: true,
		ActiveBlock:   nil,
		CCUsageErr:    nil,
	}
	m.Update(pollResultMsg{tree: probedTree})

	if m.cursor == 0 {
		t.Errorf("cursor was reset to 0 by the no-active-block branch; expected to be preserved")
	}
}

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
	// Cursor at the first SessionKind row (skip the path-node row).
	sessionIdx := -1
	for i, r := range m.flatRows {
		if r.Kind == render.SessionKind {
			sessionIdx = i
			break
		}
	}
	if sessionIdx < 0 {
		t.Fatalf("fixture produced no session row: %+v", m.flatRows)
	}
	m.cursor = sessionIdx

	out := m.View()

	// Find the footer line (the one with the Updated clock).
	var footer string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Updated ") {
			footer = line
			break
		}
	}
	if footer == "" {
		t.Fatalf("could not locate footer line in output:\n%s", out)
	}
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

func fixtureLongPR() *Model {
	d := &aggregate.Directory{
		Path:   "/p",
		Branch: "feature/very-long-branch-name-that-keeps-going",
		PRInfo: &session.PRInfo{
			Number: 9999,
			Title:  strings.Repeat("super-long-pr-title ", 8),
			URL:    "https://example.com/owner/repo/pull/9999",
		},
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{SessionID: "p1", Name: "n", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7",
				},
			},
		},
		WorkingN: 1,
	}
	return NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
}
