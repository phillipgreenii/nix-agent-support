package render

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
)

func TestHeaderContainsBlockAndBurn(t *testing.T) {
	tree := &aggregate.Tree{
		CCUsageProbed: true,
		PlanCapUSD:    90,
		ActiveBlock: &ccusage.Block{
			CostUSD:    27,
			EndTime:    time.Date(2026, 4, 23, 22, 0, 0, 0, time.UTC),
			BurnRate:   ccusage.BurnRate{TokensPerMinute: 100_000, CostPerHour: 6},
			Projection: ccusage.Projection{RemainingMinutes: 180},
		},
	}
	h := Header(tree, HeaderOpts{CaffeinateOn: true, GraceRemaining: 55 * time.Second})
	if !strings.Contains(h, "30%") {
		t.Errorf("expected 30%% in header, got:\n%s", h)
	}
	if !strings.Contains(h, "$27") {
		t.Errorf("expected $27 in header, got:\n%s", h)
	}
	if !strings.Contains(h, "55s") {
		t.Errorf("expected grace countdown, got:\n%s", h)
	}
}

func TestHeaderHidesTopupWhenNotReached(t *testing.T) {
	tree := &aggregate.Tree{
		CCUsageProbed: true,
		PlanCapUSD:    90,
		ActiveBlock:   &ccusage.Block{CostUSD: 10},
	}
	h := Header(tree, HeaderOpts{})
	if strings.Contains(strings.ToLower(h), "top-up") {
		t.Errorf("top-up line leaked when block under cap")
	}
}

func TestHeaderShowsLoadingWhenNotYetProbed(t *testing.T) {
	tree := &aggregate.Tree{PlanCapUSD: 90, CCUsageProbed: false}
	h := Header(tree, HeaderOpts{})
	if !strings.Contains(h, "5h Block") {
		t.Errorf("expected '5h Block' label, got:\n%s", h)
	}
	if !strings.Contains(h, "loading") {
		t.Errorf("expected 'loading' while not yet probed, got:\n%s", h)
	}
}

func TestHeaderShowsFallbackWhenCCUsageMissing(t *testing.T) {
	tree := &aggregate.Tree{PlanCapUSD: 90, CCUsageProbed: true, CCUsageErr: errors.New("exec: not found")}
	h := Header(tree, HeaderOpts{})
	if !strings.Contains(h, "5h Block") {
		t.Errorf("expected '5h Block' label even without ccusage, got:\n%s", h)
	}
	if !strings.Contains(h, "unavailable") || !strings.Contains(h, "ccusage") {
		t.Errorf("expected ccusage-unavailable fallback, got:\n%s", h)
	}
}

func TestHeaderShowsFallbackEvenWithoutPlanCap(t *testing.T) {
	// PlanCapUSD=0 MUST not hide the whole 5h block line when ActiveBlock is present.
	tree := &aggregate.Tree{
		CCUsageProbed: true,
		PlanCapUSD:    0,
		ActiveBlock:   &ccusage.Block{CostUSD: 4.2, EndTime: time.Date(2026, 4, 23, 22, 0, 0, 0, time.UTC)},
	}
	h := Header(tree, HeaderOpts{})
	if !strings.Contains(h, "5h Block") {
		t.Errorf("expected 5h Block line when plan cap unknown, got:\n%s", h)
	}
	if !strings.Contains(h, "$4.20") {
		t.Errorf("expected cost in fallback, got:\n%s", h)
	}
}

func TestHeaderIncludesRefreshTimestamp(t *testing.T) {
	tree := &aggregate.Tree{}
	now := time.Date(2026, 4, 23, 15, 4, 5, 0, time.UTC)
	h := Header(tree, HeaderOpts{Now: now})
	if !strings.Contains(h, "Updated 15:04:05") {
		t.Errorf("expected 'Updated 15:04:05' timestamp, got:\n%s", h)
	}
}

func TestHeaderToggleBothOptionsPresent(t *testing.T) {
	// Verify both sides of every toggle appear in the header regardless
	// of toggle state. This catches accidental omissions when state flips.
	tree := &aggregate.Tree{}
	cases := []struct {
		name    string
		opts    HeaderOpts
		wantAll []string
	}{
		{
			name:    "defaults",
			opts:    HeaderOpts{},
			wantAll: []string{"tokens", "cost", "active", "all", "name", "id", "[M] resume now"},
		},
		{
			name:    "CostMode+ShowAll+ForceID",
			opts:    HeaderOpts{CostMode: true, ShowAll: true, ForceID: true},
			wantAll: []string{"tokens", "cost", "active", "all", "name", "id", "[M] resume now"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Header(tree, tc.opts)
			for _, want := range tc.wantAll {
				if !strings.Contains(h, want) {
					t.Errorf("missing %q in header:\n%s", want, h)
				}
			}
		})
	}
}
