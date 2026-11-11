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

// renderTopZone renders the ONE pinned top zone: the paused-roles banner when a
// level requirement blocks any role (it always wins -- the more actionable,
// operator-facing fact -- so the two wordings are never combined, per
// INV-LIFE-2's mutual exclusivity), the quiescing line when the core itself is
// draining and no role is paused, or the header otherwise [design: Task 4.6 Files (banner.go);
// Task 4.6 Step 3].
func renderTopZone(d topZoneData) string {
	if text := bannerText(d.reply.PausedRoles, d.quiescing, inFlightCount(d.reply), d.reply.Dispatch); text != "" {
		return renderPausedBanner(text, d.width, d.theme)
	}
	return renderHeader(d)
}

// bannerText returns the paused-roles banner's wording when a level
// requirement blocks any role, or an informational quiescing line when the core
// itself is draining and no role is paused -- never both at once (INV-LIFE-2's
// mutual exclusivity). Returns "" when neither applies, telling the caller to
// render the header instead.
//
// The wording is "N roles paused (disk TIGHT, ...)": the resources blocking,
// ordered by the number of roles each blocks (descending, ties by resource
// name), each with the level it is at. Both branches also carry dispatch's own
// busy/N summary (Task 6.5, dispatchSummary below) -- additive, and distinct
// from -- never a replacement for -- inFlight's own "N in flight" wording.
func bannerText(paused []PausedRole, quiescing bool, inFlight int, dispatch Dispatch) string {
	switch {
	case len(paused) > 0:
		noun := "roles"
		if len(paused) == 1 {
			noun = "role"
		}
		return fmt.Sprintf("%d %s paused (%s) · %d in flight · %s",
			len(paused), noun, strings.Join(blockingResources(paused), ", "), inFlight, dispatchSummary(dispatch))
	case quiescing:
		return fmt.Sprintf("quiescing — core is draining toward exit (no role held by levels) · %s", dispatchSummary(dispatch))
	default:
		return ""
	}
}

// blockingResources lists the resources that block the paused roles as "disk
// TIGHT" (the resource and the level it is at; "no reading" for a silent one),
// ordered by the number of roles each blocks, descending, ties by resource name.
func blockingResources(paused []PausedRole) []string {
	type blocker struct {
		name, label string
		roles       int
	}
	byName := map[string]*blocker{}
	for _, p := range paused {
		seen := map[string]bool{}
		for _, f := range p.Failing {
			if seen[f.Resource] {
				continue
			}
			seen[f.Resource] = true
			b := byName[f.Resource]
			if b == nil {
				label := textsafe.Sanitize(f.Resource) + " " + textsafe.Sanitize(f.Level)
				if f.Kind == "unknown" {
					label = textsafe.Sanitize(f.Resource) + " no reading"
				}
				b = &blocker{name: f.Resource, label: label}
				byName[f.Resource] = b
			}
			b.roles++
		}
	}
	all := make([]*blocker, 0, len(byName))
	for _, b := range byName {
		all = append(all, b)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].roles != all[j].roles {
			return all[i].roles > all[j].roles
		}
		return all[i].name < all[j].name
	})
	out := make([]string, 0, len(all))
	for _, b := range all {
		out = append(out, b.label)
	}
	return out
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
// secondary line, and levels/config trail on a tertiary line with the
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
	logSize := styledQueueLogSummary(d.reply.QueueLog, d.theme)

	var lines []string
	switch tier {
	case render.TierTiny:
		lines = []string{
			fmt.Sprintf(" pg-router  core: %s       up %s", coreStateLabel(ci.State), uptime),
			" levels: " + levelsSummary(d.reply) + "  " + busy + "  " + logSize,
		}
	default:
		lines = []string{
			// Line 1: the at-a-glance health signal, leading -- everything
			// an operator needs to answer "is the core healthy" without
			// reading further.
			fmt.Sprintf(" [%s] pg-router · core: %s · up %s", health, coreStateLabel(ci.State), uptime),
			// Line 2: version detail, secondary to health/uptime.
			fmt.Sprintf(" client v%s · core v%s", d.clientVersion, coreVersion),
			// Line 3: levels + dispatch concurrency + config path, tertiary
			// -- the config path is muted so it competes least for
			// attention (it's the longest, least actionable field on the
			// banner).
			" levels: " + levelsSummary(d.reply) + "   " + busy + "   " + logSize + "   config: " + d.theme.Muted.Render(configPath),
		}
	}

	out := strings.Join(lines, "\n")
	return render.Block(out, render.EffectiveWidth(d.width))
}

// Log-size bands (bead pg2-5d3ui): the header's log summary is plain below
// logBandWarnPercent, yellow (theme.Cooling) from there up to the soft threshold,
// and red (theme.Failing) at or above logBandCritPercent — which is the soft
// threshold (90% of the limit) — or whenever the state is not "ok". Documented
// here, in the TUI legend and in the README.
const (
	logBandWarnPercent = 70.0
	logBandCritPercent = 90.0
)

// queueLogSummary renders the durable queue log's size for the header: "log:
// 31.7 MiB / 64.0 MiB (49%)" against a core that reports a limit, "log: 31.7
// MiB" against one that does not, followed by a plain-words tag when the log is
// not healthy ("emitters halted", "FULL, rejecting events", "UNWRITABLE,
// rejecting events") so the state survives with color off.
func queueLogSummary(l QueueLog) string {
	out := "log: " + humanBytes(l.Bytes)
	if l.LimitBytes > 0 {
		out += fmt.Sprintf(" / %s (%.0f%%)", humanBytes(l.LimitBytes), l.Percent)
	}
	switch l.State {
	case "emitters_halted":
		out += " · emitters halted"
	case "log_full":
		out += " · FULL, rejecting events"
	case "log_unwritable":
		out += " · UNWRITABLE, rejecting events"
	}
	return out
}

// Log-size band names (logBand).
const (
	bandOK   = "ok"
	bandWarn = "warn"
	bandCrit = "crit"
)

// logBand classifies l for coloring: crit when the state is not "ok" or the log
// is at/over logBandCritPercent of its limit, warn from logBandWarnPercent, else
// ok. A core that reports no limit is always ok (nothing to measure against).
func logBand(l QueueLog) string {
	switch {
	case l.State != "" && l.State != "ok", l.LimitBytes > 0 && l.Percent >= logBandCritPercent:
		return bandCrit
	case l.LimitBytes > 0 && l.Percent >= logBandWarnPercent:
		return bandWarn
	}
	return bandOK
}

// styledQueueLogSummary is queueLogSummary colored by logBand.
func styledQueueLogSummary(l QueueLog, theme render.Theme) string {
	text := queueLogSummary(l)
	switch logBand(l) {
	case bandCrit:
		return theme.Failing.Render(text)
	case bandWarn:
		return theme.Cooling.Render(text)
	}
	return text
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

// levelsSummary renders the header's short levels token. The header only shows
// when no role is paused, so the token says the levels hold nothing back; it is
// deliberately short so the Tiny tier keeps its log-size column.
func levelsSummary(StatusReply) string { return "ok" }

// anyRolePaused reports whether a level requirement blocks any role, the
// aggregate the pane dimming and the paused-roles banner key on.
func anyRolePaused(paused []PausedRole) bool { return len(paused) > 0 }
