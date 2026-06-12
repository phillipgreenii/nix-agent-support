package pane

import "testing"

func TestReLiveCounter(t *testing.T) {
	cases := []struct {
		name  string
		pane  string
		match bool
	}{
		{"thinking counter", "✽ Envisioning… (5s · ↓ 13 tokens · thinking with xhigh effort)", true},
		{"two-digit counter", "✽ Working… (28s · ↓ 99 tokens · thinking with xhigh effort)", true},
		{"streaming prose has no counter", "Here is a thorough essay on Unix pipes.\n⏺ Thought for 4s", false},
		{"static rewound input box", "❯ Think step by step in extensive detail...\n  -- INSERT --", false},
		{"empty pane", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReLiveCounter.MatchString(tc.pane); got != tc.match {
				t.Errorf("ReLiveCounter.MatchString(%q) = %v, want %v", tc.pane, got, tc.match)
			}
		})
	}
}
