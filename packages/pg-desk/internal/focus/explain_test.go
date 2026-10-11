package focus

import (
	"encoding/json"
	"testing"
)

func TestIsAuditBody(t *testing.T) {
	for body, want := range map[string]bool{
		"rule: focus.item\nop: hold":                   true,
		"pg-decider audit\nrule: focus.item\nop: mint": true,
		"  rule: focus.item":                           true,
		"rule: other.rule":                             false,
		"note\nmore\nrule: focus.item":                 false, // the rule line is the first or second line
		"":                                             false,
	} {
		if got := isAuditBody(body); got != want {
			t.Errorf("isAuditBody(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestCommentBodyForms(t *testing.T) {
	for raw, want := range map[string]string{
		`"plain"`:                 "plain",
		`{"body":"b","text":"t"}`: "b",
		`{"text":"t"}`:            "t",
		`{"content":"c"}`:         "c",
		`{"other":"x"}`:           "",
		`42`:                      "",
	} {
		if got := commentBody(json.RawMessage(raw)); got != want {
			t.Errorf("commentBody(%s) = %q, want %q", raw, got, want)
		}
	}
}
