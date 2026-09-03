package tui

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// TestBindingsToHelpRows_ListsEveryBinding: the [?] modal is generated
// FROM the Bindings table -- every row, in the same order, keys
// " | "-joined.
func TestBindingsToHelpRows_ListsEveryBinding(t *testing.T) {
	rows := bindingsToHelpRows()
	if len(rows) != len(Bindings) {
		t.Fatalf("bindingsToHelpRows returned %d rows, want %d (one per Binding)", len(rows), len(Bindings))
	}
	for i, b := range Bindings {
		wantKeys := strings.Join(b.Keys, " | ")
		if rows[i].Keys != wantKeys {
			t.Errorf("row %d Keys = %q, want %q", i, rows[i].Keys, wantKeys)
		}
		if rows[i].Description != b.Description {
			t.Errorf("row %d Description = %q, want %q", i, rows[i].Description, b.Description)
		}
	}
}

// TestHelpFooter_NamesVersionPairAndErrorLogPath is this packet's own
// Files entry for help.go: "footer carries the version pair + error-log
// path".
func TestHelpFooter_NamesVersionPairAndErrorLogPath(t *testing.T) {
	dir := t.TempDir()
	m := NewModel(Options{Version: "26.9.3", CacheDir: dir}, render.NewTheme(false))
	m.reply = StatusReply{Core: CoreInfo{Version: "26.9.1"}}

	got := m.helpFooter()
	if !strings.Contains(got, "26.9.3") {
		t.Errorf("helpFooter %q missing the TUI client's own version", got)
	}
	if !strings.Contains(got, "26.9.1") {
		t.Errorf("helpFooter %q missing the core's reported version", got)
	}
	if !strings.Contains(got, errorLogPath(dir)) {
		t.Errorf("helpFooter %q missing the error-log path %q", got, errorLogPath(dir))
	}
}

// TestHelpFooter_EmptyCacheDirOmitsErrorLogLine: nothing to point to when
// Options.CacheDir was never set.
func TestHelpFooter_EmptyCacheDirOmitsErrorLogLine(t *testing.T) {
	m := NewModel(Options{}, render.NewTheme(false))
	got := m.helpFooter()
	if strings.Contains(got, "Errors logged to") {
		t.Errorf("helpFooter %q names an error-log path with no CacheDir set", got)
	}
}

// TestNewModel_VersionDefaultsToDev mirrors pa-monitor's own
// Options.Version precedent: empty falls back to "dev".
func TestNewModel_VersionDefaultsToDev(t *testing.T) {
	m := NewModel(Options{}, render.NewTheme(false))
	if m.clientVersion != "dev" {
		t.Errorf("clientVersion = %q, want the \"dev\" fallback", m.clientVersion)
	}
}

// TestRenderModal_LegendAndGatesRouteToTheirOwnContent: renderModal must
// actually dispatch on activeModal rather than always rendering help.
func TestRenderModal_LegendAndGatesRouteToTheirOwnContent(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24

	m.activeModal = ModalLegend
	if got := m.renderModal(); !strings.Contains(got, "Legend") {
		t.Errorf("ModalLegend renderModal() = %q, want it to route through render.LegendModal", got)
	}

	m.activeModal = ModalGates
	if got := m.renderModal(); !strings.Contains(got, "quota-paused") {
		t.Errorf("ModalGates renderModal() = %q, want it to route through renderGatesModal", got)
	}

	m.activeModal = ModalNone
	if got := m.renderModal(); got != "" {
		t.Errorf("ModalNone renderModal() = %q, want empty", got)
	}
}
