package marker

import "strings"

// HTMLMarker is the invisible marker stamped on every body pg-pr/agents post.
// Invisible in the GitHub UI and unlikely to collide with anything a human
// types, so IsOurs can distinguish agent-authored from human-authored content
// without relying on the GitHub login (pg-pr posts under the user's own
// account — there is no bot badge to key on).
const HTMLMarker = "<!-- pg-pr -->"

// VisibleAttribution is the human-readable banner Stamp renders beneath the
// invisible marker so a reader can tell at a glance the comment was posted by
// automation and not written directly by the account owner. Leading "> " makes
// GitHub render it as a blockquote, visually separating it from the body.
const VisibleAttribution = "> " + Glyph + " _Automated comment from **pg-pr** (an agent) — not written directly by a human._"

// Stamp prefixes body with the invisible marker (for machine detection) and the
// visible attribution banner (so human readers know it is agent-authored).
// Idempotent — a body already carrying the invisible marker is returned
// unchanged, so re-posts don't stack banners.
func Stamp(body string) string {
	if strings.Contains(body, HTMLMarker) {
		return body
	}
	return HTMLMarker + "\n" + VisibleAttribution + "\n\n" + body
}

// IsOurs reports whether body was produced by pg-pr/an agent — matching the new
// HTML marker OR the legacy Glyph (transition window).
func IsOurs(body string) bool {
	return strings.Contains(body, HTMLMarker) || strings.Contains(body, Glyph)
}

// Strip removes pg-pr attribution markup — the invisible HTMLMarker, the visible
// attribution banner, and the legacy trailing Glyph — returning the underlying
// human-meaningful content with surrounding whitespace trimmed. Dedup and
// fingerprint keys use it so comparisons hinge on the actual content and stay
// stable across old→new marker formats (and don't collide once the banner
// dominates a fixed-width key window). Safe on unmarked bodies.
func Strip(body string) string {
	// Remove the full visible banner before the bare Glyph so the banner is
	// stripped as a unit (it embeds the Glyph).
	body = strings.ReplaceAll(body, VisibleAttribution, "")
	body = strings.ReplaceAll(body, HTMLMarker, "")
	body = strings.ReplaceAll(body, Glyph, "")
	return strings.TrimSpace(body)
}
