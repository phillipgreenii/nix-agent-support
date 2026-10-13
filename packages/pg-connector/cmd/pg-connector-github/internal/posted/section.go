package posted

import "strings"

// SectionHeadLen is the number of leading hex characters of a head sha that
// name a body section.
const SectionHeadLen = 12

// SectionClose is the delimiter that ends every per-head body section.
const SectionClose = "<!-- /pg-section -->"

// shortHead returns the section key of head: its first SectionHeadLen
// characters, lower-cased. ok is false for a head too short to name a section.
func shortHead(head string) (string, bool) {
	head = strings.ToLower(strings.TrimSpace(head))
	if len(head) < SectionHeadLen {
		return "", false
	}
	return head[:SectionHeadLen], true
}

// SectionOpen returns the delimiter that opens the body section for head:
// "<!-- pg-section head=<sha12> -->". It is "" when head is too short to name
// a section.
func SectionOpen(head string) string {
	short, ok := shortHead(head)
	if !ok {
		return ""
	}
	return "<!-- pg-section head=" + short + " -->"
}

// FindSection finds the per-head body section for head in a review body: the
// text from the opening delimiter to the first closing delimiter after it.
// body[start:end] is the whole section, both delimiters included. ok is false
// when the body holds no section for head, when the opening delimiter has no
// closing delimiter after it, or when head is too short to name a section.
// The delimiter match is on the head's first SectionHeadLen characters, so a
// full sha and its 12-character form find the same section.
//
// This is the single parser of the delimiter: the review_pending lookup (the
// reviewed_head field) and the body writer both use it, so the two never read
// the delimiter differently.
func FindSection(body, head string) (start, end int, ok bool) {
	open := SectionOpen(head)
	if open == "" {
		return 0, 0, false
	}
	start = strings.Index(body, open)
	if start < 0 {
		return 0, 0, false
	}
	closeAt := strings.Index(body[start+len(open):], SectionClose)
	if closeAt < 0 {
		return 0, 0, false
	}
	return start, start + len(open) + closeAt + len(SectionClose), true
}
