package classify

import "testing"

// PRStateOfFacts is the one open test the re-interpretation paths share with
// the classifier: the merged flag folds in, state is trimmed and lower-cased,
// and facts that do not decode are reported as such.
func TestPRStateOfFacts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		facts string
		state string
		ok    bool
	}{
		{"open", `{"pr_show":{"state":"open"}}`, "open", true},
		{"padded and upper-case", `{"pr_show":{"state":" OPEN "}}`, "open", true},
		{"closed", `{"pr_show":{"state":"closed"}}`, "closed", true},
		{"merged state", `{"pr_show":{"state":"merged"}}`, "merged", true},
		{"merged flag beats open", `{"pr_show":{"state":"open","merged":true}}`, "merged", true},
		{"unrecognised", `{"pr_show":{"state":"weird"}}`, "", true},
		{"no pr_show", `{}`, "", true},
		{"empty pr_show", `{"pr_show":{}}`, "", true},
		{"not json", `not json`, "", false},
		{"empty", ``, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, ok := PRStateOfFacts([]byte(tc.facts))
			if state != tc.state || ok != tc.ok {
				t.Fatalf("PRStateOfFacts(%q) = %q, %v, want %q, %v", tc.facts, state, ok, tc.state, tc.ok)
			}
		})
	}
}
