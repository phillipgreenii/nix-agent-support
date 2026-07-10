package render

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// A session blocked on human input (Blocked + AwaitingInput) must render the
// distinct "?" glyph — the legend documents "? = awaiting / blocked on human
// input", but symbol() previously rendered every Blocked session as the generic
// ◐, so "?" only ever appeared in a dead-pid edge case (legend/render mismatch).
// Other blockers keep the ◐ base (usage-limit short-circuits to ⏸; error/auth
// get overlaid by sessionGlyph).
func TestSymbolGlyphs(t *testing.T) {
	th := NewTheme(false)
	cases := []struct {
		name                           string
		st                             session.Status
		dormant, awaiting, rateLimited bool
		want                           string
	}{
		{"blocked+awaiting=human-input", session.Blocked, false, true, false, "?"},
		{"blocked+other", session.Blocked, false, false, false, "◐"},
		{"idle+dormant", session.Idle, true, false, false, "☾"},
		{"idle", session.Idle, false, false, false, "○"},
		{"working", session.Working, false, false, false, "●"},
		{"rate-limited", session.Blocked, false, false, true, "⏸"},
	}
	for _, c := range cases {
		got := symbol(c.st, c.dormant, c.awaiting, c.rateLimited, th)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: symbol()=%q, want it to contain %q", c.name, got, c.want)
		}
	}
}
