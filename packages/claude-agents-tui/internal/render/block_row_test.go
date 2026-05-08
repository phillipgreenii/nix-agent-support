package render

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
)

func TestBlockRowPreActiveStates(t *testing.T) {
	cases := []struct {
		name string
		tree *aggregate.Tree
		want string
	}{
		{"loading", &aggregate.Tree{CCUsageProbed: false}, "5h loading…"},
		{"unavailable", &aggregate.Tree{CCUsageProbed: true, CCUsageErr: errors.New("not found")}, "5h unavailable"},
		{"no active block", &aggregate.Tree{CCUsageProbed: true}, "5h no active block"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := BlockRow(tc.tree, BlockRowOpts{Width: 200, Now: time.Now()})
			if !strings.Contains(out, tc.want) {
				t.Errorf("expected %q in output, got: %q", tc.want, out)
			}
			if strings.Contains(out, "\n") {
				t.Errorf("BlockRow must be single line, got: %q", out)
			}
		})
	}
}

func TestBlockRowActiveBlockTiers(t *testing.T) {
	tree := &aggregate.Tree{
		CCUsageProbed: true,
		PlanCapUSD:    100,
		ActiveBlock: &ccusage.Block{
			CostUSD:    35,
			EndTime:    time.Date(2026, 5, 8, 1, 0, 0, 0, time.UTC),
			BurnRate:   ccusage.BurnRate{TokensPerMinute: 1_200_000, CostPerHour: 30},
			Projection: ccusage.Projection{RemainingMinutes: 90},
		},
	}
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		width    int
		wantAll  []string
		wantNone []string
	}{
		{
			name:     "wide",
			width:    140,
			wantAll:  []string{"5h", "█", "35%", "$35.00", "1.2M/m", "resets", "ex"},
			wantNone: []string{"5h Block"},
		},
		{
			name:     "narrow",
			width:    100,
			wantAll:  []string{"5h", "█", "35%", "$35.00", "resets", "ex"},
			wantNone: []string{"M/m", "5h Block"},
		},
		{
			name:     "tiny",
			width:    60,
			wantAll:  []string{"5h", "35%", "resets"},
			wantNone: []string{"█", "$", "M/m", "ex"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := BlockRow(tree, BlockRowOpts{Width: tc.width, Now: now})
			for _, want := range tc.wantAll {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in output (tier=%s):\n%s", want, tc.name, out)
				}
			}
			for _, none := range tc.wantNone {
				if strings.Contains(out, none) {
					t.Errorf("should not contain %q (tier=%s):\n%s", none, tc.name, out)
				}
			}
			if strings.Contains(out, "\n") {
				t.Errorf("BlockRow must be single line, got: %q", out)
			}
		})
	}
}

func TestBlockRowFitsAtTierFloor(t *testing.T) {
	tree := &aggregate.Tree{
		CCUsageProbed: true,
		PlanCapUSD:    100,
		ActiveBlock: &ccusage.Block{
			CostUSD:    35,
			EndTime:    time.Date(2026, 5, 8, 1, 0, 0, 0, time.UTC),
			BurnRate:   ccusage.BurnRate{TokensPerMinute: 1_200_000, CostPerHour: 30},
			Projection: ccusage.Projection{RemainingMinutes: 90},
		},
	}
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	for _, w := range []int{60, 80, 120} {
		got := BlockRow(tree, BlockRowOpts{Width: w, Now: now})
		if width := lipgloss.Width(got); width > w {
			t.Errorf("BlockRow(%d) width = %d, want <= %d; got %q", w, width, w, got)
		}
	}
}
