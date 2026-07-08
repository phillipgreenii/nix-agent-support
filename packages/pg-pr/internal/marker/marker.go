// Package marker centralises the marker policy for comments and reviews posted
// by pg-pr. Every posted body is stamped (via Stamp) with two things: the
// invisible HTMLMarker, which IsOurs detects (plus the legacy Glyph) so
// ingestion can tell agent-authored from human-authored content without relying
// on the GitHub login; and a human-visible attribution banner, so a reader can
// tell the comment was posted by automation and not written directly by the
// account owner (pg-pr posts under the user's own account — there is no bot
// badge). Strip reverses the stamping for content-based dedup/fingerprint keys.
package marker

// Glyph is the agent robot emoji. It marked agent-authored bodies before the
// switch to the invisible HTMLMarker (IsOurs still recognises it for that
// transition window) and now also leads the visible attribution banner (see
// VisibleAttribution in htmlmarker.go).
const Glyph = "\U0001F916" // 🤖
