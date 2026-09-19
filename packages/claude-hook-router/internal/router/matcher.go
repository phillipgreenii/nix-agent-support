package router

import (
	"regexp"
	"strings"
)

// exactAlternationCharset matches a matcher string made up ONLY of
// letters/digits/underscore/hyphen/spaces/comma/pipe — the subset ADR 0071
// §2.3 says Claude Code itself treats as exact-string-or-pipe-alternation
// rather than a regex.
var exactAlternationCharset = regexp.MustCompile(`^[A-Za-z0-9_\- ,|]*$`)

// MatchesEvent replicates Claude Code's own matcher semantics (ADR 0071
// §2.3, itself replicating what §1.2 measured against real Claude Code
// behavior): absent/"*"/"" is match-all; a matcher containing only
// letters/digits/_/-/spaces/,/| is exact-string-or-pipe-alternation against
// value; anything else is evaluated as an unanchored regex. A matcher that
// fails to compile as a regex can never match — this is a deliberate
// fail-closed-on-that-one-delegate choice, not a panic or a router-wide
// failure (packet A1's generator is the actual mitigation for a delegate
// shipping an unreviewed regex-shaped matcher; see §2.3's "Risk flagged for
// implementation").
func MatchesEvent(matcher, value string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	if exactAlternationCharset.MatchString(matcher) {
		for _, alt := range strings.Split(matcher, "|") {
			if alt == value {
				return true
			}
		}
		return false
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		return false
	}
	return re.MatchString(value)
}
