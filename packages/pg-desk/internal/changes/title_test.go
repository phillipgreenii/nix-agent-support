package changes

import (
	"strings"
	"testing"
)

func TestTitleFromFacts(t *testing.T) {
	long := strings.Repeat("é", 100)
	for _, tc := range []struct {
		name, typ, facts, want string
	}{
		{"pr", "pr", `{"pr_show":{"title":"Add retry to client"}}`, "Add retry to client"},
		{"issue", "issue", `{"issue_show":{"title":"Fix the thing"}}`, "Fix the thing"},
		{"thread first line", "thread", `{"thread_show":{"text":"\n  \n  hello there \nsecond line"}}`, "hello there"},
		{"thread truncated to 80 runes", "thread", `{"thread_show":{"text":"` + long + `"}}`, strings.Repeat("é", 80)},
		{"thread no text", "thread", `{"thread_show":{"text":"   "}}`, ""},
		{"pr missing title", "pr", `{"pr_show":{}}`, ""},
		{"pr missing section", "pr", `{}`, ""},
		{"pr title not a string", "pr", `{"pr_show":{"title":5}}`, ""},
		{"pr section not an object", "pr", `{"pr_show":"x"}`, ""},
		{"malformed", "pr", `{not json`, ""},
		{"empty", "pr", ``, ""},
		{"unknown type", "build", `{"pr_show":{"title":"x"}}`, ""},
		{"pr ignores issue section", "pr", `{"issue_show":{"title":"x"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TitleFromFacts(tc.typ, tc.facts); got != tc.want {
				t.Errorf("TitleFromFacts(%q, %s) = %q, want %q", tc.typ, tc.facts, got, tc.want)
			}
		})
	}
}
