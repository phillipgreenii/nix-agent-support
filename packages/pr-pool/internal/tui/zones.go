// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.6) carries the pinned zone ladder: zoneSpec and layoutZones,
// adapted from pa-monitor's own layout.go (packages/pa-monitor/internal/
// tui/layout.go) with one new field pa-monitor's version does not have --
// pinned, a zone that NEVER drops regardless of height pressure [design:
// Task 4.6 Contract; ux-6].
package tui

import (
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// pinnedFloorHeight is the height below which even the fill zone is
// dropped -- only the two pinned zones (top zone + footer) render at all
// [design: Task 4.6 Interfaces; Task 4.1 (zone ladder table)]. Chosen to
// match the design's own literal floor: "below terminal height 4, only the
// two pinned zones render."
const pinnedFloorHeight = 4

// zoneSpec describes one row group in the main screen's zone ladder.
//
// Non-fill zones contribute a fixed pre-rendered string. The fill zone
// (there must be exactly one) is rendered last with whatever rows remain
// after every surviving non-fill, non-pinned zone has claimed its share.
//
// pinned zones NEVER drop, no matter how much the terminal height is
// squeezed -- not even when their own combined line count exceeds the
// requested height. dropOrder is consulted only for non-pinned, non-fill
// zones: when the terminal is too short to fit the desired layout, zones
// with the smallest dropOrder are removed first until the fill zone has at
// least 1 row, until only pinned+fill zones remain, or (below
// pinnedFloorHeight) until only the pinned zones remain [design: Task 4.6
// Interfaces; Task 4.6 Step 2].
type zoneSpec struct {
	name      string
	content   string // for non-fill zones; ignored when fill=true
	fill      bool   // exactly one zone must set this
	pinned    bool   // NEW vs. pa-monitor: never drops, even under extreme height pressure
	dropOrder int    // ignored when pinned or fill

	renderFill func(height int) string
}

// lineCount returns the number of "\n"-separated lines a zone contributes.
// Non-fill (including pinned): count "\n" + 1 in content. Fill: not used
// (caller supplies height).
func (z zoneSpec) lineCount() int {
	if z.fill || z.content == "" {
		return 0
	}
	return strings.Count(z.content, "\n") + 1
}

// layoutZones returns a string laid out for a terminal `height` rows tall,
// each line clipped to `width` visible columns via render.Block.
//
// When height == 0 the function returns every zone concatenated in source
// order with no padding/truncation/dropping at all (test/headless mode --
// callers driving a real terminal always pass a positive height).
//
// Otherwise: pinned zones always render in full and are never candidates
// for the drop search. Below pinnedFloorHeight, even the fill zone is
// dropped -- only the pinned zones render. Above that floor, non-pinned,
// non-fill zones are dropped by ascending dropOrder (smallest drops first)
// until the fill zone would get at least 1 row, or until none are left to
// drop.
//
// The returned string normally has exactly `height` lines. The ONE
// exception is the pathological case where the pinned zones' OWN combined
// line count already meets or exceeds `height`: they still render in full
// (never truncated -- that would defeat "pinned"), so the output may then
// be longer than `height` [design: Task 4.6 Validation ("PinnedNeverDrops");
// Task 4.6 Acceptance Criteria].
func layoutZones(zones []zoneSpec, width, height int) string {
	if height == 0 {
		return concatZones(zones, 0, width)
	}

	if height < pinnedFloorHeight {
		out := concatZones(pinnedOnly(zones), 0, width)
		return padOrExtend(out, height)
	}

	survivors := append([]zoneSpec(nil), zones...)
	bodyHeight := computeBodyHeight(survivors, height)
	for bodyHeight < 1 {
		idx := highestPriorityDroppable(survivors)
		if idx < 0 {
			break // only pinned (+ fill) zones remain
		}
		survivors = append(survivors[:idx], survivors[idx+1:]...)
		bodyHeight = computeBodyHeight(survivors, height)
	}
	if bodyHeight < 0 {
		// The pinned zones alone already consume more than `height` --
		// there is no room left for the fill zone at all.
		bodyHeight = 0
	}

	out := concatZones(survivors, bodyHeight, width)
	return padOrExtend(out, height)
}

// pinnedOnly returns the subset of zones with pinned == true, in source
// order -- used by the pinnedFloorHeight branch, which drops everything
// else including the fill zone.
func pinnedOnly(zones []zoneSpec) []zoneSpec {
	out := make([]zoneSpec, 0, len(zones))
	for _, z := range zones {
		if z.pinned {
			out = append(out, z)
		}
	}
	return out
}

// computeBodyHeight = height - sum(lineCount for pinned and non-fill
// survivors). May go negative.
func computeBodyHeight(zones []zoneSpec, height int) int {
	used := 0
	for _, z := range zones {
		if !z.fill {
			used += z.lineCount()
		}
	}
	return height - used
}

// highestPriorityDroppable returns the index in zones of the surviving
// zone with the smallest dropOrder among those that are neither pinned nor
// fill, or -1 if none remain. This is the drop search Task 4.6's own
// acceptance bar requires never return a pinned zone's index.
func highestPriorityDroppable(zones []zoneSpec) int {
	idx := -1
	for i, z := range zones {
		if z.fill || z.pinned {
			continue
		}
		if idx < 0 || z.dropOrder < zones[idx].dropOrder {
			idx = i
		}
	}
	return idx
}

// concatZones joins surviving zones in source order. The fill zone is
// rendered with bodyHeight (skipped entirely when bodyHeight <= 0). Width
// is forwarded to render.Block as the final per-line clip.
func concatZones(zones []zoneSpec, bodyHeight, width int) string {
	parts := make([]string, 0, len(zones))
	for _, z := range zones {
		var s string
		switch {
		case z.fill:
			if z.renderFill != nil && bodyHeight > 0 {
				s = z.renderFill(bodyHeight)
			}
		default:
			s = z.content
		}
		parts = append(parts, s)
	}
	joined := strings.Join(parts, "\n")
	if width > 0 {
		joined = render.Block(joined, width)
	}
	return joined
}

// padOrExtend pads s with blank trailing lines until it has at least
// `height` lines. Unlike pa-monitor's own padOrTruncate precedent, it never
// TRUNCATES: a pinned zone's content must survive verbatim even when the
// pinned zones' combined height exceeds `height` (see layoutZones's doc).
// In the ordinary case (no pinned-overflow), the caller has already sized
// the fill zone so the total comes out to exactly `height` lines, and this
// is a no-op.
func padOrExtend(s string, height int) string {
	if height <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
