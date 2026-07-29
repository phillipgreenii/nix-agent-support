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
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected error kind 'unknown' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "sid-1") {
		t.Errorf("expected session label 'sid-1' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "ERROR") {
		t.Errorf("expected ERROR header in tabular output, got:\n%s", out)
	}
	if !strings.Contains(out, "TERM") {
		t.Errorf("expected TERM header in tabular output, got:\n%s", out)
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
	if !strings.Contains(out, "[disrupted,manual]") {
		t.Errorf("expected nudge column '[disrupted,manual]' in output, got:\n%s", out)
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

func authDetail(sid string) *pb.SessionDetail {
	return &pb.SessionDetail{
		View:      &pb.SessionView{SessionId: sid, Status: "idle"},
		LastError: &pb.ApiError{Kind: "authentication_failed", IsTerminal: true, Text: "Please run /login · API Error: 401 Invalid authentication credentials"},
	}
}

func TestFormatAuthFailureBanner(t *testing.T) {
	if out := formatAuthFailureBanner(nil); out != "" {
		t.Errorf("no sessions: want empty, got %q", out)
	}
	one := formatAuthFailureBanner([]*pb.SessionDetail{authDetail("a")})
	if !strings.Contains(one, "⊘") || !strings.Contains(one, "/login") || !strings.Contains(one, "(1 session)") {
		t.Errorf("one auth failure banner wrong: %q", one)
	}
	two := formatAuthFailureBanner([]*pb.SessionDetail{authDetail("a"), authDetail("b")})
	if !strings.Contains(two, "(2 sessions)") {
		t.Errorf("two auth failures plural wrong: %q", two)
	}
}

func TestFormatStatusSessionsAuthColumn(t *testing.T) {
	out := formatStatusSessions([]*pb.SessionDetail{authDetail("sid-1")})
	if !strings.Contains(out, "auth") {
		t.Errorf("expected compact 'auth' in ERROR column, got:\n%s", out)
	}
	if strings.Contains(out, "authentication_failed") {
		t.Errorf("expected compact 'auth', not raw kind, got:\n%s", out)
	}
	if strings.Contains(out, "/login") {
		t.Errorf("status table column should not contain /login hint, got:\n%s", out)
	}
}

func TestFormatSessionInfoAuthHint(t *testing.T) {
	out := formatSessionInfo(authDetail("sid-1"))
	if !strings.Contains(out, "authentication_failed — run /login") {
		t.Errorf("expected run /login hint on last_error line, got:\n%s", out)
	}
}

// --- formatUsageLine tests (ADR 0021 §5 / ADR 0024 D3) ---

// TestFormatUsageLineAuthoritative proves the status line shows the
// AUTHORITATIVE used_percentage (verbatim, already [0,100]) while keeping the
// native cost/cap dollars — NOT the misleading cost/cap ratio.
func TestFormatUsageLineAuthoritative(t *testing.T) {
	pct := 436.0 // authoritative 5h reading well over the cost/cap ratio
	got := formatUsageLine("block", "2026-06-01T10Z", 100.0, 90.0, &pct)
	want := "block 2026-06-01T10Z:  cost $100.00 / cap $90.00 (436.0%)\n"
	if got != want {
		t.Errorf("block line:\n got  %q\n want %q", got, want)
	}
}

// TestFormatUsageLineUnknownOmitsPct proves an unknown (nil) authoritative
// reading omits the percentage and shows cost-only — never the cost ratio.
func TestFormatUsageLineUnknownOmitsPct(t *testing.T) {
	got := formatUsageLine("week ", "2026-W21", 50.0, 200.0, nil)
	want := "week  2026-W21:  cost $50.00 / cap $200.00\n"
	if got != want {
		t.Errorf("week line (unknown pct):\n got  %q\n want %q", got, want)
	}
	if strings.Contains(got, "%") {
		t.Errorf("unknown authoritative reading must omit the percentage, got %q", got)
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

// --- ADR 0024 {working, blocked, idle} rollup on `info path:` (bead pg2-vsrxf) ---

// TestFormatPathRollupReportsBlockedSessions pins the bug in bead pg2-vsrxf:
// `info path:` printed "%d working, %d idle, %d dormant" from
// WorkingN/IdleN/DormantN. The DB-path bucketer increments ONLY BlockedN for a
// blocked session, and dormant_n is retired (permanently 0), so a directory
// whose three sessions were all blocked on a usage limit rendered
// "0 working, 0 idle, 0 dormant" — the sessions vanished from the rollup.
func TestFormatPathRollupReportsBlockedSessions(t *testing.T) {
	d := &pb.Directory{
		Path:         "/w/repo",
		Branch:       "main",
		BlockedN:     3,
		TotalTokens:  1234,
		TotalCostUsd: 1.5,
	}
	got := formatPathRollup(d)
	if want := "sessions: 0 working, 3 blocked, 0 idle\n"; !strings.Contains(got, want) {
		t.Errorf("blocked sessions lost from the rollup:\ngot:\n%s\nwant line: %q", got, want)
	}
	if strings.Contains(got, "dormant") {
		t.Errorf("`info path:` must not print the retired, always-zero dormant count:\n%s", got)
	}
	// The rest of the block must survive the refactor.
	for _, want := range []string{"path:     /w/repo\n", "branch:   main\n", "tokens:   1234\n", "cost:     $1.50\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestFormatPathRollupFoldsDormantIntoIdle covers version skew (ADR 0024 R8): an
// older daemon still sends dormant_n, and those sessions are plain idle under
// the new model, so dormant_n must be ADDED to idle — never reported on its own.
func TestFormatPathRollupFoldsDormantIntoIdle(t *testing.T) {
	d := &pb.Directory{WorkingN: 1, BlockedN: 2, IdleN: 3, DormantN: 4}
	got := formatPathRollup(d)
	if want := "sessions: 1 working, 2 blocked, 7 idle\n"; !strings.Contains(got, want) {
		t.Errorf("legacy dormant_n not folded into idle:\ngot:\n%s\nwant line: %q", got, want)
	}
}

// TestFormatPathRollupNilDirectory guards the defensive nil path.
func TestFormatPathRollupNilDirectory(t *testing.T) {
	if got := formatPathRollup(nil); got != "" {
		t.Errorf("nil directory: got %q, want empty", got)
	}
}

// TestDirSessionCounts locks the fold rule at the helper both `status` and
// `info path:` share, including the nil case.
func TestDirSessionCounts(t *testing.T) {
	w, b, i := dirSessionCounts(&pb.Directory{WorkingN: 2, BlockedN: 3, IdleN: 4, DormantN: 5})
	if w != 2 || b != 3 || i != 9 {
		t.Errorf("counts = (%d, %d, %d), want (2, 3, 9) — idle must absorb dormant_n", w, b, i)
	}
	if w, b, i := dirSessionCounts(nil); w != 0 || b != 0 || i != 0 {
		t.Errorf("nil directory counts = (%d, %d, %d), want all zero", w, b, i)
	}
}

// --- ADR 0024 status/blocker form on `info session:` (bead pg2-vsrxf) ---

// TestFormatStatusWithBlocker pins ADR 0024 D1: a blocked session is qualified
// with its blocker; every other status is bare (blocker is present ONLY when
// status == "blocked", with no `none` sentinel).
func TestFormatStatusWithBlocker(t *testing.T) {
	for _, tc := range []struct {
		name, status, blocker, want string
	}{
		{"usage limit", "blocked", "usage_limit", "blocked/usage_limit"},
		{"human input", "blocked", "human_input", "blocked/human_input"},
		{"human authn", "blocked", "human_authn", "blocked/human_authn"},
		{"error", "blocked", "error", "blocked/error"},
		{"working is bare", "working", "", "working"},
		{"idle is bare", "idle", "", "idle"},
		{"no status", "", "", ""},
	} {
		v := &pb.SessionView{Status: tc.status, Blocker: tc.blocker}
		if got := formatStatusWithBlocker(v); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := formatStatusWithBlocker(nil); got != "" {
		t.Errorf("nil view: got %q, want empty", got)
	}
}

// TestInfoSessionPrintsBlockerInStatusSubcommandForm pins the second half of
// bead pg2-vsrxf: `info session:` printed `status:` bare, so a session blocked
// on a usage limit read just "blocked" with no reason — whereas the `status`
// subcommand's table already qualified it. Both surfaces must render the SAME
// status/blocker token for the same SessionView.
func TestInfoSessionPrintsBlockerInStatusSubcommandForm(t *testing.T) {
	v := &pb.SessionView{SessionId: "sid-1", Status: "blocked", Blocker: "usage_limit"}
	sd := &pb.SessionDetail{
		View:      v,
		LastError: &pb.ApiError{Kind: "rate_limit", IsTerminal: true},
	}

	header := formatSessionInfoHeader(v)
	if want := "status:         blocked/usage_limit\n"; !strings.Contains(header, want) {
		t.Errorf("`info session:` dropped the blocker:\ngot:\n%s\nwant line: %q", header, want)
	}

	// Same token in the `status` subcommand's table — the shared form.
	table := formatStatusSessions([]*pb.SessionDetail{sd})
	if !strings.Contains(table, "blocked/usage_limit") {
		t.Errorf("`status` table lost the shared status/blocker form:\n%s", table)
	}
}

// TestFormatSessionInfoHeaderNonBlockedStaysBare confirms the header keeps the
// bare status for non-blocked sessions and still renders the other fields.
func TestFormatSessionInfoHeaderNonBlockedStaysBare(t *testing.T) {
	v := &pb.SessionView{
		SessionId:     "sid-2",
		Status:        "working",
		Model:         "opus",
		Cwd:           "/w/repo",
		Branch:        "main",
		ContextTokens: 42,
		Labels:        map[string]string{"workspace.scope": "personal"},
	}
	got := formatSessionInfoHeader(v)
	for _, want := range []string{
		"session_id:     sid-2\n",
		"status:         working\n",
		"model:          opus\n",
		"cwd:            /w/repo\n",
		"branch:         main\n",
		"scope:          personal\n",
		"context_tokens: 42\n",
		"subagents:      0\n",
		"subshells:      0\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if got := formatSessionInfoHeader(nil); got != "" {
		t.Errorf("nil view: got %q, want empty", got)
	}
}

// TestFormatSessionInfoHeaderOmitsScopeWhenUnset keeps the pg2-4xbrm behavior:
// an unlabeled session has no empty scope line.
func TestFormatSessionInfoHeaderOmitsScopeWhenUnset(t *testing.T) {
	got := formatSessionInfoHeader(&pb.SessionView{SessionId: "sid-3", Status: "idle"})
	if strings.Contains(got, "scope:") {
		t.Errorf("unlabeled session must not get a scope line:\n%s", got)
	}
}
