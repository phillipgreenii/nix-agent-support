// Package threadref recognises references to pull requests inside free-form
// thread text. It is shared by the run thread command and the thread link
// extractor so the recognition rules live in one place.
package threadref

import "regexp"

// permalinkRE finds a GitHub PR URL anywhere in free-form text. Unlike an
// anchored `<pr>` argument parser, a thread's text is prose that may embed a
// permalink anywhere, with trailing punctuation or more sentence after it,
// so this pattern is deliberately NOT end-anchored and stops at the number.
var permalinkRE = regexp.MustCompile(`/pull/(\d+)\b`)

// ScanPermalinks returns the deduplicated set of PR entity ids
// ("<repo>#<n>", the same form used everywhere else in this codebase) named
// by a GitHub PR URL anywhere in text. One configured repository is
// supported, so whatever owner/repo the URL itself names is not consulted;
// every match resolves against repo. Returns nil when text contains no
// PR-URL-shaped substring.
func ScanPermalinks(text, repo string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range permalinkRE.FindAllStringSubmatch(text, -1) {
		id := repo + "#" + m[1]
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
