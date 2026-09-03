// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.6) carries the pinned TOP zone: either the header (core identity,
// version pair, gates summary) or the PAUSED banner, mutually exclusive
// [design: Task 4.6 Files (banner.go); Binding decisions 6]. This is where
// the sibling register-tracking bead pg2-gkpjz's "Phase 4: banner
// rendering" stage lands.
package tui

import (
	"fmt"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/textsafe"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
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
	gated := anyGateSet(d.reply.Gates)
	if text := bannerText(gated, d.quiescing, inFlightCount(d.reply)); text != "" {
		return renderPausedBanner(text, d.width, d.theme)
	}
	return renderHeader(d)
}

// bannerText returns the PAUSED banner's wording when gated, or an
// informational quiescing line when the core itself is draining and no
// gate is set -- never both at once (INV-LIFE-2's mutual exclusivity).
// Returns "" when neither applies, telling the caller to render the header
// instead [design: Task 4.6 Step 3; Binding decisions 6].
func bannerText(gated, quiescing bool, inFlight int) string {
	switch {
	case gated:
		return fmt.Sprintf("PAUSED — dispatch halted · %d in flight", inFlight)
	case quiescing:
		return "quiescing — core is draining toward exit (no gate set)"
	default:
		return ""
	}
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
// (Wide/Narrow drop nothing from this two-line shape; Tiny drops the
// version pair and config path, per the design's own Tiny mockup)
// [design: Task 4.6 Files (banner.go); §4.3 Tier mockups].
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

	var line1, line2 string
	switch tier {
	case render.TierTiny:
		line1 = fmt.Sprintf(" pr-pool  core: %s       up %s", coreStateLabel(ci.State), uptime)
		line2 = " gates: " + gatesSummary(d.reply.Gates)
	default:
		line1 = fmt.Sprintf(
			" pr-pool          core: %s v%s · core v%s   up %s   [%s]",
			coreStateLabel(ci.State), d.clientVersion, coreVersion, uptime, health,
		)
		line2 = " gates: " + gatesSummary(d.reply.Gates) + "     config: " + configPath
	}

	out := line1 + "\n" + line2
	return render.Block(out, render.EffectiveWidth(d.width))
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

// gatesSummary renders both of INV-LIFE-2's named gates as a compact
// checkbox pair -- "quota[.] cicd[.]" when clear, "quota[X]" when set --
// matching the design's own Wide/Narrow/Tiny mockups (§4.3) exactly.
func gatesSummary(gates []Gate) string {
	return fmt.Sprintf(
		"quota[%s] cicd[%s]",
		gateCheckbox(gates, core.GateQuotaPaused),
		gateCheckbox(gates, core.GateCICDDown),
	)
}

func gateCheckbox(gates []Gate, name string) string {
	for _, g := range gates {
		if g.Name == name && g.Set {
			return "X"
		}
	}
	return "."
}

// anyGateSet reports the EFFECTIVE (OR'd) aggregate gate state (ux-7): true
// when either of INV-LIFE-2's two named gates is currently set, regardless
// of which one.
func anyGateSet(gates []Gate) bool {
	for _, g := range gates {
		if g.Set {
			return true
		}
	}
	return false
}
