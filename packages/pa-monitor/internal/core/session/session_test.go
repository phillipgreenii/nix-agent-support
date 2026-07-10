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
		{Blocked, "blocked"},
		{Idle, "idle"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("Status(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestBlockerString(t *testing.T) {
	cases := []struct {
		b    Blocker
		want string
	}{
		{NoBlocker, ""},
		{HumanInput, "human_input"},
		{HumanAuthn, "human_authn"},
		{UsageLimit, "usage_limit"},
		{ErrorBlocker, "error"},
	}
	for _, c := range cases {
		if got := c.b.String(); got != c.want {
			t.Errorf("Blocker(%d).String() = %q, want %q", c.b, got, c.want)
		}
		if got := ParseBlocker(c.want); c.b != NoBlocker && got != c.b {
			t.Errorf("ParseBlocker(%q) = %v, want %v", c.want, got, c.b)
		}
	}
}

func TestAwaitsHumanAndKeepAwake(t *testing.T) {
	cases := []struct {
		blocker         Blocker
		retryable       bool
		wantAwaitsHuman bool
	}{
		{HumanInput, false, true},
		{HumanAuthn, false, true},
		{UsageLimit, false, false},
		{ErrorBlocker, true, false},
		{ErrorBlocker, false, true},
		{NoBlocker, false, false},
	}
	for _, c := range cases {
		if got := AwaitsHuman(c.blocker, c.retryable); got != c.wantAwaitsHuman {
			t.Errorf("AwaitsHuman(%v,%v) = %v, want %v", c.blocker, c.retryable, got, c.wantAwaitsHuman)
		}
		// KeepAwake for a Blocked session is the inverse of AwaitsHuman.
		if got := KeepAwake(Blocked, c.blocker, c.retryable); got == c.wantAwaitsHuman {
			t.Errorf("KeepAwake(Blocked,%v,%v) = %v, want %v", c.blocker, c.retryable, got, !c.wantAwaitsHuman)
		}
	}
	if !KeepAwake(Working, NoBlocker, false) {
		t.Error("KeepAwake(Working) = false, want true")
	}
	if KeepAwake(Idle, NoBlocker, false) {
		t.Error("KeepAwake(Idle) = true, want false")
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
	// Dormancy is no longer a Classify bucket (ADR 0024): a very old mtime is
	// still Idle. Age is surfaced separately via IsLongIdle.
	if got := Classify(now, now.Add(-2*time.Hour), working, idle); got != Idle {
		t.Errorf("2h ago classified as %v, want Idle", got)
	}
	if !IsLongIdle(now, now.Add(-2*time.Hour), idle) {
		t.Error("IsLongIdle(2h ago, 1h) = false, want true")
	}
	if IsLongIdle(now, now.Add(-30*time.Second), idle) {
		t.Error("IsLongIdle(30s ago, 1h) = true, want false")
	}
}
