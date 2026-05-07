package render

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
)

// TestHeaderTierContent verifies that each width tier emits the expected key
// hints / labels for both the controls line and the auto-resume line.
func TestHeaderTierContent(t *testing.T) {
	tree := &aggregate.Tree{}
	cases := []struct {
		name      string
		width     int
		wantAll   []string
		wantNone  []string
	}{
		{
			name:     "wide",
			width:    140,
			wantAll:  []string{"[C]affeinate:", "tokens", "cost", "active", "all", "name", "id", "[q]", "[R] auto-resume:", "[M] resume now"},
			wantNone: []string{"[t]tok", "[a]act"},
		},
		{
			name:     "narrow",
			width:    100,
			wantAll:  []string{"[C] ", "[t] tok", "cost", "[a] act", "all", "[n] nm", "id", "[q]", "[R] auto:", "[M] resume"},
			wantNone: []string{"[C]affeinate:", "tokens", "active", "[M] resume now"},
		},
		{
			name:     "tiny",
			width:    60,
			wantAll:  []string{"[C]", "[t]", "[a]", "[n]", "[q]", "[R]", "[M]now"},
			wantNone: []string{"[C]affeinate:", "[C] ●", "tokens", "active", "auto:", "auto-resume:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Header(tree, HeaderOpts{Width: tc.width})
			for _, want := range tc.wantAll {
				if !strings.Contains(h, want) {
					t.Errorf("tier=%s missing %q in header:\n%s", tc.name, want, h)
				}
			}
			for _, none := range tc.wantNone {
				if strings.Contains(h, none) {
					t.Errorf("tier=%s should not contain %q in header:\n%s", tc.name, none, h)
				}
			}
		})
	}
}
