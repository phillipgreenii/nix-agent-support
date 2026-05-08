package tui

import (
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// zoneSpec describes one row group in (*Model).View()'s output.
//
// Non-fill zones contribute a fixed pre-rendered string. The fill zone (there
// must be exactly one) is rendered last with whatever rows remain after all
// surviving non-fill zones have claimed their share.
//
// dropOrder is consulted only for non-fill zones. When the terminal is too
// short to fit the desired layout (sum(non-fill heights) >= height), zones
// with the smallest dropOrder are removed first until the fill zone has at
// least 1 row, or until only the fill zone remains.
type zoneSpec struct {
	name       string
	content    string // for non-fill zones; ignored when fill=true
	fill       bool   // exactly one zone must set this
	dropOrder  int    // smaller = drops first; ignored when fill=true
	renderFill func(height int) string
}

// lineCount returns the number of "\n"-separated lines a zone contributes.
// Non-fill: count "\n" + 1 in content. Fill: not used (caller supplies height).
func (z zoneSpec) lineCount() int {
	if z.fill || z.content == "" {
		return 0
	}
	return strings.Count(z.content, "\n") + 1
}

// layoutZones returns a string with exactly `height` "\n"-separated lines
// when height > 0. When height == 0 the function returns the zones
// concatenated in source order with no padding or truncation (test/headless
// mode — caller is expected to bypass for headless rendering).
//
// Width is forwarded to wrap.Block as a final per-line clip so every emitted
// line satisfies lipgloss.Width(line) <= width.
func layoutZones(zones []zoneSpec, width, height int) string {
	if height == 0 {
		return concatZones(zones, 0, width)
	}

	survivors := append([]zoneSpec(nil), zones...)
	bodyHeight := computeBodyHeight(survivors, height)
	for bodyHeight < 1 {
		idx := highestPriorityNonFill(survivors)
		if idx < 0 {
			break // only fill zone left; let body claim whatever height is
		}
		survivors = append(survivors[:idx], survivors[idx+1:]...)
		bodyHeight = computeBodyHeight(survivors, height)
	}
	if bodyHeight < 1 {
		bodyHeight = height // only fill zone remains; let it consume everything
	}

	out := concatZones(survivors, bodyHeight, width)
	return padOrTruncate(out, height)
}

// computeBodyHeight = height - sum(lineCount for non-fill survivors). May go negative.
func computeBodyHeight(zones []zoneSpec, height int) int {
	used := 0
	for _, z := range zones {
		if !z.fill {
			used += z.lineCount()
		}
	}
	return height - used
}

// highestPriorityNonFill returns the index in zones of the surviving non-fill
// zone with the smallest dropOrder, or -1 if none.
func highestPriorityNonFill(zones []zoneSpec) int {
	idx := -1
	for i, z := range zones {
		if z.fill {
			continue
		}
		if idx < 0 || z.dropOrder < zones[idx].dropOrder {
			idx = i
		}
	}
	return idx
}

// concatZones joins surviving zones in source order. The fill zone is
// rendered with bodyHeight. Width is forwarded to wrap.Block as the final
// per-line clip.
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
		joined = wrap.Block(joined, width)
	}
	return joined
}

// padOrTruncate returns s with exactly height "\n"-separated lines.
// Shorter: pads with empty lines at the bottom. Longer: keeps the first
// `height` lines and discards the rest.
func padOrTruncate(s string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
