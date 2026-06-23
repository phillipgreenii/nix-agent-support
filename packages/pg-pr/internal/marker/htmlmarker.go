package marker

import "strings"

// HTMLMarker is the invisible marker stamped on every body pg-pr/agents post.
// Invisible in the GitHub UI and unlikely to collide with anything a human types.
const HTMLMarker = "<!-- pg-pr -->"

// Stamp prefixes body with the HTML marker (idempotent — won't double-stamp).
func Stamp(body string) string {
	if strings.Contains(body, HTMLMarker) {
		return body
	}
	return HTMLMarker + "\n" + body
}

// IsOurs reports whether body was produced by pg-pr/an agent — matching the new
// HTML marker OR the legacy Glyph (transition window).
func IsOurs(body string) bool {
	return strings.Contains(body, HTMLMarker) || strings.Contains(body, Glyph)
}
