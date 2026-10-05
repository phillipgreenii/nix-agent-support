package classify

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSourceTerminal(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	recent := now.Add(-time.Hour).Format(time.RFC3339)
	old := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	for _, tc := range []struct {
		name  string
		typ   string
		facts string
		want  bool
	}{
		{"closed pr", "pr", `{"pr_show":{"state":"closed"}}`, true},
		{"merged pr by flag", "pr", `{"pr_show":{"state":"open","merged":true}}`, true},
		{"merged pr by state", "pr", `{"pr_show":{"state":"MERGED"}}`, true},
		{"open pr", "pr", `{"pr_show":{"state":"open"}}`, false},
		{"pr without show", "pr", `{}`, false},
		{"pr empty payload", "pr", ``, false},
		{"done issue", "issue", `{"issue_show":{"state":"done"}}`, true},
		{"closed issue upper case", "issue", `{"issue_show":{"state":"Closed"}}`, true},
		{"open issue", "issue", `{"issue_show":{"state":"open"}}`, false},
		{"in progress issue", "issue", `{"issue_show":{"state":"in_progress"}}`, false},
		{"issue without state", "issue", `{"issue_show":{}}`, false},
		{"quiet thread", "thread", `{"thread_show":{"last_reply_at":"` + old + `"}}`, true},
		{"active thread", "thread", `{"thread_show":{"last_reply_at":"` + recent + `"}}`, false},
		{"thread without last reply", "thread", `{"thread_show":{}}`, false},
		{"unknown type", "other", `{"pr_show":{"state":"closed"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SourceTerminal(tc.typ, json.RawMessage(tc.facts), now, week); got != tc.want {
				t.Errorf("SourceTerminal = %v, want %v", got, tc.want)
			}
		})
	}
	// A zero window means the 7 day default.
	quiet := json.RawMessage(`{"thread_show":{"last_reply_at":"` + old + `"}}`)
	if !SourceTerminal("thread", quiet, now, 0) {
		t.Error("zero window must default to 7 days")
	}
}
