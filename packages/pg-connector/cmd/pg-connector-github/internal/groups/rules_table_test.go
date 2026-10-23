package groups

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
)

// The GitHub rules are a groupkit.Table built from the settings: runs takes
// ci_active_ttl and ci_max_age, and every group of All() has a spec.
func TestRulesTableIsBuiltFromSettings(t *testing.T) {
	s := config.Defaults().Settings
	tbl := NewRules(s).Table()
	if tbl.TerminalTTL != s.TerminalTTL.Std() {
		t.Errorf("TerminalTTL = %v, want %v", tbl.TerminalTTL, s.TerminalTTL.Std())
	}
	if got := tbl.TTL(Runs); got != s.CIActiveTTL.Std() || got == 0 {
		t.Errorf("runs TTL = %v, want ci_active_ttl %v", got, s.CIActiveTTL.Std())
	}
	if got := tbl.MaxAge(Runs); got != s.CIMaxAge.Std() {
		t.Errorf("runs MaxAge = %v, want %v", got, s.CIMaxAge.Std())
	}
	if len(tbl.Groups) != len(All()) {
		t.Fatalf("table lists %d groups, want %d", len(tbl.Groups), len(All()))
	}
	for _, g := range All() {
		if _, ok := tbl.Spec(g); !ok {
			t.Errorf("table has no spec for %s", g)
		}
	}
	for g, want := range map[string]time.Duration{Summary: s.SummaryTTL.Std(), Detail: s.DetailTTL.Std(), Conversation: s.ConversationTTL.Std(), Pending: s.PendingTTL.Std(), Files: 0, Commits: 0} {
		if got := tbl.TTL(g); got != want {
			t.Errorf("%s TTL = %v, want %v", g, got, want)
		}
	}
}

// A group the rules do not list is refreshed in the background, as before the
// rules were a table.
func TestRulesBackgroundOfUnknownGroup(t *testing.T) {
	r := NewRules(config.Defaults().Settings)
	if !r.Background("example-unknown") {
		t.Error("an unlisted group must be background-refreshed")
	}
	if r.Stamped("example-unknown") || r.TTL("example-unknown") != 0 || r.MaxAge("example-unknown") != 0 {
		t.Error("an unlisted group has no TTL, max age or stamps")
	}
}
