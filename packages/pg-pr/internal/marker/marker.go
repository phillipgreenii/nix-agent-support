// Package marker centralises the 🤖 marker policy for comments and reviews
// posted by pg-pr. The CLI auto-applies the marker so that downstream
// review-feedback tooling can distinguish agent-authored content from
// human-authored content.
package marker

import "strings"

// Glyph is the canonical agent marker.
const Glyph = "\U0001F916" // 🤖

// Markerify appends the agent glyph to the body if it isn't already
// present. Bodies that already contain the glyph are returned unchanged
// (avoids stacking on re-posts).
func Markerify(body string) string {
	if strings.Contains(body, Glyph) {
		return body
	}
	trimmed := strings.TrimRight(body, "\n")
	if trimmed == "" {
		return Glyph
	}
	return trimmed + "\n\n" + Glyph
}
