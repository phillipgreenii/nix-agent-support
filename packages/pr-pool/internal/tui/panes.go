// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.6) carries the Listeners/Queues/Sources/Registry/Activity pane
// renderers, the derived two-axis health grammar (pool / participants),
// and the DECL column [design: Task 4.6 Files (panes.go); Task 4.6 Binding
// decisions].
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pr-pool/internal/textsafe"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// --- Derived health: two axes [design: Task 4.6 Binding decisions] ---

// poolHealthText ranks the pool's own health: no-core > paused >
// core-tick-wedged > degraded > ok. hasCore is always true when actually
// called from screenMain (no-core has its own dedicated screen), but the
// parameter is kept explicit so the ranking itself -- the no-core/paused
// checks happening before core-tick-wedged/degraded/ok -- is directly
// testable without routing through a full Model.
func poolHealthText(hasCore, gated, tickWedged, degraded bool, theme render.Theme) string {
	switch {
	case !hasCore:
		return theme.Disabled.Render("no-core")
	case gated:
		return theme.Paused.Render("paused")
	case tickWedged:
		return theme.Failing.Render("core-tick-wedged")
	case degraded:
		return theme.Cooling.Render("degraded")
	default:
		return theme.OK.Render("ok")
	}
}

// staleThreshold is the shared "3 x tick interval, floored at 5s" formula
// [design: Task 4.6 (§7 Staleness)], reused for both the pool's own
// core-tick-wedged check and a source's staleness check -- tickIntervalMs
// is the only cadence figure the wire reply carries; a fast-ticking core
// must not flap into "wedged"/"stale" on ordinary jitter.
func staleThreshold(tickIntervalMs int64) time.Duration {
	t := time.Duration(tickIntervalMs) * time.Millisecond * 3
	if t < 5*time.Second {
		return 5 * time.Second
	}
	return t
}

// poolTickWedged reports whether the pool's own health is
// core-tick-wedged: lastTickAt is older than staleThreshold. A zero
// lastTickAt (no tick observed yet) is never wedged -- that is a
// pre-first-tick fact, not a stall.
func poolTickWedged(lastTickAt time.Time, tickIntervalMs int64, now time.Time) bool {
	if lastTickAt.IsZero() {
		return false
	}
	return now.Sub(lastTickAt) > staleThreshold(tickIntervalMs)
}

// poolDegraded reports whether any self-reporting registrant names itself
// degraded or unavailable (internal/core/registry.go's SelfStatus) --
// the pool axis's own "degraded" rung.
func poolDegraded(registry []Registration) bool {
	for _, r := range registry {
		if r.Self == "degraded" || r.Self == "unavailable" {
			return true
		}
	}
	return false
}

// listenerHealthText ranks a listener's health: disabled > excluded >
// cooling > ok. disabled (a config fact) and excluded (a run-scoped
// selector fact) both outrank any runtime observation; disabled outranks
// excluded because it is the more durable fact [design: Task 4.6 Binding
// decisions].
func listenerHealthText(l Listener, theme render.Theme) string {
	switch {
	case !l.Enabled:
		return theme.Disabled.Render("disabled")
	case l.Excluded:
		return theme.Excluded.Render("excluded")
	case l.Backoff != nil:
		return theme.Cooling.Render("cooling " + formatSeconds(time.Until(l.Backoff.NextEligible)))
	default:
		return theme.OK.Render("ok")
	}
}

// sourceHealthText ranks a source's health: disabled > excluded > failing
// > stale > idle > ok. A source that has never ticked (LastTick's zero
// value) renders idle, never stale -- staleness names a source that WAS
// ticking and has gone quiet, a different fact from "hasn't started yet."
func sourceHealthText(s Source, tickIntervalMs int64, now time.Time, theme render.Theme) string {
	switch {
	case !s.Enabled:
		return theme.Disabled.Render("disabled")
	case s.Excluded:
		return theme.Excluded.Render("excluded")
	case s.Failure != nil && s.Failure.Count > 0:
		return theme.Failing.Render(fmt.Sprintf("failing ×%d", s.Failure.Count))
	case s.LastTick.IsZero():
		return theme.Muted.Render("idle")
	case now.Sub(s.LastTick) > staleThreshold(tickIntervalMs):
		return theme.Stale.Render("stale " + formatMinutes(now.Sub(s.LastTick)))
	default:
		return theme.OK.Render("ok")
	}
}

// formatSeconds/formatMinutes render a non-negative duration coarsely (no
// sub-unit component), matching the design's own grammar examples ("cooling
// 42s", "stale 8m").
func formatSeconds(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func formatMinutes(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// --- Pane renderers [design: Task 4.6 Files (panes.go); §4.3 Tier mockups] ---

// renderListenersPane renders the Listeners pane. Column set narrows with
// tier: Wide keeps BINDS; Narrow drops it; Tiny further drops DECL (the
// design's own Tiny mockup shows only ROLE/HEALTH/DLVD). title lets the
// caller append "(focused)" when this pane is the zone ladder's fill zone
// (matching the Tiny mockup's own "Listeners (focused)" heading).
func renderListenersPane(listeners []Listener, tier int, theme render.Theme, emptyMsg, title string) string {
	var headers []string
	var widths []int
	switch tier {
	case render.TierWide:
		headers, widths = []string{"ROLE", "BINDS", "HEALTH", "DLVD", "DECL"}, []int{10, 14, 16, 6, 6}
	case render.TierNarrow:
		headers, widths = []string{"ROLE", "HEALTH", "DLVD", "DECL"}, []int{10, 16, 6, 6}
	default:
		headers, widths = []string{"ROLE", "HEALTH", "DLVD"}, []int{10, 14, 6}
	}

	rows := make([][]string, 0, len(listeners))
	for _, l := range listeners {
		role := textsafe.Sanitize(l.Role)
		health := listenerHealthText(l, theme)
		dlvd := fmt.Sprintf("%d", l.Delivered)
		decl := fmt.Sprintf("%d", l.Declined)
		switch tier {
		case render.TierWide:
			binds := textsafe.Sanitize(strings.Join(l.Binds, ","))
			rows = append(rows, []string{role, binds, health, dlvd, decl})
		case render.TierNarrow:
			rows = append(rows, []string{role, health, dlvd, decl})
		default:
			rows = append(rows, []string{role, health, dlvd})
		}
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg)
}

// renderQueuesPane renders the Queues pane: TYPE/DEPTH, unchanged across
// tiers.
func renderQueuesPane(queues []Queue, emptyMsg, title string) string {
	headers := []string{"TYPE", "DEPTH"}
	widths := []int{18, 8}
	rows := make([][]string, 0, len(queues))
	for _, q := range queues {
		rows = append(rows, []string{textsafe.Sanitize(q.Type), fmt.Sprintf("%d", q.Depth)})
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg)
}

// renderSourcesPane renders the Sources pane: SOURCE/LAST TICK/STATE,
// unchanged across tiers ("Sources", never "QUERY" -- ux-13).
func renderSourcesPane(sources []Source, tickIntervalMs int64, now time.Time, theme render.Theme, emptyMsg, title string) string {
	headers := []string{"SOURCE", "LAST TICK", "STATE"}
	widths := []int{12, 10, 16}
	rows := make([][]string, 0, len(sources))
	for _, s := range sources {
		lastTick := "-"
		if !s.LastTick.IsZero() {
			lastTick = s.LastTick.Format("15:04:05")
		}
		rows = append(rows, []string{
			textsafe.Sanitize(s.Name),
			lastTick,
			sourceHealthText(s, tickIntervalMs, now, theme),
		})
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg)
}

// renderRegistryPane renders the Registry pane: ID/KIND/STATE. Rendered
// only when non-empty per v1's own carried decision -- emptyMsg is still
// accepted so a caller CAN show a placeholder (matching the Wide/Narrow
// mockups' "(no participants registered)"), but the sibling composing
// this into the zone ladder (model.go) is free to omit the zone entirely
// instead.
func renderRegistryPane(registry []Registration, emptyMsg, title string) string {
	headers := []string{"ID", "KIND", "STATE"}
	widths := []int{16, 10, 10}
	rows := make([][]string, 0, len(registry))
	for _, r := range registry {
		rows = append(rows, []string{
			textsafe.Sanitize(r.ID),
			textsafe.Sanitize(r.Kind),
			textsafe.Sanitize(r.State),
		})
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg)
}

// renderActivityPane renders the full-width Activity row: one line per
// entry, oldest-first per the ring's own Read order reversed here so the
// newest entry renders first (matching the mockup's own top-to-bottom
// recency). ActivityEntry (reply.go, Task 4.4) carries only
// Seq/StartedAt/Type/Outcome -- no role/binding fields exist to render the
// mockup's fuller line, so this renders exactly what the frozen wire shape
// carries.
func renderActivityPane(activity []ActivityEntry, dropped bool, emptyMsg string) string {
	rows := make([]string, 0, len(activity)+1)
	if dropped {
		rows = append(rows, "(older entries dropped -- ring capacity exceeded)")
	}
	for i := len(activity) - 1; i >= 0; i-- {
		a := activity[i]
		ts := "-"
		if !a.StartedAt.IsZero() {
			ts = a.StartedAt.Format("15:04:05")
		}
		line := fmt.Sprintf("%s  %-10s", ts, textsafe.Sanitize(a.Type))
		if a.Outcome != "" {
			line += " → " + textsafe.Sanitize(a.Outcome)
		}
		rows = append(rows, line)
	}
	return renderPaneBoxPlain("Activity", rows, emptyMsg)
}

// renderPaneBox renders a bordered box with a title, a fixed-width column
// header row, and one row per data row. Each cell is padded to its column
// width via lipgloss (ANSI/width-aware, so a themed/colored health cell
// still aligns). len(rows) == 0 renders emptyMsg as the sole content line
// instead of the header row.
func renderPaneBox(title string, headers []string, widths []int, rows [][]string, emptyMsg string) string {
	var lines []string
	if len(rows) == 0 {
		if emptyMsg != "" {
			lines = append(lines, emptyMsg)
		}
	} else {
		lines = append(lines, formatPaneRow(headers, widths))
		for _, r := range rows {
			lines = append(lines, formatPaneRow(r, widths))
		}
	}
	return paneFrame(title, lines)
}

// renderPaneBoxPlain is renderPaneBox's un-columned sibling for the
// Activity pane, whose rows are already fully-composed strings.
func renderPaneBoxPlain(title string, rows []string, emptyMsg string) string {
	lines := rows
	if len(rows) == 0 && emptyMsg != "" {
		lines = []string{emptyMsg}
	}
	return paneFrame(title, lines)
}

func formatPaneRow(cells []string, widths []int) string {
	parts := make([]string, len(cells))
	for i, c := range cells {
		w := 0
		if i < len(widths) {
			w = widths[i]
		}
		if w > 0 {
			parts[i] = lipgloss.NewStyle().Width(w).Render(c)
		} else {
			parts[i] = c
		}
	}
	return strings.Join(parts, " ")
}

// paneFrame wraps content lines in a "┌ Title ─...─┐" / "└─...─┘" box,
// sized to the content's own widest line -- pane boxes are composed
// STACKED (not side-by-side) in this packet's zone-ladder wiring
// [design: Task 4.6 Files]; the terminal-width clip that matters for a
// real terminal is applied once, at the top of the zone ladder
// (zones.go's concatZones -> render.Block), not per-pane here.
func paneFrame(title string, lines []string) string {
	inner := lipgloss.Width(title) + 2
	for _, l := range lines {
		if w := lipgloss.Width(l); w > inner {
			inner = w
		}
	}
	var b strings.Builder
	b.WriteString("┌ " + title + " " + strings.Repeat("─", max(0, inner-lipgloss.Width(title)-1)) + "┐\n")
	for _, l := range lines {
		pad := inner - lipgloss.Width(l)
		if pad < 0 {
			pad = 0
		}
		b.WriteString("│ " + l + strings.Repeat(" ", pad) + " │\n")
	}
	b.WriteString("└" + strings.Repeat("─", inner+2) + "┘")
	return b.String()
}
