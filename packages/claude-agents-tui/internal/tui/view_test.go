package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

// TestViewLineWidthInvariant is the contract test for the View boundary clip:
// every line of (*Model).View() output is <= effectiveWidth(width). At width=0
// the model must defer rendering and return the literal "loading…".
func TestViewLineWidthInvariant(t *testing.T) {
	widths := []int{0, 30, 60, 80, 120, 200}
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
			name := fmt.Sprintf("%s @ width=%d", fx.name, w)
			t.Run(name, func(t *testing.T) {
				m := fx.make()
				if w > 0 {
					m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
				}
				out := m.View()

				if w == 0 {
					if out != "loading…" {
						t.Errorf("width=0 should defer rendering; got %q", out)
					}
					return
				}

				ew := wrap.EffectiveWidth(w)
				for i, line := range strings.Split(out, "\n") {
					if got := lipgloss.Width(line); got > ew {
						t.Errorf("line %d width = %d, want <= %d (fixture=%q, width=%d): %q",
							i, got, ew, fx.name, w, line)
					}
				}
			})
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
