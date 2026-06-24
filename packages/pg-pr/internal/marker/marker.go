// Package marker centralises the marker policy for comments and reviews posted
// by pg-pr. Every posted body is stamped (via Stamp) with the invisible
// HTMLMarker; IsOurs detects it (plus the legacy Glyph during the transition
// window) so ingestion can distinguish agent-authored content from
// human-authored content without relying on the GitHub login.
package marker

// Glyph is the legacy agent marker. It is retained only so IsOurs still
// recognises comments pg-pr posted before the switch to HTMLMarker; new bodies
// are stamped with HTMLMarker (see htmlmarker.go).
const Glyph = "\U0001F916" // 🤖
