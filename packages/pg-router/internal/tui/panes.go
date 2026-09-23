// Package tui implements pg-router's operator-facing terminal UI. This file
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

	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
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

// selfReportDegraded reports whether state (a Listener's own
// SelfReportState) is one of the two states poolDegraded already treats as
// unhealthy at the pool axis -- reused here so a listener's OWN self-report
// gets the identical warning treatment, not a silently plainer rendering.
func selfReportDegraded(state string) bool {
	return state == "degraded" || state == "unavailable"
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

// sourceHealthText ranks a source's health: disabled > excluded > failing >
// idle > unknown-interval (N/A) > stale > ok. A source that has never
// ticked renders idle regardless of whether its interval is known -- idle
// outranks N/A, since "never started" and "cadence unknown" are different
// facts. Widened (this task) to use the source's OWN ExpectedIntervalMs
// instead of the pool-wide tickIntervalMs.
func sourceHealthText(s Source, now time.Time, theme render.Theme) string {
	switch {
	case !s.Enabled:
		return theme.Disabled.Render("disabled")
	case s.Excluded:
		return theme.Excluded.Render("excluded")
	case s.Failure != nil && s.Failure.Count > 0:
		return theme.Failing.Render(fmt.Sprintf("failing ×%d", s.Failure.Count))
	case s.LastTick.IsZero():
		return theme.Muted.Render("idle")
	case s.ExpectedIntervalMs <= 0:
		return theme.Muted.Render("N/A")
	case now.Sub(s.LastTick) > staleThreshold(s.ExpectedIntervalMs):
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

// formatCoarse renders d at second granularity below a minute, minute
// granularity at or above -- the same coarse convention formatSeconds/
// formatMinutes already establish, applied to whichever is appropriate.
func formatCoarse(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return formatSeconds(d)
	}
	return formatMinutes(d)
}

// sourceNextCheckText renders the Sources pane's "NEXT CHECK IN" column: a
// countdown when healthy and the interval is known, "overdue by <duration>"
// when stale, or "-" for every other health state (disabled/excluded/
// failing/idle/unknown-interval), since sourceHealthText's own column
// already explains why there is nothing to count down.
func sourceNextCheckText(s Source, now time.Time) string {
	if !s.Enabled || s.Excluded || s.LastTick.IsZero() || s.ExpectedIntervalMs <= 0 {
		return "-"
	}
	if s.Failure != nil && s.Failure.Count > 0 {
		return "-"
	}
	elapsed := now.Sub(s.LastTick)
	expected := time.Duration(s.ExpectedIntervalMs) * time.Millisecond
	if elapsed > staleThreshold(s.ExpectedIntervalMs) {
		return "overdue by " + formatCoarse(elapsed)
	}
	remaining := expected - elapsed
	if remaining < 0 {
		remaining = 0
	}
	return "~" + formatCoarse(remaining)
}

// --- Pane renderers [design: Task 4.6 Files (panes.go); §4.3 Tier mockups] ---

// unmatchedPartners partitions unmatchedBindings (reply.UnmatchedBindings --
// every bound TYPE this run's queue has never once enqueued, per
// eventqueue.Queue.UnmatchedBindings) against listeners' own Binds lists
// [pg2-7ezqt]: a type bound by exactly ONE listener row is that row's
// "partner", and moves inline onto it via perRow rather than staying only in
// the attention-line banner (liveness.go's attentionLine, which reports the
// complementary set via bannered). A type bound by zero rows (no configured
// role declares it any more -- config drift) or by two-or-more rows (which
// one would own the marker?) has no single partner and stays banner-only.
//
// A duplicate entry within one listener's own Binds is deliberately
// deduplicated (the inner seen set) before counting, so a config quirk that
// lists the same type twice under one role does not inflate that type's row
// count past 1 and wrongly evict it to the banner.
func unmatchedPartners(unmatchedBindings []string, listeners []Listener) (bannered []string, perRow map[int][]string) {
	if len(unmatchedBindings) == 0 {
		return nil, nil
	}
	unmatchedSet := make(map[string]bool, len(unmatchedBindings))
	for _, t := range unmatchedBindings {
		unmatchedSet[t] = true
	}
	counts := make(map[string]int, len(unmatchedBindings))
	rowOf := make(map[string]int, len(unmatchedBindings))
	for i, l := range listeners {
		seen := make(map[string]bool, len(l.Binds))
		for _, b := range l.Binds {
			if !unmatchedSet[b] || seen[b] {
				continue
			}
			seen[b] = true
			counts[b]++
			rowOf[b] = i
		}
	}
	perRow = make(map[int][]string)
	for _, t := range unmatchedBindings {
		if counts[t] == 1 {
			row := rowOf[t]
			perRow[row] = append(perRow[row], t)
		} else {
			bannered = append(bannered, t)
		}
	}
	return bannered, perRow
}

// unmatchedRowMarker renders the inline marker appended to a Listener row
// that owns one or more "partner" unmatched types (unmatchedPartners' own
// perRow) [pg2-7ezqt]. Wording deliberately mirrors attentionLine's own
// reworded phrase ("not seen yet this run", never "matched no configured
// role") -- the fact is the same in both places, so the words describing it
// must match. types is joined the same way attentionLine already joins its
// own names (","), then sanitized: an operator-configured event type is as
// untrusted as any other reply field reaching this render path.
func unmatchedRowMarker(types []string, theme render.Theme) string {
	names := textsafe.Sanitize(strings.Join(types, ","))
	return theme.Cooling.Render("! not seen yet this run: " + names)
}

// renderListenersPane renders the Listeners pane. Column set narrows with
// tier: Wide keeps BINDS; Narrow drops it; Tiny further drops DECL (the
// design's own Tiny mockup shows only ROLE/HEALTH/DLVD). title lets the
// caller append "(focused)" when this pane is the zone ladder's fill zone
// (matching the Tiny mockup's own "Listeners (focused)" heading).
//
// width is the available terminal width (typically
// render.EffectiveWidth(m.width)), threaded through to renderPaneBox so
// its columns can widen past the tier's declared minimums instead of
// always truncating to them [pg2-hlpuv]. width <= 0 is unbounded --
// widths are never capped below their natural content size (see
// paneColumnWidths).
//
// unmatchedBindings is reply.UnmatchedBindings, threaded in so a row whose
// bound type maps to exactly one row (its "partner", unmatchedPartners
// above) can carry the inline marker [pg2-7ezqt] -- an extra trailing cell
// appended only to rows that need it, past the declared headers/widths;
// formatPaneRow already renders any cell index beyond len(widths) unstyled
// and unclipped, so this needs no header/width changes for any tier.
func renderListenersPane(listeners []Listener, tier, width int, theme render.Theme, emptyMsg, title string, unmatchedBindings []string) string {
	var headers []string
	var widths []int
	switch tier {
	case render.TierWide:
		headers, widths = []string{"ROLE", "BINDS", "HEALTH", "LAST DELIVERED", "DLVD", "DECL(busy/unavail/other)", "SELF", "FAIL"}, []int{10, 14, 16, 14, 6, 14, 10, 6}
	case render.TierNarrow:
		headers, widths = []string{"ROLE", "HEALTH", "LAST DELIVERED", "DLVD", "DECL(busy/unavail/other)", "FAIL"}, []int{10, 16, 14, 6, 14, 6}
	default:
		headers, widths = []string{"ROLE", "HEALTH", "DLVD"}, []int{10, 14, 6}
	}

	_, perRow := unmatchedPartners(unmatchedBindings, listeners)
	rows := make([][]string, 0, len(listeners))
	for i, l := range listeners {
		role := textsafe.Sanitize(l.Role)
		health := listenerHealthText(l, theme)
		dlvd := fmt.Sprintf("%d", l.Delivered)
		busy, unavailable, other := l.DeclinedBucketed()
		decl := fmt.Sprintf("%d / %d / %d", busy, unavailable, other)
		lastDelivered := "-"
		if l.LastDeliveredAtMs > 0 {
			lastDelivered = formatCoarse(time.Since(time.UnixMilli(l.LastDeliveredAtMs))) + " ago"
		}
		self := "—"
		if l.SelfReportState != "" {
			self = textsafe.Sanitize(l.SelfReportState)
			if selfReportDegraded(l.SelfReportState) {
				self = theme.Cooling.Render(self)
			}
		}
		fail := fmt.Sprintf("%d", l.HandlerFailures)
		var row []string
		switch tier {
		case render.TierWide:
			binds := textsafe.Sanitize(strings.Join(l.Binds, ","))
			row = []string{role, binds, health, lastDelivered, dlvd, decl, self, fail}
		case render.TierNarrow:
			row = []string{role, health, lastDelivered, dlvd, decl, fail}
		default:
			row = []string{role, health, dlvd}
		}
		if types := perRow[i]; len(types) > 0 {
			row = append(row, unmatchedRowMarker(types, theme))
		}
		rows = append(rows, row)
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg, width)
}

// heartbeatQueuePrefixes names queue-type prefixes whose depth is a
// reconciliation heartbeat (a monitored-universe count), not an
// incremental/actionable backlog -- pr.reconcile today. pg-router has no
// per-type metadata to derive this from, so it is a hardcoded set; promote
// to config if a second heartbeat-style prefix is ever added. Matched by
// EXACT string or by prefix at a dot boundary, per the docket's Global
// Constraints ("starts with pr.reconcile") -- this docket's semantic
// post-check found the design's own Task 5 Step 3 code sample used an
// exact-match lookup (round 1), then found a naive prefix fix would
// misclassify a same-prefix sibling like "pr.reconciled" (round 2); the
// dot-boundary check here closes both.
var heartbeatQueuePrefixes = []string{
	"pr.reconcile",
}

// isHeartbeatQueueType reports whether t equals a heartbeat prefix exactly,
// or starts with one immediately followed by "." -- never a bare
// character-level prefix match, which would also match an unrelated
// same-prefix type name like "pr.reconciled".
func isHeartbeatQueueType(t string) bool {
	for _, prefix := range heartbeatQueuePrefixes {
		if t == prefix || strings.HasPrefix(t, prefix+".") {
			return true
		}
	}
	return false
}

// depthBar renders a coarse, fixed-width bar for depth out of an assumed
// max (this task uses 100 as a generous ceiling -- there is no configured
// per-type max to scale against, and a bar that never fills for a
// reasonable depth is more honest than a false sense of precision). Any
// nonzero depth floors to at least 1 filled cell -- integer scaling against
// the 100 ceiling would otherwise round a small-but-real depth (e.g. 3) down
// to 0 filled cells, rendering identically to an empty queue, which is LESS
// honest than the false-precision this bar avoids, not more.
func depthBar(depth int) string {
	const width = 20
	const assumedMax = 100
	filled := depth * width / assumedMax
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	if filled == 0 && depth > 0 {
		filled = 1
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// renderQueuesPane renders the Queues pane: TYPE/DEPTH, unchanged across
// tiers. width is the available terminal width, see renderListenersPane's
// doc [pg2-hlpuv]. DEPTH renders a coarse depth bar for ordinary
// incremental queue types, or a (heartbeat) label with no bar for a queue
// type in the heartbeat set (pr.reconcile today) -- that depth is a
// monitored-universe count, not an actionable backlog [design: Task 5,
// "pr.reconcile relabeling"; Global Constraints].
func renderQueuesPane(queues []Queue, width int, emptyMsg, title string) string {
	headers := []string{"TYPE", "DEPTH"}
	widths := []int{18, 30}
	rows := make([][]string, 0, len(queues))
	for _, q := range queues {
		depthCell := fmt.Sprintf("%d", q.Depth)
		if isHeartbeatQueueType(q.Type) {
			depthCell += "  (heartbeat)"
		} else {
			depthCell += "  " + depthBar(q.Depth)
		}
		rows = append(rows, []string{textsafe.Sanitize(q.Type), depthCell})
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg, width)
}

// renderSourcesPane renders the Sources pane: SOURCE/STATUS/SINCE LAST
// TICK/NEXT CHECK IN, unchanged across tiers ("Sources", never "QUERY" --
// ux-13). width is the available terminal width, see renderListenersPane's
// doc [pg2-hlpuv]. Widened (this task) to use each source's own
// ExpectedIntervalMs rather than the pool-wide tickIntervalMs -- the
// tickIntervalMs parameter is gone from this signature entirely.
func renderSourcesPane(sources []Source, now time.Time, width int, theme render.Theme, emptyMsg, title string) string {
	headers := []string{"SOURCE", "STATUS", "SINCE LAST TICK", "NEXT CHECK IN"}
	widths := []int{12, 8, 16, 18}
	rows := make([][]string, 0, len(sources))
	for _, s := range sources {
		sinceLastTick := "-"
		if !s.LastTick.IsZero() {
			sinceLastTick = formatCoarse(now.Sub(s.LastTick))
		}
		rows = append(rows, []string{
			textsafe.Sanitize(s.Name),
			sourceHealthText(s, now, theme),
			sinceLastTick,
			sourceNextCheckText(s, now),
		})
	}
	return renderPaneBox(title, headers, widths, rows, emptyMsg, width)
}

// outcomeBudgetEscalation is the Activity Ring's own "resource-limit"
// outcome verb (internal/activity/ring.go's ADR-0026 vocabulary; this bead,
// pg2-fm2gw): a component hitting its OWN resource ceiling
// (`packages/pg-router/docs/behavior/glossary.md`'s "resource-limit" — "not a
// defect, and the handler will be able again once the ceiling lifts").
const outcomeBudgetEscalation = "budget_escalation"

// activityDisplayCap is the most recent N Activity entries actually
// rendered (pg2-mnf7t.6) -- a Go constant, not configurable, for phase 1.
const activityDisplayCap = 8

// renderActivityPane renders the full-width Activity row: at most
// activityDisplayCap entries, newest-first per the ring's own Read order
// reversed here so the newest entry renders first (matching the mockup's
// own top-to-bottom recency), each with a relative "Xs ago"/"Xm ago"
// timestamp (formatCoarse, Task 1) rather than an absolute HH:MM:SS one --
// relative recency at a glance, and a bounded render regardless of the
// ring's own size [design: Task 6]. ActivityEntry (reply.go, Task 4.4)
// carries only Seq/StartedAt/Type/Outcome -- no role/binding fields exist to
// render the mockup's fuller line, so this renders exactly what the frozen
// wire shape carries. theme (this bead) styles ONLY the budget_escalation
// outcome distinctly from every other outcome in this same pane -- see
// renderActivityOutcome's own doc for why, and for how that reads distinctly
// from the operator-pause gate's own rendering (banner.go's
// renderPausedBanner).
func renderActivityPane(activity []ActivityEntry, dropped bool, emptyMsg string, theme render.Theme) string {
	rows := make([]string, 0, activityDisplayCap+1)
	if dropped {
		rows = append(rows, "(older entries dropped -- ring capacity exceeded)")
	}
	now := time.Now()
	shown := 0
	for i := len(activity) - 1; i >= 0 && shown < activityDisplayCap; i-- {
		a := activity[i]
		ts := "-"
		if !a.StartedAt.IsZero() {
			ts = formatCoarse(now.Sub(a.StartedAt)) + " ago"
		}
		line := fmt.Sprintf("%-10s %-10s", ts, textsafe.Sanitize(a.Type))
		if a.Outcome != "" {
			line += " → " + renderActivityOutcome(a.Outcome, theme)
		}
		rows = append(rows, line)
		shown++
	}
	return renderPaneBoxPlain("Activity", rows, emptyMsg)
}

// renderActivityOutcome renders one Activity entry's Outcome text (this
// bead, pg2-fm2gw), styled distinctly for outcomeBudgetEscalation so a
// component's OWN resource-limit hit reads visually differently from every
// OTHER outcome in the SAME pane -- and, more to this bead's own acceptance
// criterion, differently from the operator-pause gate's own rendering
// (banner.go's renderPausedBanner: theme.Paused, reverse video, on a pinned
// full-width banner, never an inline pane row). theme.Cooling is the SAME
// style listenerHealthText already uses for a listener's own transient
// backoff/cooldown state above -- the identical "temporarily unable, will
// recover on its own" semantics glossary.md's "resource-limit" entry states
// -- deliberately a DIFFERENT color token than theme.Paused, so the two
// conditions this bead must keep distinct (a component's own transient
// state vs a deliberate operator action) never share a style. Every other
// outcome renders plain (unstyled), unchanged from before this bead.
func renderActivityOutcome(outcome string, theme render.Theme) string {
	text := textsafe.Sanitize(outcome)
	if outcome == outcomeBudgetEscalation {
		return theme.Cooling.Render(text)
	}
	return text
}

// renderPaneBox renders a bordered box with a title, a column header row,
// and one row per data row. Each cell is padded to its column width via
// lipgloss (ANSI/width-aware, so a themed/colored health cell still
// aligns). len(rows) == 0 renders emptyMsg as the sole content line
// instead of the header row.
//
// staticWidths are the tier's own declared MINIMUM widths (what every
// column was unconditionally fixed at before pg2-hlpuv); budget is the
// available terminal width. The actual widths a row renders at are
// resolved by paneColumnWidths, which widens columns past staticWidths
// when budget allows -- renderPaneBox itself no longer chooses widths,
// only gathers the row data and hands the decision off.
func renderPaneBox(title string, headers []string, staticWidths []int, rows [][]string, emptyMsg string, budget int) string {
	var lines []string
	if len(rows) == 0 {
		if emptyMsg != "" {
			lines = append(lines, emptyMsg)
		}
	} else {
		widths := paneColumnWidths(headers, rows, staticWidths, budget)
		lines = append(lines, formatPaneRow(headers, widths))
		for _, r := range rows {
			lines = append(lines, formatPaneRow(r, widths))
		}
	}
	return paneFrame(title, lines)
}

// paneBoxOverhead is how many columns paneFrame's own border/padding add
// to a content line beyond the joined cell widths themselves: "│ " (2)
// on the left, " │" (2) on the right.
const paneBoxOverhead = 4

// paneColumnWidths chooses the actual per-column widths one pane box's
// row(s) render at [pg2-hlpuv]. Before this function existed, every
// caller was frozen at the tier's declared staticWidths regardless of the
// terminal's real size -- a long Role/Source-name/Registry-ID value
// always got ellipsis-truncated by formatPaneRow even when the terminal
// had plenty of free space to show it in full. This consciously
// supersedes that choice (paneFrame's own doc, below, names it as a
// deliberate "Task 4.6" decision) rather than treating it as a bugfix on
// an oversight.
//
// Each column's NATURAL width -- the widest of its header and every cell
// actually being rendered (ANSI/width-aware via lipgloss.Width), floored
// at its staticWidths minimum -- becomes that column's rendered width as
// long as the whole row still fits budget: every column's natural width,
// plus the (n-1) inter-column spaces formatPaneRow's strings.Join adds,
// plus paneBoxOverhead. budget <= 0 (no terminal width known -- every
// direct caller in this package's own tests before real terminal
// geometry exists) is treated as unbounded, so natural widths always
// render uncapped; this is also why callers whose content already fits
// staticWidths render byte-identical output whether or not a budget is
// given -- natural width equals the static width whenever nothing
// overflows it.
//
// When the natural row does NOT fit budget, this function never
// truncates content itself -- it only picks smaller widths. It grows
// each column from its staticWidths floor toward its natural width one
// column at a time (round-robin, so no single column monopolizes
// whatever spare room exists) until budget is exhausted, or falls back
// to staticWidths verbatim when there is no spare room at all (budget
// does not even cover the static minimums) -- exactly matching this
// package's pre-pg2-hlpuv behavior in that case. Any column whose chosen
// width still falls short of its natural width is truncated by
// formatPaneRow when the row is actually rendered, exactly as it always
// was [pg2-8iy1m]: that fix's guard (truncate a cell to its column width
// before lipgloss pads/renders it, so an overflowing value never wraps
// across physical lines and corrupts the box's borders) is unconditional
// in formatPaneRow regardless of how the width it was given got chosen,
// so it is preserved here verbatim for the case where the terminal
// genuinely lacks room.
func paneColumnWidths(headers []string, rows [][]string, staticWidths []int, budget int) []int {
	n := len(staticWidths)
	natural := make([]int, n)
	copy(natural, staticWidths)
	for i := 0; i < n && i < len(headers); i++ {
		if w := lipgloss.Width(headers[i]); w > natural[i] {
			natural[i] = w
		}
	}
	for _, r := range rows {
		for i := 0; i < n && i < len(r); i++ {
			if w := lipgloss.Width(r[i]); w > natural[i] {
				natural[i] = w
			}
		}
	}

	if budget <= 0 {
		return natural
	}

	sumWidths := func(ws []int) int {
		total := 0
		for _, w := range ws {
			total += w
		}
		return total
	}
	available := budget - paneBoxOverhead - (n - 1)
	if sumWidths(natural) <= available {
		return natural
	}
	if available <= sumWidths(staticWidths) {
		return append([]int(nil), staticWidths...)
	}

	widths := append([]int(nil), staticWidths...)
	remaining := available - sumWidths(staticWidths)
	for remaining > 0 {
		grew := false
		for i := 0; i < n && remaining > 0; i++ {
			if widths[i] < natural[i] {
				widths[i]++
				remaining--
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	return widths
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

// formatPaneRow lays out cells at their declared column widths, truncating
// (never wrapping) any cell that overflows its column [pg2-8iy1m].
// lipgloss's Style.Width() word-wraps content wider than the width given
// rather than only padding it -- so a Role/Bind-list/Source-name/Registry-ID
// value at or beyond its column's budget (all unbounded, operator-supplied
// strings) split that one cell across multiple physical lines, which both
// broke that row's own box border (the continuation lines carried no
// leading "│ "/trailing " │") and pushed every following row out of column
// alignment with the header. Truncating each cell to its column width first
// (render.Line, ANSI/width-aware, matching modalLeftColumnWidth's and
// legendRows' dynamic-width fixes for the same fixed-width-column shape,
// pg2-y6sy5/pg2-58ecs) guarantees every rendered row stays exactly one
// physical line with columns starting at the same offset, at the cost of an
// ellipsis on the rare overflowing value instead of a corrupted pane.
func formatPaneRow(cells []string, widths []int) string {
	parts := make([]string, len(cells))
	for i, c := range cells {
		w := 0
		if i < len(widths) {
			w = widths[i]
		}
		if w > 0 {
			parts[i] = lipgloss.NewStyle().Width(w).Render(render.Line(c, w))
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
// (zones.go's concatZones -> render.Block), not per-pane here. That
// remains true after pg2-hlpuv: paneFrame itself still never stretches a
// box wider than its own content, and still never re-clips per pane.
// What changed is what counts as "its own content" -- paneColumnWidths
// (above) now lets a row's columns grow toward their natural,
// untruncated size when the terminal has room, so a box arriving here
// can legitimately be much wider than the tier's old fixed minimums.
// Task 4.6 originally treated "content-sized, clip-only, never padded
// up" as covering column width too (deliberately never widening a
// column to use free terminal space); pg2-hlpuv consciously supersedes
// that half of the choice while leaving paneFrame's own box-sizing
// mechanism untouched.
//
// The top border's dash count is `inner - title width` (not `- 1`)
// [pg2-8iy1m]: every content/bottom line totals `inner + 4` columns
// ("│ " + inner + " │", "└" + (inner+2) + "┘"), so the top border -- "┌ " +
// title + " " + dashes + "┐" = title_width + dashes + 4 -- needs
// dashes = inner - title_width to reach that same total. The previous
// "- 1" made the top border exactly one column narrower than every other
// line of the box on every single render, regardless of overflow -- the
// box's own frame didn't align with itself.
func paneFrame(title string, lines []string) string {
	inner := lipgloss.Width(title) + 2
	for _, l := range lines {
		if w := lipgloss.Width(l); w > inner {
			inner = w
		}
	}
	var b strings.Builder
	b.WriteString("┌ " + title + " " + strings.Repeat("─", max(0, inner-lipgloss.Width(title))) + "┐\n")
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
