package main

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// --- formatStatusSessions tests ---

// TestFormatStatusSessionsNoAnnotations verifies that sessions with no
// terminal errors and no pending nudges produce no output (avoids adding
// unnecessary columns to the table).
func TestFormatStatusSessionsNoAnnotations(t *testing.T) {
	sessions := []*pb.SessionDetail{
		{
			View: &pb.SessionView{SessionId: "sid-1", Status: "idle"},
		},
		{
			View: &pb.SessionView{SessionId: "sid-2", Status: "working"},
		},
	}
	out := formatStatusSessions(sessions)
	if out != "" {
		t.Errorf("expected empty output for sessions with no errors/nudges, got:\n%s", out)
	}
}

// TestFormatStatusSessionsTerminalErrorAnnotation verifies that a session with
// LastError.IsTerminal=true and Kind=unknown is rendered with the error kind
// annotation in the output.
func TestFormatStatusSessionsTerminalErrorAnnotation(t *testing.T) {
	sessions := []*pb.SessionDetail{
		{
			View:      &pb.SessionView{SessionId: "sid-1", Status: "idle"},
			LastError: &pb.ApiError{Kind: "unknown", IsTerminal: true, IsRetryable: true},
		},
	}
	out := formatStatusSessions(sessions)
	if out == "" {
		t.Fatal("expected non-empty output for session with terminal error, got empty")
	}
	if !strings.Contains(out, "error:unknown") {
		t.Errorf("expected 'error:unknown' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "sid-1") {
		t.Errorf("expected session id 'sid-1' in output, got:\n%s", out)
	}
}

// TestFormatStatusSessionsNudgeAnnotation verifies that pending nudge sources
// are shown in the status annotation.
func TestFormatStatusSessionsNudgeAnnotation(t *testing.T) {
	sessions := []*pb.SessionDetail{
		{
			View:         &pb.SessionView{SessionId: "sid-3", Status: "idle"},
			PendingNudge: &pb.PendingNudge{Sources: []string{"disrupted", "manual"}},
		},
	}
	out := formatStatusSessions(sessions)
	if out == "" {
		t.Fatal("expected non-empty output for session with pending nudge, got empty")
	}
	if !strings.Contains(out, "nudge:[disrupted,manual]") {
		t.Errorf("expected nudge annotation in output, got:\n%s", out)
	}
}

// TestFormatStatusSessionsNoTerminalErrorSkipped verifies that a non-terminal
// error does not trigger the annotation (keeps the table clean).
func TestFormatStatusSessionsNoTerminalErrorSkipped(t *testing.T) {
	sessions := []*pb.SessionDetail{
		{
			View:      &pb.SessionView{SessionId: "sid-4", Status: "idle"},
			LastError: &pb.ApiError{Kind: "rate_limit", IsTerminal: false},
		},
	}
	out := formatStatusSessions(sessions)
	if out != "" {
		t.Errorf("expected empty output for non-terminal error, got:\n%s", out)
	}
}

// --- formatSessionInfo tests ---

// TestFormatSessionInfoPlainNoLastError verifies that a plain session without
// a LastError produces no last_error section.
func TestFormatSessionInfoPlainNoLastError(t *testing.T) {
	sd := &pb.SessionDetail{
		View: &pb.SessionView{SessionId: "sid-1", Status: "idle"},
	}
	out := formatSessionInfo(sd)
	if strings.Contains(out, "last_error") {
		t.Errorf("expected no last_error section for plain session, got:\n%s", out)
	}
}

// TestFormatSessionInfoTerminalErrorShown verifies that a session with
// LastError.IsTerminal=true renders the last_error section.
func TestFormatSessionInfoTerminalErrorShown(t *testing.T) {
	errAt := time.Now().Add(-90 * time.Second)
	sd := &pb.SessionDetail{
		View: &pb.SessionView{SessionId: "sid-2", Status: "idle"},
		LastError: &pb.ApiError{
			Kind:        "server_error",
			Text:        "API Error: 529 Overloaded.",
			At:          timestamppb.New(errAt),
			IsTerminal:  true,
			IsRetryable: true,
		},
	}
	out := formatSessionInfo(sd)
	if !strings.Contains(out, "last_error:") {
		t.Errorf("expected last_error section, got:\n%s", out)
	}
	if !strings.Contains(out, "server_error") {
		t.Errorf("expected 'server_error' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "529 Overloaded") {
		t.Errorf("expected error text in output, got:\n%s", out)
	}
	// Verify no spurious (escalated) suffix when IsRetryable=true.
	if strings.Contains(out, "escalated") {
		t.Errorf("unexpected (escalated) suffix when IsRetryable=true, got:\n%s", out)
	}
}

// TestFormatSessionInfoEscalatedSuffix verifies that a session with
// Kind=server_error and IsRetryable=false (escalated by the daemon)
// shows the (escalated) suffix.
func TestFormatSessionInfoEscalatedSuffix(t *testing.T) {
	errAt := time.Now().Add(-5 * time.Minute)
	sd := &pb.SessionDetail{
		View: &pb.SessionView{SessionId: "sid-3", Status: "idle"},
		LastError: &pb.ApiError{
			Kind:        "server_error",
			Text:        "upstream 500",
			At:          timestamppb.New(errAt),
			IsTerminal:  true,
			IsRetryable: false, // daemon escalated: kind is retryable but flag flipped
		},
	}
	out := formatSessionInfo(sd)
	if !strings.Contains(out, "last_error:") {
		t.Errorf("expected last_error section, got:\n%s", out)
	}
	if !strings.Contains(out, "(escalated)") {
		t.Errorf("expected '(escalated)' suffix for escalated error, got:\n%s", out)
	}
}

// TestFormatSessionInfoPendingNudgeShown verifies that pending_nudge sources
// are shown.
func TestFormatSessionInfoPendingNudgeShown(t *testing.T) {
	sd := &pb.SessionDetail{
		View:         &pb.SessionView{SessionId: "sid-4", Status: "idle"},
		PendingNudge: &pb.PendingNudge{Sources: []string{"disrupted", "manual"}},
	}
	out := formatSessionInfo(sd)
	if !strings.Contains(out, "pending_nudge:") {
		t.Errorf("expected pending_nudge section, got:\n%s", out)
	}
	if !strings.Contains(out, "disrupted") {
		t.Errorf("expected 'disrupted' source in output, got:\n%s", out)
	}
	if !strings.Contains(out, "manual") {
		t.Errorf("expected 'manual' source in output, got:\n%s", out)
	}
}

// TestFormatSessionInfoNonTerminalHidden verifies that a non-terminal error
// does not produce a last_error section.
func TestFormatSessionInfoNonTerminalHidden(t *testing.T) {
	sd := &pb.SessionDetail{
		View: &pb.SessionView{SessionId: "sid-5", Status: "idle"},
		LastError: &pb.ApiError{
			Kind:       "rate_limit",
			IsTerminal: false,
		},
	}
	out := formatSessionInfo(sd)
	if strings.Contains(out, "last_error") {
		t.Errorf("expected no last_error section for non-terminal error, got:\n%s", out)
	}
}

// --- apiErrorIsEscalated tests ---

func TestAPIErrorIsEscalatedUnknownFlipped(t *testing.T) {
	e := &pb.ApiError{Kind: "unknown", IsRetryable: false}
	if !apiErrorIsEscalated(e) {
		t.Error("expected escalated=true for kind=unknown, IsRetryable=false")
	}
}

func TestAPIErrorIsEscalatedServerErrorFlipped(t *testing.T) {
	e := &pb.ApiError{Kind: "server_error", IsRetryable: false}
	if !apiErrorIsEscalated(e) {
		t.Error("expected escalated=true for kind=server_error, IsRetryable=false")
	}
}

func TestAPIErrorIsEscalatedRetryableNotEscalated(t *testing.T) {
	e := &pb.ApiError{Kind: "server_error", IsRetryable: true}
	if apiErrorIsEscalated(e) {
		t.Error("expected escalated=false when IsRetryable=true")
	}
}

func TestAPIErrorIsEscalatedRateLimitNotEscalated(t *testing.T) {
	e := &pb.ApiError{Kind: "rate_limit", IsRetryable: false}
	if apiErrorIsEscalated(e) {
		t.Error("expected escalated=false for rate_limit kind (not inherently retryable)")
	}
}
