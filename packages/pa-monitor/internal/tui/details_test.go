package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// The details overlay (Enter on a session) never showed the session's
// status/blocker, so an idle/working/blocked session revealed nothing about its
// state. It MUST show Status, and qualify a blocked session with its blocker
// (e.g. "blocked/usage_limit"), matching the CLI table.
func TestDetailsShowsStatusAndBlocker(t *testing.T) {
	working := &aggregate.SessionView{Session: &session.Session{SessionID: "id1", Status: session.Working}}
	if out := RenderDetails(working, 120); !strings.Contains(out, "Status:") || !strings.Contains(out, "working") {
		t.Errorf("details missing Status/working:\n%s", out)
	}
	blocked := &aggregate.SessionView{Session: &session.Session{SessionID: "id2", Status: session.Blocked, Blocker: session.UsageLimit}}
	if out := RenderDetails(blocked, 120); !strings.Contains(out, "blocked/usage_limit") {
		t.Errorf("details missing blocked/usage_limit:\n%s", out)
	}
}

// The details overlay must surface the session's workspace.scope label (from
// the persisted label set) so a viewer can tell which workspace a session
// belongs to (personal / ziprecruiter). It is shown only when the
// label is present. See pg2-4xbrm.
func TestDetailsShowsWorkspaceScope(t *testing.T) {
	withScope := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id-scope"},
		SessionEnrichment: aggregate.SessionEnrichment{
			Labels: map[string]string{"workspace.scope": "ziprecruiter"},
		},
	}
	out := RenderDetails(withScope, 120)
	if !strings.Contains(out, "Scope:") || !strings.Contains(out, "ziprecruiter") {
		t.Errorf("details missing workspace.scope:\n%s", out)
	}

	// Absent label: no Scope line (avoid a blank/misleading row).
	noScope := &aggregate.SessionView{Session: &session.Session{SessionID: "id-noscope"}}
	if out := RenderDetails(noScope, 120); strings.Contains(out, "Scope:") {
		t.Errorf("details should omit Scope line when workspace.scope is absent:\n%s", out)
	}
}

// TestDetailsStatusQualifierByState is the negative / complementary coverage to
// TestDetailsShowsStatusAndBlocker (which asserts only that "working" appears and
// the usage_limit qualifier renders). RenderDetails appends "/blocker" to the
// Status line ONLY for a Blocked session (details.go). This verifies the
// current behavior across states:
//   - a non-blocked status (working/idle) must NOT gain a slash-qualifier — even
//     when a stray Blocker is set, proving the Status==Blocked guard (not merely
//     the NoBlocker check) is what suppresses it; and
//   - the human_input / error / human_authn blocker strings (only usage_limit was
//     previously asserted) render as "blocked/<blocker>".
func TestDetailsStatusQualifierByState(t *testing.T) {
	cases := []struct {
		name    string
		status  session.Status
		blocker session.Blocker
		want    string // substring that MUST appear
		notWant string // substring that MUST NOT appear ("" = no negative assertion)
	}{
		{"working has no qualifier", session.Working, session.NoBlocker, "working", "working/"},
		// Intentionally-inconsistent input (a blocker on a non-blocked session): the
		// Status==Blocked guard must still suppress the "/usage_limit" suffix.
		{"working ignores stray blocker", session.Working, session.UsageLimit, "working", "working/"},
		{"idle has no qualifier", session.Idle, session.NoBlocker, "idle", "idle/"},
		{"blocked human_input", session.Blocked, session.HumanInput, "blocked/human_input", ""},
		{"blocked error", session.Blocked, session.ErrorBlocker, "blocked/error", ""},
		{"blocked human_authn", session.Blocked, session.HumanAuthn, "blocked/human_authn", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sv := &aggregate.SessionView{
				Session: &session.Session{SessionID: "id1", Status: c.status, Blocker: c.blocker},
			}
			out := RenderDetails(sv, 120)
			if !strings.Contains(out, c.want) {
				t.Errorf("Status line: want %q in output:\n%s", c.want, out)
			}
			if c.notWant != "" && strings.Contains(out, c.notWant) {
				t.Errorf("Status line: %q must NOT appear (a non-blocked status carries no qualifier):\n%s", c.notWant, out)
			}
		})
	}
}

func TestDetailsShowsTerminalHost(t *testing.T) {
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1", TerminalHost: "tmux"},
		SessionEnrichment: aggregate.SessionEnrichment{},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "tmux") {
		t.Errorf("details missing terminal host:\n%s", out)
	}
}

func TestDetailsOverlayShowsPerModelBreakdown(t *testing.T) {
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Name: "n1", Cwd: "/p", PID: 42, Kind: "interactive"},
		SessionEnrichment: aggregate.SessionEnrichment{
			Model: "claude-opus-4-7", ContextTokens: 5000,
			FirstPrompt: "first prompt text",
		},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "n1") || !strings.Contains(out, "id1") {
		t.Errorf("details missing identifiers:\n%s", out)
	}
	if !strings.Contains(out, "first prompt text") {
		t.Errorf("details missing first prompt:\n%s", out)
	}
}

// TestDetailsRuleLineScalesWithWidth verifies the rule line stretches to the
// effective width at every tier, with the centered "Session Details" label.
func TestDetailsRuleLineScalesWithWidth(t *testing.T) {
	sv := &aggregate.SessionView{
		Session:           &session.Session{},
		SessionEnrichment: aggregate.SessionEnrichment{},
	}
	cases := []struct{ width int }{{60}, {80}, {120}, {200}}
	for _, c := range cases {
		out := RenderDetails(sv, c.width)
		first := strings.SplitN(out, "\n", 2)[0]
		if !strings.Contains(first, "Session Details") {
			t.Errorf("rule line missing label at width=%d: %q", c.width, first)
		}
		if w := lipgloss.Width(first); w != c.width {
			t.Errorf("rule line width = %d, want %d at width=%d: %q", w, c.width, c.width, first)
		}
	}
}

// TestDetailsLastErrorTerminalShown verifies that a terminal error is shown in
// the details pane with kind and (escalated) suffix when appropriate.
func TestDetailsLastErrorTerminalShown(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrServerError,
		Text:       "API Error: 529 Overloaded",
		At:         time.Now().Add(-2 * time.Minute),
		IsTerminal: true,
	}
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: le, LastErrorRetryable: true},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "Last error") {
		t.Errorf("details missing Last error section:\n%s", out)
	}
	if !strings.Contains(out, "server_error") {
		t.Errorf("details missing error kind:\n%s", out)
	}
	if !strings.Contains(out, "API Error: 529 Overloaded") {
		t.Errorf("details missing error text:\n%s", out)
	}
	if strings.Contains(out, "(escalated)") {
		t.Errorf("retryable error should not show (escalated); got:\n%s", out)
	}
}

// TestDetailsLastErrorEscalatedSuffix verifies that (escalated) appears when
// kind is inherently retryable but IsRetryable has been flipped to false.
func TestDetailsLastErrorEscalatedSuffix(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrServerError, // inherently retryable (transient server)
		Text:       "API Error: 529 Overloaded",
		At:         time.Now().Add(-5 * time.Minute),
		IsTerminal: true,
	}
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: le, LastErrorRetryable: false}, // escalated by daemon
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "(escalated)") {
		t.Errorf("escalated error should show (escalated) suffix:\n%s", out)
	}
}

// TestDetailsLastErrorNonTerminalHidden verifies that a non-terminal error is
// not shown in the details pane.
func TestDetailsLastErrorNonTerminalHidden(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrServerError,
		Text:       "transient error",
		At:         time.Now().Add(-1 * time.Minute),
		IsTerminal: false, // user resumed — not terminal
	}
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: le, LastErrorRetryable: true},
	}
	out := RenderDetails(sv, 120)
	if strings.Contains(out, "Last error") {
		t.Errorf("non-terminal error should not appear in details:\n%s", out)
	}
}

// TestDetailsPendingNudgeShown verifies that PendingNudge sources surface in
// the "Nudge:" block when something is currently queued.
func TestDetailsPendingNudgeShown(t *testing.T) {
	nudge := &aggregate.PendingNudge{Sources: []string{"disrupted", "manual"}}
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{PendingNudge: nudge},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "Nudge:") {
		t.Errorf("details missing Nudge: block:\n%s", out)
	}
	if !strings.Contains(out, "pending:") {
		t.Errorf("details missing pending: line:\n%s", out)
	}
	if !strings.Contains(out, "disrupted") || !strings.Contains(out, "manual") {
		t.Errorf("details missing nudge sources:\n%s", out)
	}
}

// TestDetailsNoNudgeBlockWhenEmpty verifies that the Nudge: block is absent
// when both PendingNudge and LastNudgedAt are empty.
func TestDetailsNoNudgeBlockWhenEmpty(t *testing.T) {
	sv := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{PendingNudge: nil},
	}
	out := RenderDetails(sv, 120)
	if strings.Contains(out, "Nudge:") {
		t.Errorf("empty nudge state should not produce a Nudge: block:\n%s", out)
	}
}

// TestDetailsLastNudgeShownWithoutPending verifies that nudge history surfaces
// even when nothing is currently queued.
func TestDetailsLastNudgeShownWithoutPending(t *testing.T) {
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastNudgedAt:     time.Now().Add(-3 * time.Minute),
			LastNudgeSources: []string{"manual"},
		},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "Nudge:") {
		t.Errorf("details missing Nudge: header when LastNudgedAt set:\n%s", out)
	}
	if strings.Contains(out, "pending:") {
		t.Errorf("no pending intents — pending: line should be absent:\n%s", out)
	}
	if !strings.Contains(out, "last sent:") {
		t.Errorf("details missing last sent: line:\n%s", out)
	}
	if !strings.Contains(out, "3 minutes ago") {
		t.Errorf("details missing humanized age:\n%s", out)
	}
	if !strings.Contains(out, "via:") || !strings.Contains(out, "manual") {
		t.Errorf("details missing via: source line:\n%s", out)
	}
}

// TestDetailsPendingAndLastBothShown verifies that the Nudge: block renders
// both the pending sources and the last-sent line when both apply.
func TestDetailsPendingAndLastBothShown(t *testing.T) {
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1"},
		SessionEnrichment: aggregate.SessionEnrichment{
			PendingNudge:     &aggregate.PendingNudge{Sources: []string{"window_reset"}},
			LastNudgedAt:     time.Now().Add(-10 * time.Second),
			LastNudgeSources: []string{"disrupted"},
		},
	}
	out := RenderDetails(sv, 120)
	if !strings.Contains(out, "pending:") || !strings.Contains(out, "window_reset") {
		t.Errorf("expected pending: line with window_reset:\n%s", out)
	}
	if !strings.Contains(out, "last sent:") {
		t.Errorf("expected last sent: line:\n%s", out)
	}
	if !strings.Contains(out, "via:") || !strings.Contains(out, "disrupted") {
		t.Errorf("expected via: line with disrupted:\n%s", out)
	}
}

// TestHumanizeAge verifies the humanizeAge helper for a few key thresholds.
func TestHumanizeAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "just now"},
		{30 * time.Second, "30 seconds ago"},
		{90 * time.Second, "1 minute ago"},
		{3 * time.Minute, "3 minutes ago"},
		{90 * time.Minute, "1 hour ago"},
		{3 * time.Hour, "3 hours ago"},
	}
	for _, c := range cases {
		got := humanizeAge(c.d)
		if got != c.want {
			t.Errorf("humanizeAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// --- Scrollable details viewport ---

// longPromptSV builds a SessionView with a multi-line FirstPrompt so the
// rendered details are guaranteed to exceed a small viewport.
func longPromptSV() *aggregate.SessionView {
	lines := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		lines = append(lines, "prompt line padding for scroll test")
	}
	return &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1", Name: "n1"},
		SessionEnrichment: aggregate.SessionEnrichment{
			FirstPrompt: strings.Join(lines, "\n"),
		},
	}
}

// TestDetailsDownKeyScrollsWhenSelected verifies that pressing 'j' (down) while
// a session is selected increments detailsScrollOffset rather than moving the
// session-list cursor.
func TestDetailsDownKeyScrollsWhenSelected(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.selected = longPromptSV()
	m.width = 80
	m.height = 10
	before := m.detailsScrollOffset
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.detailsScrollOffset <= before {
		t.Errorf("down with selection: expected detailsScrollOffset > %d, got %d", before, m.detailsScrollOffset)
	}
}

// TestDetailsUpKeyDecreasesOffset verifies that pressing 'k' (up) with a
// positive scroll offset decreases the offset.
func TestDetailsUpKeyDecreasesOffset(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.selected = longPromptSV()
	m.width = 80
	m.height = 10
	m.detailsScrollOffset = 5
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.detailsScrollOffset != 4 {
		t.Errorf("up with selection from offset=5: want 4, got %d", m.detailsScrollOffset)
	}
}

// TestDetailsUpKeyAtZeroClamps verifies the offset never goes negative.
func TestDetailsUpKeyAtZeroClamps(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.selected = longPromptSV()
	m.width = 80
	m.height = 10
	m.detailsScrollOffset = 0
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.detailsScrollOffset != 0 {
		t.Errorf("up at offset=0: want 0, got %d", m.detailsScrollOffset)
	}
}

// TestEscResetsDetailsScrollOffset verifies that closing the details panel
// also resets the scroll offset.
func TestEscResetsDetailsScrollOffset(t *testing.T) {
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.selected = longPromptSV()
	m.detailsScrollOffset = 7
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.selected != nil {
		t.Errorf("esc should clear m.selected; got %#v", m.selected)
	}
	if m.detailsScrollOffset != 0 {
		t.Errorf("esc should reset detailsScrollOffset to 0; got %d", m.detailsScrollOffset)
	}
}

// TestEnterResetsDetailsScrollOffset verifies that re-opening a session resets
// the scroll offset so the new view starts at the top.
func TestEnterResetsDetailsScrollOffset(t *testing.T) {
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "id1"},
	}
	d := &aggregate.Directory{Path: "/p", Sessions: []*aggregate.SessionView{sv}}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	// Position cursor on a session row.
	for i, r := range m.flatRows {
		if r.Session != nil {
			m.cursor = i
			break
		}
	}
	m.detailsScrollOffset = 9
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.selected == nil {
		t.Fatalf("enter should select a session; got nil")
	}
	if m.detailsScrollOffset != 0 {
		t.Errorf("enter should reset detailsScrollOffset to 0; got %d", m.detailsScrollOffset)
	}
}

// TestRenderDetailsWindowClipsToHeight verifies the windowed renderer clips
// long content to the supplied viewport and indicates more content below.
func TestRenderDetailsWindowClipsToHeight(t *testing.T) {
	sv := longPromptSV()
	height := 8
	out := RenderDetailsWindow(sv, 80, height, 0)
	lines := strings.Split(out, "\n")
	if len(lines) > height {
		t.Errorf("RenderDetailsWindow: output has %d lines, want <= %d", len(lines), height)
	}
	if !strings.Contains(out, "↓") {
		t.Errorf("expected a ↓ overflow indicator when content exceeds viewport:\n%s", out)
	}
}

// TestRenderDetailsWindowShowsAboveIndicator verifies the windowed renderer
// shows an ↑ indicator when scrolled past the top.
func TestRenderDetailsWindowShowsAboveIndicator(t *testing.T) {
	sv := longPromptSV()
	out := RenderDetailsWindow(sv, 80, 8, 5)
	if !strings.Contains(out, "↑") {
		t.Errorf("expected a ↑ overflow indicator when scrollOffset>0:\n%s", out)
	}
}

// TestRenderDetailsWindowMaxOffsetExposesBottom is the regression test for an
// off-by-2 in the maxOffset clamp. Symptom: scrolling to the bottom hid the
// last two content lines because the algorithm always reserved a "↓ N more"
// footer row even at the literal end of the content. Verifies the trailing
// "[esc] close" hint (last line emitted by RenderDetails) is visible at a
// scroll offset far past the old clamp.
func TestRenderDetailsWindowMaxOffsetExposesBottom(t *testing.T) {
	sv := longPromptSV()
	out := RenderDetailsWindow(sv, 80, 8, 10_000) // ask for far more than possible
	if !strings.Contains(out, "[esc] close") {
		t.Errorf("scrolling to max should expose the closing hint at the bottom:\n%s", out)
	}
	if strings.Contains(out, "↓") {
		t.Errorf("at the bottom there should be no ↓ indicator (we're at the end):\n%s", out)
	}
}

// TestDetailsClipsLongValues verifies that an oversized field value is
// truncated with an ellipsis at narrow widths.
func TestDetailsClipsLongValues(t *testing.T) {
	sv := &aggregate.SessionView{
		Session: &session.Session{
			SessionID: "id",
			Name:      "n",
			Cwd:       strings.Repeat("/very-long-path-segment", 10),
		},
		SessionEnrichment: aggregate.SessionEnrichment{},
	}
	out := RenderDetails(sv, 60)
	for line := range strings.SplitSeq(out, "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Errorf("details line exceeds width=60 (got %d): %q", w, line)
		}
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis somewhere when long Cwd is clipped:\n%s", out)
	}
}
