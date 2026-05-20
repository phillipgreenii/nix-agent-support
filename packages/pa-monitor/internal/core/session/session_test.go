package session

import (
	"testing"
	"time"
)

func TestStatusString(t *testing.T) {
	cases := []struct {
		s    Status
		want string
	}{
		{Working, "working"},
		{Idle, "idle"},
		{Dormant, "dormant"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("Status(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestSessionShortLabelPrefersName(t *testing.T) {
	s := &Session{Name: "my-project-9306", SessionID: "b0b9c488-aaaa-bbbb-cccc-ddddddddeeee"}
	if got := s.Label(false); got != "my-project-9306" {
		t.Errorf("Label(false) = %q, want name", got)
	}
	if got := s.Label(true); got != "b0b9c488-aaaa-bbbb-cccc-ddddddddeeee" {
		t.Errorf("Label(true) = %q, want full id", got)
	}
}

func TestSessionShortLabelFallsBackToShortID(t *testing.T) {
	s := &Session{Name: "", SessionID: "b0b9c488-aaaa-bbbb-cccc-ddddddddeeee"}
	if got := s.Label(false); got != "b0b9c488" {
		t.Errorf("Label(false) = %q, want first 8 of id", got)
	}
}

func TestTranscriptPath(t *testing.T) {
	s := &Session{Cwd: "/Users/phil/proj", SessionID: "abc-123"}
	got := s.TranscriptPath("/home/.claude")
	want := "/home/.claude/projects/-Users-phil-proj/abc-123.jsonl"
	if got != want {
		t.Errorf("TranscriptPath = %q, want %q", got, want)
	}
}

func TestTranscriptPathSlugReplacesUnderscores(t *testing.T) {
	cases := []struct {
		cwd, want string
	}{
		{"/Users/test_user/my_workspace", "/home/.claude/projects/-Users-test-user-my-workspace/id.jsonl"},
		{"/a/b_c_d", "/home/.claude/projects/-a-b-c-d/id.jsonl"},
		{"/only/slashes", "/home/.claude/projects/-only-slashes/id.jsonl"},
		{"/both_under/dash-already", "/home/.claude/projects/-both-under-dash-already/id.jsonl"},
	}
	for _, c := range cases {
		s := &Session{Cwd: c.cwd, SessionID: "id"}
		if got := s.TranscriptPath("/home/.claude"); got != c.want {
			t.Errorf("TranscriptPath(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

func TestClassifyLiveness(t *testing.T) {
	now := time.Now()
	working := 10 * time.Second
	idle := 1 * time.Hour
	if got := Classify(now, now.Add(-5*time.Second), working, idle); got != Working {
		t.Errorf("5s ago classified as %v, want Working", got)
	}
	if got := Classify(now, now.Add(-30*time.Second), working, idle); got != Idle {
		t.Errorf("30s ago classified as %v, want Idle", got)
	}
	if got := Classify(now, now.Add(-2*time.Hour), working, idle); got != Dormant {
		t.Errorf("2h ago classified as %v, want Dormant", got)
	}
}
