package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestResolveTranscriptIgnoresStatusFile proves a frequently-rewritten
// <id>.status.jsonl is NEVER selected as the transcript, even when it is the
// newest file by mtime (the load-bearing failure mode from ADR 0021 §2).
func TestResolveTranscriptIgnoresStatusFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-Users-me-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The real transcript is older; the status file is the newest by mtime.
	tx := writeTranscript(t, dir, "sess-1.jsonl", "", time.Now().Add(-1*time.Hour))
	statusPath := filepath.Join(dir, "sess-1.status.jsonl")
	if err := os.WriteFile(statusPath, []byte(`{"ts":1700000000,"five_hour_pct":34}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(statusPath, now, now); err != nil {
		t.Fatal(err)
	}

	s := &Session{Cwd: "/Users/me/proj", SessionID: "sess-1"}
	path, _, ok := ResolveTranscript(home, s)
	if !ok {
		t.Fatal("ResolveTranscript ok=false, want true (real transcript present)")
	}
	if path != tx {
		t.Errorf("ResolveTranscript picked %q, want the real transcript %q (never the status file)", path, tx)
	}
}

// TestResolveTranscriptReturnsFalseWhenOnlyStatusFilePresent guards the case where
// a directory holds ONLY a status file: it must resolve to nothing, not select the
// status file.
func TestResolveTranscriptReturnsFalseWhenOnlyStatusFilePresent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-Users-me-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	statusPath := filepath.Join(dir, "sess-1.status.jsonl")
	if err := os.WriteFile(statusPath, []byte(`{"ts":1700000000}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Session{Cwd: "/Users/me/proj", SessionID: "sess-1"}
	if _, _, ok := ResolveTranscript(home, s); ok {
		t.Error("ResolveTranscript ok=true, want false (only a status file present)")
	}
}
