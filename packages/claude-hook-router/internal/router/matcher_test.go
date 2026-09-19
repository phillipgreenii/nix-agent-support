package router

import "testing"

// TestMatchesEvent is a regression lock over the matcher shapes ADR 0071
// §2.3 says are actually in use today: absent/"*" (match-all), an exact
// string, and a pipe-alternation of exact strings. It deliberately does NOT
// assert Go-RE2/JS-regex parity for a genuinely regex-shaped matcher — that
// risk is mitigated at the generator (packet A1), not here (ADR 0071 §4
// Phase B, B2 "Matcher evaluation").
func TestMatchesEvent(t *testing.T) {
	cases := []struct {
		name    string
		matcher string
		value   string
		want    bool
	}{
		{"absent matcher matches anything", "", "Bash", true},
		{"absent matcher matches empty value", "", "", true},
		{"star matches anything", "*", "Write", true},
		{"exact string matches itself", "Bash", "Bash", true},
		{"exact string does not match a different tool", "Bash", "Write", false},
		{"exact string is case sensitive", "Bash", "bash", false},
		{"pipe alternation matches first alternative", "Write|Edit", "Write", true},
		{"pipe alternation matches second alternative", "Write|Edit", "Edit", true},
		{"pipe alternation does not match a non-member", "Write|Edit", "Bash", false},
		{"three-way pipe alternation matches the last alternative", "Write|Edit|MultiEdit", "MultiEdit", true},
		{"matcher with hyphen and underscore is still exact-alternation", "session-start_reason", "session-start_reason", true},
		{"matcher with spaces is still exact-alternation", "end reason", "end reason", true},
		{"non-matching exact-alternation charset value fails closed", "Write|Edit", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesEvent(tc.matcher, tc.value); got != tc.want {
				t.Errorf("MatchesEvent(%q, %q) = %v, want %v", tc.matcher, tc.value, got, tc.want)
			}
		})
	}
}

// TestMatchesEventUnparseableRegexFailsClosedForThatDelegateOnly covers the
// documented fail-closed behavior for a matcher that falls outside the
// exact-string/pipe-alternation charset and does not compile as a regex: it
// never matches, but MatchesEvent itself never panics or errors — the
// failure is scoped to that one delegate, not the router.
func TestMatchesEventUnparseableRegexFailsClosedForThatDelegateOnly(t *testing.T) {
	if MatchesEvent("[unclosed", "[unclosed") {
		t.Error("an unparseable regex-shaped matcher must never match, even the literal string that broke it")
	}
}

// TestMatchesEventRegexShapedMatcherIsEvaluatedAsRegex is a narrow
// regression lock that a matcher OUTSIDE the exact-alternation charset is at
// least routed through the regexp path (not silently treated as an exact
// string) — without claiming any Go-RE2/JS-regex parity guarantee.
func TestMatchesEventRegexShapedMatcherIsEvaluatedAsRegex(t *testing.T) {
	if !MatchesEvent("^Ba.*$", "Bash") {
		t.Error("a matcher containing regex metacharacters outside the exact-alternation charset must be evaluated as a regex")
	}
}
