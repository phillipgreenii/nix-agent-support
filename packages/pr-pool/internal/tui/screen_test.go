package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// TestScreen_ContractOrdering pins the exact iota ordering the Contract
// gives verbatim [design: Task 4.5 Interfaces] -- every later sibling
// packet (Tasks 4.6-4.9) extends Update/View by switching on these same
// values, so a silent reorder would be a breaking change nothing else here
// would catch.
func TestScreen_ContractOrdering(t *testing.T) {
	cases := []struct {
		s    screen
		want int
	}{
		{screenLoading, 0},
		{screenNoCore, 1},
		{screenMain, 2},
		{screenDrillDown, 3},
		{screenModal, 4},
		{screenQuiescing, 5},
	}
	for _, c := range cases {
		if int(c.s) != c.want {
			t.Errorf("screen %v = %d, want %d", c.s, int(c.s), c.want)
		}
	}
}

// TestScreen_String names every value distinctly, which is what makes a
// failed t.Fatalf("screen = %v, ...") elsewhere in this package actually
// readable instead of a bare integer.
func TestScreen_String(t *testing.T) {
	cases := map[screen]string{
		screenLoading:   "loading",
		screenNoCore:    "no-core",
		screenMain:      "main",
		screenDrillDown: "drill-down",
		screenModal:     "modal",
		screenQuiescing: "quiescing",
	}
	seen := map[string]bool{}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("screen(%d).String() = %q, want %q", int(s), got, want)
		}
		if seen[want] {
			t.Errorf("screen name %q reused by more than one value", want)
		}
		seen[want] = true
	}
}

// TestView_ScreenMainRoutesToRenderMain is the sibling packet covering
// Task 4.6's own supersession of the pre-4.6 shared placeholder:
// screenMain now renders the real zone ladder (renderMain, model.go) --
// distinct from screenDrillDown's still-unbuilt placeholder (Task 4.7's
// own concern, out of scope here, section 8). Mirrors
// TestView_ScreenModalRoutesToRenderModal's own precedent (Task 4.8's
// analogous supersession of the modal placeholder).
func TestView_ScreenMainRoutesToRenderMain(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.screen = screenMain

	got := m.View()
	if got == "" || got == "loading…" || got == "pr-pool: main" {
		t.Fatalf("screenMain rendered the old placeholder %q, want the real zone ladder", got)
	}
	// Registry is deliberately excluded from this list: it is omitted
	// entirely when empty (panes_test.go's own
	// TestRenderRegistryPane_OmittedEntirelyWhenEmpty), which is exactly
	// the state of this zero-value StatusReply.
	for _, want := range []string{"Listeners", "Queues", "Sources", "Activity"} {
		if !strings.Contains(got, want) {
			t.Errorf("screenMain View() missing pane %q; got:\n%s", want, got)
		}
	}
}

// TestView_ScreenDrillDownRoutesToRenderDrillDown is Task 4.7's own
// supersession of the pre-4.7 placeholder: screenDrillDown now renders the
// real full-screen detail view + breadcrumb (drilldown.go's
// renderDrillDown), distinct from the main/modal placeholders, driven by
// whichever row m.drillKind/m.drillIndex currently select.
func TestView_ScreenDrillDownRoutesToRenderDrillDown(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.screen = screenDrillDown
	m.drillKind = rowListener
	m.reply = StatusReply{Listeners: []Listener{{Role: "reviewer"}}}

	got := m.View()

	if !strings.Contains(got, "reviewer") {
		t.Fatalf("screenDrillDown View() = %q, want it to name the drilled listener (reviewer)", got)
	}
	if !strings.Contains(got, "Config") {
		t.Fatalf("screenDrillDown View() = %q, want it to include the Config section", got)
	}
}

// TestView_ScreenModalRoutesToRenderModal is Task 4.8's own supersession
// of the pre-4.8 placeholder: screenModal now renders real content
// (help.go's renderModal), distinct from the main/drill-down placeholder
// above, driven by whichever ModalKind is active.
func TestView_ScreenModalRoutesToRenderModal(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24
	m.screen = screenModal
	m.activeModal = ModalHelp

	got := m.View()
	if got == "" || got == "pr-pool: main" {
		t.Fatalf("screenModal/ModalHelp rendered the old placeholder %q, want real help-modal content", got)
	}
	if !strings.Contains(got, "Help") {
		t.Errorf("screenModal/ModalHelp View() should route through render.HelpModal; got:\n%s", got)
	}
}

// TestNoCoreMessage_SanitizesBeforeComposing is Binding Decision Step 5,
// literally: an error string or discovery path carrying a raw control
// sequence must never reach the composed message unsanitized -- if
// Sanitize ran AFTER composing (or not at all), the escape bytes below
// would still be present in the output.
func TestNoCoreMessage_SanitizesBeforeComposing(t *testing.T) {
	dirtyErr := errors.New("dial unix \x1b[31msocket\x1b[0m: connection refused")
	dirtyPath := "/tmp/\x1b[1mpr-pool\x1b[0m/discovery.json"

	got := noCoreMessage(dirtyPath, dirtyErr, render.NewTheme(false), 80)

	if strings.Contains(got, "\x1b") {
		t.Fatalf("noCoreMessage output still contains a raw ESC byte: %q", got)
	}
	if !strings.Contains(got, "dial unix socket: connection refused") {
		t.Errorf("noCoreMessage dropped/paraphrased the sanitized error text: %q", got)
	}
	if !strings.Contains(got, "/tmp/pr-pool/discovery.json") {
		t.Errorf("noCoreMessage dropped/paraphrased the sanitized discovery path: %q", got)
	}
}

// TestNoCoreMessage_ShapeMatchesDaemonOfflinePrecedent reproduces pa-
// monitor's daemonOfflineMessage shape (packages/pa-monitor/internal/tui/
// view.go): both remedies always shown as text (never conditioned on live
// systemd/launchd detection), the auto-reconnect note, and a press-q line.
func TestNoCoreMessage_ShapeMatchesDaemonOfflinePrecedent(t *testing.T) {
	err := fmt.Errorf("tui: poll: %w", core.ErrNoRunningCore)
	got := noCoreMessage("/var/pr-pool/discovery.json", err, render.NewTheme(false), 80)

	for _, want := range []string{
		"No core running",
		"/var/pr-pool/discovery.json",
		"pr-pool run",
		"daemon",
		"automatically",
		"Press q to quit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("noCoreMessage missing %q; got:\n%s", want, got)
		}
	}
	if !strings.Contains(got, core.ErrNoRunningCore.Error()) {
		t.Errorf("noCoreMessage does not carry the underlying error verbatim; got:\n%s", got)
	}
}
