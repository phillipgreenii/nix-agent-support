// Package marker centralises the 🤖 marker policy for comments and reviews
// posted by pg-pr. The CLI auto-applies the marker so that downstream
// review-feedback tooling can distinguish agent-authored content from
// human-authored content.
package marker

// Glyph is the canonical agent marker.
const Glyph = "\U0001F916" // 🤖
