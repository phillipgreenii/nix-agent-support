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

// TestView_MainDrillDownModalShareThePlaceholder documents this packet's
// own out-of-scope boundary (section 8): the zone ladder/banner/dashboard
// panes that give screenMain its FINAL rendering, plus all drill-down and
// modal content, are the sibling packets covering Tasks 4.6-4.8. Until
// then all three render the same minimal placeholder.
func TestView_MainDrillDownModalShareThePlaceholder(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 24

	var got [3]string
	for i, s := range []screen{screenMain, screenDrillDown, screenModal} {
		m.screen = s
		got[i] = m.View()
	}
	if got[0] != got[1] || got[1] != got[2] {
		t.Fatalf("main/drill-down/modal placeholders differ: %q, %q, %q", got[0], got[1], got[2])
	}
	if got[0] == "" || got[0] == "loading…" {
		t.Fatalf("main placeholder = %q, want a non-empty, non-loading placeholder", got[0])
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

	got := noCoreMessage(dirtyPath, dirtyErr, render.NewTheme(false))

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
	got := noCoreMessage("/var/pr-pool/discovery.json", err, render.NewTheme(false))

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
