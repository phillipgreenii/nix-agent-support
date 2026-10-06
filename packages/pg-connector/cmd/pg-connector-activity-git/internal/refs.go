// refs.go: ticket and pull-request reference extraction from a commit message.
//
// extractRefs is pure: it takes the subject and body strings and returns the
// references found in them, calling out to nothing (no git, no network).
package internal

import (
	"regexp"
	"strings"
)

// refPattern finds, scanning left to right, three kinds of reference. The
// alternation order matters only where two kinds could start at the same
// position; the qualified pull-request form is tried first so that
// "acme/widgets#12" is consumed whole and its "#12" tail is never seen again
// as a bare reference.
//
//	group 1: owner/repo#N   (reported exactly as written)
//	group 2: PROJ-123       (upper-case letters, a hyphen, digits)
//	group 3: #N             (bare pull-request or issue number)
var refPattern = regexp.MustCompile(
	`([A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*#\d+\b)` +
		`|(\b[A-Z]+-\d+\b)` +
		`|(#\d+\b)`,
)

// extractRefs returns the distinct references in subject and body, in order
// of first appearance, exactly as written. It never returns nil: a message
// with no references yields an empty slice, so it marshals as [].
func extractRefs(subject, body string) []string {
	text := subject + "\n" + body
	refs := []string{}
	seen := map[string]bool{}
	for _, m := range refPattern.FindAllStringSubmatchIndex(text, -1) {
		start := m[0]
		// A bare #N glued to a word character or path separator ("abc#12",
		// "dir/#12") is an anchor or fragment, not a reference; Go's regexp has
		// no lookbehind, so the preceding byte is checked here.
		if m[6] >= 0 && start > 0 && isRefGlue(text[start-1]) {
			continue
		}
		ref := text[m[0]:m[1]]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs
}

func isRefGlue(c byte) bool {
	return c == '_' || c == '/' || (c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || strings.ContainsRune("-.", rune(c))
}
