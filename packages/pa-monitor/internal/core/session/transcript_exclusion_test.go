package session

import (
	"testing"
)

// TestIsTranscriptFile is the ADR 0021 §2 truth table: a real transcript ends in
// .jsonl but NOT .status.jsonl, and the .status.last hash sidecar is also excluded.
func TestIsTranscriptFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"abc-123.jsonl", true},
		{"a.b.c.jsonl", true}, // ordinary dotted session id is still a transcript
		{"abc-123.status.jsonl", false},
		{"abc-123.status.last", false},
		{"abc-123.json", false}, // session record, not a transcript
		{"agent-1.jsonl", true},
		{"", false},
		{".jsonl", true},       // suffix-only edge: still a .jsonl, not a status file
		{"status.jsonl", true}, // no session-id prefix but not the .status.jsonl suffix form
		{"x.status.jsonl", false},
	}
	for _, c := range cases {
		if got := IsTranscriptFile(c.name); got != c.want {
			t.Errorf("IsTranscriptFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestIsStatusSiblingFile is the direct truth table for the predicate itself
// (bead pg2-gzrn2 added the third case, pg2-thkh0.5 the fourth): a real session
// record ends in .json or .jsonl but is NEITHER a rate_limits sibling NOR a
// session-mode sibling NOR a compaction marker.
func TestIsStatusSiblingFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"abc-123.status.jsonl", true},
		{"abc-123.status.last", true},
		{"abc-123.session-mode.json", true},
		{"abc-123.compaction.json", true}, // compaction marker (bead pg2-thkh0.5)
		{"abc-123.json", false},           // genuine session record
		{"abc-123.jsonl", false},          // genuine transcript
		{"", false},
	}
	for _, c := range cases {
		if got := IsStatusSiblingFile(c.name); got != c.want {
			t.Errorf("IsStatusSiblingFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
