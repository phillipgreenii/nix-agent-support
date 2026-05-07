package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestLegendTierContent(t *testing.T) {
	cases := []struct {
		name    string
		width   int
		wantAll []string
	}{
		{
			name:    "wide",
			width:   140,
			wantAll: []string{"working", "idle", "paused", "awaiting", "dormant", "subagents", "shells", "branch", "nav", "details"},
		},
		{
			name:    "narrow",
			width:   100,
			wantAll: []string{"●working", "○idle", "⏸paused", "?awaiting", "✕dormant", "🤖subs", "🐚sh", "🌿br", "details"},
		},
		{
			name:    "tiny",
			width:   60,
			wantAll: []string{"●", "○", "⏸", "?", "✕", "🤖", "🐚", "🌿", "[↑↓]", "[enter]"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Legend(tc.width)
			for _, want := range tc.wantAll {
				if !strings.Contains(got, want) {
					t.Errorf("Legend(%d) missing %q in %q", tc.width, want, got)
				}
			}
		})
	}
}

func TestLegendFitsAtTierFloor(t *testing.T) {
	// Each tier's chosen string must fit within the tier's lower-bound width
	// so the boundary clip never has to chew on it inside that tier.
	cases := []struct {
		name    string
		width   int
		ceiling int
	}{
		{"tiny floor", 30, 30},
		{"narrow floor", 80, 80},
		{"wide floor", 120, 120},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Legend(c.width)
			if w := lipgloss.Width(got); w > c.ceiling {
				t.Errorf("Legend(%d) width = %d, want <= %d; got %q", c.width, w, c.ceiling, got)
			}
		})
	}
}
