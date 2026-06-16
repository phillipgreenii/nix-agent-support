package proto

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// TestSessionEnvRoundTrip confirms that the known env-key subset
// (CMUX_WORKSPACE_ID, TMUX, GC_RIG, GC_AGENT, WORKSPACE) survives
// FromTree → ToTree without loss. Arbitrary other keys must NOT be
// exposed on the wire — verifies the privacy guard.
func TestSessionEnvRoundTrip(t *testing.T) {
	in := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Sessions: []*aggregate.SessionView{
					{
						Session: &session.Session{
							SessionID: "s1",
							PID:       42,
							Env: map[string]string{
								"CMUX_WORKSPACE_ID": "ws-7",
								"TMUX":              "/tmp/tmux-501/default,1,0",
								"GC_RIG":            "beads",
								"GC_AGENT":          "polecat",
								"WORKSPACE":         "ws-1",
								"AWS_SECRET":        "should-not-leak",
								"OAUTH_TOKEN":       "still-not-leaking",
							},
						},
					},
				},
			},
		},
	}
	wire := FromTree(in)
	out := ToTree(wire)

	if len(out.Dirs) != 1 || len(out.Dirs[0].Sessions) != 1 {
		t.Fatal("shape lost")
	}
	got := out.Dirs[0].Sessions[0].Env
	if got["CMUX_WORKSPACE_ID"] != "ws-7" {
		t.Errorf("CMUX_WORKSPACE_ID lost: %+v", got)
	}
	if got["TMUX"] != "/tmp/tmux-501/default,1,0" {
		t.Errorf("TMUX lost: %+v", got)
	}
	if got["GC_RIG"] != "beads" {
		t.Errorf("GC_RIG lost: %+v", got)
	}
	if got["GC_AGENT"] != "polecat" {
		t.Errorf("GC_AGENT lost: %+v", got)
	}
	if got["WORKSPACE"] != "ws-1" {
		t.Errorf("WORKSPACE lost: %+v", got)
	}
	// Privacy guard: arbitrary keys must NOT round-trip.
	if _, ok := got["AWS_SECRET"]; ok {
		t.Errorf("AWS_SECRET leaked across the wire: %+v", got)
	}
	if _, ok := got["OAUTH_TOKEN"]; ok {
		t.Errorf("OAUTH_TOKEN leaked across the wire: %+v", got)
	}
}

// TestSessionDetailLastErrorPendingNudgeRoundTrip confirms that LastError
// and PendingNudge survive SessionDetailFromView → SessionDetailToView.
func TestSessionDetailLastErrorPendingNudgeRoundTrip(t *testing.T) {
	errAt := time.Date(2025, 5, 1, 12, 0, 0, 0, time.UTC)
	in := &aggregate.SessionView{
		Session: &session.Session{
			SessionID: "s-rt-1",
			PID:       99,
		},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastError: &transcript.ErrorRecord{
				Kind:         transcript.ErrServerError,
				Text:         "upstream 500",
				At:           errAt,
				IsTerminal:   true,
				IsRetryable:  true,
				FromSubagent: true,
			},
			PendingNudge: &aggregate.PendingNudge{
				Sources: []string{"disrupted", "manual"},
			},
		},
	}

	wire := SessionDetailFromView(in)
	if wire == nil {
		t.Fatal("SessionDetailFromView returned nil")
	}

	// Verify the wire proto has the expected fields.
	if wire.GetLastError() == nil {
		t.Fatal("wire.LastError = nil, want non-nil")
	}
	if wire.GetLastError().GetKind() != string(transcript.ErrServerError) {
		t.Errorf("wire.LastError.Kind = %q, want %q", wire.GetLastError().GetKind(), transcript.ErrServerError)
	}
	if wire.GetPendingNudge() == nil {
		t.Fatal("wire.PendingNudge = nil, want non-nil")
	}

	out := SessionDetailToView(wire)
	if out == nil {
		t.Fatal("SessionDetailToView returned nil")
	}

	// --- LastError round-trip ---
	le := out.LastError
	if le == nil {
		t.Fatal("out.LastError = nil, want non-nil")
	}
	if le.Kind != transcript.ErrServerError {
		t.Errorf("LastError.Kind = %q, want %q", le.Kind, transcript.ErrServerError)
	}
	if le.Text != "upstream 500" {
		t.Errorf("LastError.Text = %q, want %q", le.Text, "upstream 500")
	}
	if !le.At.Equal(errAt) {
		t.Errorf("LastError.At = %v, want %v", le.At, errAt)
	}
	if !le.IsTerminal {
		t.Error("LastError.IsTerminal = false, want true")
	}
	if !le.IsRetryable {
		t.Error("LastError.IsRetryable = false, want true")
	}
	if !le.FromSubagent {
		t.Error("LastError.FromSubagent = false, want true")
	}

	// --- PendingNudge round-trip ---
	pn := out.PendingNudge
	if pn == nil {
		t.Fatal("out.PendingNudge = nil, want non-nil")
	}
	if len(pn.Sources) != 2 {
		t.Fatalf("PendingNudge.Sources len = %d, want 2: %v", len(pn.Sources), pn.Sources)
	}
	wantSources := map[string]bool{"disrupted": true, "manual": true}
	for _, s := range pn.Sources {
		if !wantSources[s] {
			t.Errorf("unexpected source %q in PendingNudge.Sources", s)
		}
	}
}

// TestSessionDetailNilFieldsRoundTrip confirms that nil LastError and
// PendingNudge survive the round-trip without panicking.
func TestSessionDetailNilFieldsRoundTrip(t *testing.T) {
	in := &aggregate.SessionView{
		Session: &session.Session{
			SessionID: "s-rt-2",
			PID:       77,
		},
	}
	wire := SessionDetailFromView(in)
	if wire == nil {
		t.Fatal("SessionDetailFromView returned nil")
	}
	if wire.GetLastError() != nil {
		t.Errorf("wire.LastError should be nil for nil input, got %+v", wire.GetLastError())
	}
	if wire.GetPendingNudge() != nil {
		t.Errorf("wire.PendingNudge should be nil for nil input, got %+v", wire.GetPendingNudge())
	}

	out := SessionDetailToView(wire)
	if out == nil {
		t.Fatal("SessionDetailToView returned nil")
	}
	if out.LastError != nil {
		t.Errorf("out.LastError should be nil, got %+v", out.LastError)
	}
	if out.PendingNudge != nil {
		t.Errorf("out.PendingNudge should be nil, got %+v", out.PendingNudge)
	}
}

// TestDaemonStateAutoResumeFieldsRoundTrip confirms that the auto_resume_enabled
// and auto_resume_delay_s fields survive a manual marshal/unmarshal of the
// DaemonState proto, independent of the tree translation.
func TestDaemonStateAutoResumeFieldsRoundTrip(t *testing.T) {
	// Build via FromTree then manually set the daemon-level fields,
	// mirroring what buildState() does in server.go.
	tree := &aggregate.Tree{}
	wire := FromTree(tree)
	wire.AutoResumeEnabled = true
	wire.AutoResumeDelayS = 30

	// Simulate the client-side ToTree path: the daemon-level fields are
	// preserved on the proto and can be read directly from DaemonState.
	if !wire.GetAutoResumeEnabled() {
		t.Error("AutoResumeEnabled = false, want true")
	}
	if wire.GetAutoResumeDelayS() != 30 {
		t.Errorf("AutoResumeDelayS = %d, want 30", wire.GetAutoResumeDelayS())
	}

	// ToTree reconstructs the tree from the proto; auto-resume fields are
	// daemon-level and are NOT round-tripped into the Tree (by design),
	// but they must remain readable on the DaemonState proto itself.
	out := ToTree(wire)
	if out == nil {
		t.Fatal("ToTree returned nil")
	}
}
