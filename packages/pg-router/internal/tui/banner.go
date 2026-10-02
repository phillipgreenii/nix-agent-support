// Package tui implements pg-router's operator-facing terminal UI. This file
// (Task 4.6) carries the pinned TOP zone: either the header (core identity,
// version pair, gates summary) or the PAUSED banner, mutually exclusive
// [design: Task 4.6 Files (banner.go); Binding decisions 6]. This is where
// the sibling register-tracking bead pg2-gkpjz's "Phase 4: banner
// rendering" stage lands.
package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// topZoneData is every field renderTopZone needs, collected independently
// of *Model so both Model.View (model.go) and this file's own tests can
// build it directly [design: Task 4.6 Interfaces].
type topZoneData struct {
	clientVersion string
	reply         StatusReply
	quiescing     bool // m.screen == screenQuiescing
	width         int
	theme         render.Theme
}

// renderTopZone renders the ONE pinned top zone: the PAUSED banner when any
// gate is set (it always wins -- the more actionable, operator-facing fact
// -- so the two wordings are never combined, per INV-LIFE-2's mutual
// exclusivity), the quiescing line when the core itself is draining and no
// gate is set, or the header otherwise [design: Task 4.6 Files (banner.go);
// Task 4.6 Step 3].
func renderTopZone(d topZoneData) string {
	if text := bannerText(gateTypes(d.reply.Gates), d.quiescing, inFlightCount(d.reply), d.reply.Dispatch); text != "" {
		return renderPausedBanner(text, d.width, d.theme)
	}
	return renderHeader(d)
}

// bannerText returns the PAUSED banner's wording when gated, or an
// informational quiescing line when the core itself is draining and no
// gate is set -- never both at once (INV-LIFE-2's mutual exclusivity).
// Returns "" when neither applies, telling the caller to render the header
// instead [design: Task 4.6 Step 3; Binding decisions 6].
//
// Both branches also carry dispatch's own busy/N summary (Task 6.5,
// dispatchSummary below) -- additive, and distinct from -- never a
// replacement for -- inFlight's own pinned "N in flight" wording, which is
// UNCHANGED by this addition [design: Binding decision 5].
func bannerText(gates []string, quiescing bool, inFlight int, dispatch Dispatch) string {
	switch {
	case len(gates) > 0:
		// SYSTEM_PAUSE keeps the operator-facing "PAUSED" wording; any other gate
		// reads "GATED" -- a gate acts on the participants that block on it, so
		// the banner names which gates are in force rather than claiming the
		// whole pool is halted.
		lead := "GATED"
		for _, g := range gates {
			if g == core.GateSystemPause {
				lead = "PAUSED"
			}
		}
		return fmt.Sprintf("%s — routing halted for blocked participants (%s) · %d in flight · %s",
			lead, strings.Join(gates, ", "), inFlight, dispatchSummary(dispatch))
	case quiescing:
		return fmt.Sprintf("quiescing — core is draining toward exit (no gate set) · %s", dispatchSummary(dispatch))
	default:
		return ""
	}
}

// dispatchSummary renders the additive dispatch: { busy, total } field
// (Task 6.5) as a short "busy B/T" token, fitting alongside the existing
// gate/banner line in every tier -- the real fan-out ratio
// (Queue.SessionsInFlight() over the active listener count), never to be
// confused with the pinned banner's own "N in flight" reading above (that
// one reads Deliveries; this one reads the new dispatch object) [design:
// Task 6.5 Files; Binding decision 5].
func dispatchSummary(d Dispatch) string {
	return fmt.Sprintf("busy %d/%d", d.Busy, d.Total)
}

// inFlightCount reports the number of deliveries currently in a handler's
// custody. Pre-extraction (today's shipped listener), this legitimately
// saturates at 0 or 1 -- Task 2.2 established custody tracks at most one
// in-flight session per handler; the wire's `deliveries` array (reply.go's
// Delivery, StatusReply.Deliveries) is the real field this reads, carried
// now so the shape is correct once a later phase widens custody tracking
// [design: Task 4.6 (§4.2 banner)].
func inFlightCount(reply StatusReply) int {
	return len(reply.Deliveries)
}

// renderPausedBanner renders the mutually-exclusive PAUSED/quiescing
// wording as reverse video (never the double-width `██` glyph v1's own
// mockup used -- ux-3/ux-11, spec §10) plus the plain text token itself, so
// the distinction survives even with color off.
func renderPausedBanner(text string, width int, theme render.Theme) string {
	styled := theme.Paused.Reverse(true).Render(text)
	return render.Line(styled, render.EffectiveWidth(width))
}

// renderHeader composes the non-paused header: identity, the client/core
// version pair, uptime, and the gates summary line -- adapted per tier
// (Wide/Narrow drop nothing, but spread it across three lines instead of
// two, per pg2-xp415: the pool health signal leads on its own line so "is
// the core healthy" reads at a glance, versions move to their own
// secondary line, and gates/config trail on a tertiary line with the
// config path visually de-emphasized -- Tiny is unchanged, still dropping
// the version pair and config path, per the design's own Tiny mockup)
// [design: Task 4.6 Files (banner.go); §4.3 Tier mockups; pg2-xp415].
func renderHeader(d topZoneData) string {
	tier := render.Tier(d.width)
	ci := d.reply.Core

	coreVersion := textsafe.Sanitize(ci.Version)
	if coreVersion == "" {
		coreVersion = "(unknown)"
	}
	configPath := textsafe.Sanitize(ci.ConfigPath)
	uptime := formatUptime(uptimeSince(ci.StartedAt))

	// renderHeader is only ever reached from renderTopZone once bannerText
	// has already determined the pool is NOT gated (the header and the
	// PAUSED banner are mutually exclusive) -- so the pool health axis's
	// own "paused" rung can never fire here; hasCore is likewise always
	// true (the no-core screen is a distinct screen entirely, screen.go).
	// [design: Task 4.6 Binding decisions (derived health, two axes)].
	health := poolHealthText(
		true, false,
		poolTickWedged(d.reply.LastTickAt, d.reply.TickIntervalMs, time.Now()),
		poolDegraded(d.reply.Registry),
		d.theme,
	)

	// busy is the additive dispatch-concurrency token (Task 6.5) rendered
	// alongside the existing gates line in every tier -- fitting each
	// tier's own column budget via the same render.Block/Line clipping the
	// header already runs through below, never by hand-truncating the text
	// itself [design: Task 6.5 Files; Binding decision 5].
	busy := dispatchSummary(d.reply.Dispatch)
	logSize := queueLogSummary(d.reply.QueueLog)

	var lines []string
	switch tier {
	case render.TierTiny:
		lines = []string{
			fmt.Sprintf(" pg-router  core: %s       up %s", coreStateLabel(ci.State), uptime),
			" gates: " + gatesSummary(d.reply.Gates) + "  " + busy + "  " + logSize,
		}
	default:
		lines = []string{
			// Line 1: the at-a-glance health signal, leading -- everything
			// an operator needs to answer "is the core healthy" without
			// reading further.
			fmt.Sprintf(" [%s] pg-router · core: %s · up %s", health, coreStateLabel(ci.State), uptime),
			// Line 2: version detail, secondary to health/uptime.
			fmt.Sprintf(" client v%s · core v%s", d.clientVersion, coreVersion),
			// Line 3: gates + dispatch concurrency + config path, tertiary
			// -- the config path is muted so it competes least for
			// attention (it's the longest, least actionable field on the
			// banner).
			" gates: " + gatesSummary(d.reply.Gates) + "   " + busy + "   " + logSize + "   config: " + d.theme.Muted.Render(configPath),
		}
	}

	out := strings.Join(lines, "\n")
	return render.Block(out, render.EffectiveWidth(d.width))
}

// queueLogSummary renders the durable queue log's size for the header ("log:
// 31.7 MiB"), the at-a-glance answer to "is the write-ahead log growing without
// bound" (bead pg2-8e0m6).
func queueLogSummary(l QueueLog) string {
	return "log: " + humanBytes(l.Bytes)
}

// humanBytes renders n as a short binary-unit size ("31.7 MiB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// uptimeSince returns time.Since(startedAt), or 0 when startedAt is the
// zero value (the core has not reported one yet -- e.g. a synthetic reply
// in a test).
func uptimeSince(startedAt time.Time) time.Duration {
	if startedAt.IsZero() {
		return 0
	}
	d := time.Since(startedAt)
	if d < 0 {
		return 0
	}
	return d
}

// formatUptime renders d as "<h>h<mm>m", matching the design's own Wide/
// Narrow/Tiny mockups ("up 2h14m") -- deliberately coarser than Go's own
// Duration.String(), which would also print a seconds component.
func formatUptime(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%02dm", h, m)
}

// coreStateLabel names the core's own lifecycle state for the header,
// falling back to a placeholder when the reply has not carried one yet
// (e.g. a synthetic/zero-value reply in a test).
func coreStateLabel(state string) string {
	if state == "" {
		return "(unknown)"
	}
	return textsafe.Sanitize(state)
}

// gatesSummary renders the active gates as a compact list for the header's
// gates line: "none" when nothing is gated, else the sorted TYPEs (at most
// gatesSummaryMax, then "+N more") -- the full detail is the Gates modal's job.
func gatesSummary(gates []Gate) string {
	types := gateTypes(gates)
	if len(types) == 0 {
		return "none"
	}
	if len(types) > gatesSummaryMax {
		more := len(types) - gatesSummaryMax
		types = append(types[:gatesSummaryMax:gatesSummaryMax], fmt.Sprintf("+%d more", more))
	}
	return strings.Join(types, ", ")
}

// gatesSummaryMax bounds how many gate TYPEs the header line spells out.
const gatesSummaryMax = 3

// gateTypes returns the sanitized TYPEs of the active gates, sorted.
func gateTypes(gates []Gate) []string {
	out := make([]string, 0, len(gates))
	for _, g := range gates {
		out = append(out, textsafe.Sanitize(g.Type))
	}
	sort.Strings(out)
	return out
}

// anyGateSet reports whether any gate is in force (the wire lists active gates
// only), the aggregate the pane dimming and the PAUSED/GATED banner key on.
func anyGateSet(gates []Gate) bool { return len(gates) > 0 }
