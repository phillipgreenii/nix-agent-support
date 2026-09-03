// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.7) carries the drill-down full-screen detail view + breadcrumb
// (k9s-describe-style), [ / ] sibling stepping restricted to the same row
// kind, non-focusable queue/activity rows (comp-6), and the Config section
// rendering resolvedConfig's existing legacy-scalar fields [design: Task
// 4.7 Files; Task 4.7 Objective].
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pr-pool/internal/textsafe"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// focusableRowKind selects which row kind screenDrillDown is currently
// showing. Queue rows and activity rows are deliberately NOT members of
// this enum -- pressing enter on either is a no-op, never a screen
// transition (comp-6) [design: Task 4.7 Interfaces; Step 1].
type focusableRowKind int

const (
	rowListener focusableRowKind = iota
	rowSource
	// queue rows and activity rows are deliberately NOT members of this
	// enum -- comp-6.
)

// enterDrillDown implements the design's enter row: only rowListener/
// rowSource are focusable (comp-6). It only ever fires from screenMain
// (the design's own screen transition table: drill-down is "Entered
// when: enter on a listener/source row") -- pressed from any other
// screen (no-core/quiescing/modal), or on a pane with nothing to drill
// into (Queues/Registry, or an empty Listeners/Sources slice), it is a
// no-op: no screen transition [design: Task 4.7 Step 1; Task 4.7
// Interfaces].
//
// Task 4.6 delivered only pane-level focus (m.focusedPane), never a
// row-level cursor within a pane -- see Model.drillKind's own doc. Absent
// that, enterDrillDown always targets index 0 of the focused pane's kind.
func (m *Model) enterDrillDown() tea.Cmd {
	if m.screen != screenMain {
		return nil
	}
	switch m.focusedPane {
	case paneListeners:
		if len(m.reply.Listeners) == 0 {
			return nil
		}
		m.drillKind = rowListener
		m.drillIndex = 0
	case paneSources:
		if len(m.reply.Sources) == 0 {
			return nil
		}
		m.drillKind = rowSource
		m.drillIndex = 0
	default:
		// paneQueues / paneRegistry: comp-6 -- not members of
		// focusableRowKind, so entering from either is a no-op.
		return nil
	}
	m.screen = screenDrillDown
	return nil
}

// stepSibling implements the design's [ / ] row inside drill-down: moves
// to the previous (delta -1) or next (delta +1) sibling row of the SAME
// kind -- listener<->listener, source<->source, never crossing kinds
// (ux-12). Stepping past either end clamps; it does not wrap. A no-op
// outside screenDrillDown (there is no sibling row to step to) [design:
// Task 4.7 Step 3; ux-12].
func (m *Model) stepSibling(delta int) tea.Cmd {
	if m.screen != screenDrillDown {
		return nil
	}
	n := m.drillSiblingCount()
	if n == 0 {
		return nil
	}
	next := m.drillIndex + delta
	if next < 0 {
		next = 0
	}
	if next > n-1 {
		next = n - 1
	}
	m.drillIndex = next
	return nil
}

// drillSiblingCount is the length of the slice m.drillKind currently
// selects -- the clamp bound stepSibling steps within.
func (m *Model) drillSiblingCount() int {
	switch m.drillKind {
	case rowListener:
		return len(m.reply.Listeners)
	case rowSource:
		return len(m.reply.Sources)
	default:
		return 0
	}
}

// drillListener/drillSource return the row m.drillIndex currently
// selects, clamped defensively against a poll refresh that shrunk the
// underlying slice out from under an open drill-down screen --
// applyPollResult's own contract (model.go) is that only the DATA
// refreshes while a keyboard-driven screen is open; the screen itself (and
// this index) is left exactly where the operator put it. ok is false only
// when the slice is now empty.
func (m *Model) drillListener() (Listener, bool) {
	if len(m.reply.Listeners) == 0 {
		return Listener{}, false
	}
	i := m.drillIndex
	if i >= len(m.reply.Listeners) {
		i = len(m.reply.Listeners) - 1
	}
	return m.reply.Listeners[i], true
}

func (m *Model) drillSource() (Source, bool) {
	if len(m.reply.Sources) == 0 {
		return Source{}, false
	}
	i := m.drillIndex
	if i >= len(m.reply.Sources) {
		i = len(m.reply.Sources) - 1
	}
	return m.reply.Sources[i], true
}

// drillBreadcrumb renders the k9s-describe-style breadcrumb naming the
// currently drilled row's kind and identity. The exact text format beyond
// "k9s-describe-style" is left to this packet's own freedom boundary
// [design: Task 4.7].
func (m *Model) drillBreadcrumb() string {
	switch m.drillKind {
	case rowListener:
		if l, ok := m.drillListener(); ok {
			return fmt.Sprintf("pr-pool ▸ Listeners ▸ %s", textsafe.Sanitize(l.Role))
		}
	case rowSource:
		if s, ok := m.drillSource(); ok {
			return fmt.Sprintf("pr-pool ▸ Sources ▸ %s", textsafe.Sanitize(s.Name))
		}
	}
	return "pr-pool ▸ (nothing selected)"
}

// renderDrillDown composes screenDrillDown's full-screen detail view: the
// breadcrumb, the drilled row's own fields, and the Config section
// (resolvedConfig's legacy-scalar fields, Step 4) [design: Task 4.7 Files;
// Task 4.7 Step 4].
//
// Width clipping (pg2-wp7k6): the composed output is run through
// render.Block/render.EffectiveWidth before returning, matching the pattern
// every other tui render/pane file already uses (e.g. banner.go's
// renderHeader) -- the Config section's "perParticipant" line (57 columns)
// otherwise exceeds narrow widths unconditionally.
func (m *Model) renderDrillDown() string {
	var b strings.Builder
	b.WriteString(m.drillBreadcrumb())
	b.WriteString("\n\n")
	b.WriteString(m.drillDetail())
	b.WriteString("\n")
	b.WriteString(renderConfigSection(resolvedConfigView{}))
	return render.Block(b.String(), render.EffectiveWidth(m.width))
}

// drillDetail renders the currently drilled row's own fields -- reusing
// panes.go's own health-text functions (listenerHealthText/
// sourceHealthText) so the health grammar shown here always matches the
// pane row it drilled from.
func (m *Model) drillDetail() string {
	switch m.drillKind {
	case rowListener:
		l, ok := m.drillListener()
		if !ok {
			return "(no listeners configured)\n"
		}
		return renderListenerDetail(l, m.theme)
	case rowSource:
		s, ok := m.drillSource()
		if !ok {
			return "(no sources configured)\n"
		}
		return renderSourceDetail(s, m.reply.TickIntervalMs, time.Now(), m.theme)
	default:
		return ""
	}
}

func renderListenerDetail(l Listener, theme render.Theme) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Role:      %s\n", textsafe.Sanitize(l.Role))
	fmt.Fprintf(&b, "Binds:     %s\n", textsafe.Sanitize(strings.Join(l.Binds, ",")))
	fmt.Fprintf(&b, "Health:    %s\n", listenerHealthText(l, theme))
	fmt.Fprintf(&b, "Delivered: %d\n", l.Delivered)
	fmt.Fprintf(&b, "Declined:  %d\n", l.Declined)
	return b.String()
}

func renderSourceDetail(s Source, tickIntervalMs int64, now time.Time, theme render.Theme) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Name:     %s\n", textsafe.Sanitize(s.Name))
	fmt.Fprintf(&b, "Type:     %s\n", textsafe.Sanitize(s.Type))
	fmt.Fprintf(&b, "Mode:     %s\n", textsafe.Sanitize(s.Mode))
	fmt.Fprintf(&b, "Health:   %s\n", sourceHealthText(s, tickIntervalMs, now, theme))
	lastTick := "-"
	if !s.LastTick.IsZero() {
		lastTick = s.LastTick.Format("15:04:05")
	}
	fmt.Fprintf(&b, "LastTick: %s\n", lastTick)
	return b.String()
}

// resolvedConfigView is this file's own rendering-shape mirror of
// core.ResolvedConfig's legacy scalar fields (repoRoot/beadsPrefix/
// pollIntervalMs/activeRoles/activeQueries) -- NOT a wire decode target.
// internal/tui.StatusReply does not decode `resolvedConfig` at all today
// (Flagged for operator, out of scope section 8: closing that gap belongs
// to a later change outside this docket's own fixed structure), so every
// call site here hands renderConfigSection a zero value until it does. A
// future extraction phase's real per-kind perParticipant content slots in
// here without changing this function's shape [design: Task 4.7 Files].
type resolvedConfigView struct {
	RepoRoot       string
	BeadsPrefix    string
	PollIntervalMs int64
	ActiveRoles    int
	ActiveQueries  int
}

// renderConfigSection renders the Config section body: the legacy-scalar
// fields verbatim plus a one-line note that perParticipant is empty until
// a per-kind whitelist producer exists -- this section renders exactly
// what is available today, never inventing per-kind content [design: Task
// 4.7 Step 4].
func renderConfigSection(cfg resolvedConfigView) string {
	var b strings.Builder
	b.WriteString("Config\n")
	fmt.Fprintf(&b, "  repoRoot:       %s\n", configString(cfg.RepoRoot))
	fmt.Fprintf(&b, "  beadsPrefix:    %s\n", configString(cfg.BeadsPrefix))
	fmt.Fprintf(&b, "  pollIntervalMs: %s\n", configInt64(cfg.PollIntervalMs))
	fmt.Fprintf(&b, "  activeRoles:    %d\n", cfg.ActiveRoles)
	fmt.Fprintf(&b, "  activeQueries:  %d\n", cfg.ActiveQueries)
	b.WriteString("  perParticipant: {} (no per-kind whitelist producer yet)\n")
	return b.String()
}

// configString/configInt64 render an as-yet-unpopulated ResolvedConfig
// scalar as "-" rather than an empty/zero literal, matching this package's
// existing "-" convention for an absent value (e.g. renderSourcesPane's
// LAST TICK column).
func configString(s string) string {
	if s == "" {
		return "-"
	}
	return textsafe.Sanitize(s)
}

func configInt64(n int64) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}
