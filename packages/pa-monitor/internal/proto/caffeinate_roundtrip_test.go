package proto

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestDaemonStateCaffeinateRoundTrip confirms the two caffeinate indicators
// (MODE, PROCESS), the grace-remaining seconds, and the cause survive a
// protobuf marshal/unmarshal. The incident case is MODE on + PROCESS off; the
// round-trip must preserve that exact, distinct combination.
func TestDaemonStateCaffeinateRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		process CaffeinateProcess
		mode    bool
		graceS  uint32
		cause   string
	}{
		{"incident_mode_on_process_off", CaffeinateProcess_CAFFEINATE_PROCESS_OFF, true, 0, "manual"},
		{"holding", CaffeinateProcess_CAFFEINATE_PROCESS_ON, true, 0, "agents_active"},
		{"grace_with_secs", CaffeinateProcess_CAFFEINATE_PROCESS_GRACE, true, 55, "agents_active"},
		{"error", CaffeinateProcess_CAFFEINATE_PROCESS_ERROR, true, 0, "manual"},
		{"mode_off", CaffeinateProcess_CAFFEINATE_PROCESS_OFF, false, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &DaemonState{
				CaffeinateActive:          tc.mode || tc.process != CaffeinateProcess_CAFFEINATE_PROCESS_OFF,
				CaffeinateMode:            tc.mode,
				CaffeinateProcess:         tc.process,
				CaffeinateGraceRemainingS: tc.graceS,
				CaffeinateCause:           tc.cause,
			}
			b, err := proto.Marshal(in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var out DaemonState
			if err := proto.Unmarshal(b, &out); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if out.GetCaffeinateMode() != tc.mode {
				t.Errorf("mode = %v, want %v", out.GetCaffeinateMode(), tc.mode)
			}
			if out.GetCaffeinateProcess() != tc.process {
				t.Errorf("process = %v, want %v", out.GetCaffeinateProcess(), tc.process)
			}
			if out.GetCaffeinateGraceRemainingS() != tc.graceS {
				t.Errorf("graceS = %d, want %d", out.GetCaffeinateGraceRemainingS(), tc.graceS)
			}
			if out.GetCaffeinateCause() != tc.cause {
				t.Errorf("cause = %q, want %q", out.GetCaffeinateCause(), tc.cause)
			}
		})
	}
}

// TestCaffeinateResponseRoundTrip confirms the augmented CaffeinateResponse —
// the new mode/process/grace fields plus the previously-unpopulated `until` —
// survive a protobuf round-trip.
func TestCaffeinateResponseRoundTrip(t *testing.T) {
	until := timestamppb.Now()
	in := &CaffeinateResponse{
		Active:          true,
		Cause:           "agents_active",
		Until:           until,
		Mode:            true,
		Process:         CaffeinateProcess_CAFFEINATE_PROCESS_GRACE,
		GraceRemainingS: 30,
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out CaffeinateResponse
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !out.GetMode() || out.GetProcess() != CaffeinateProcess_CAFFEINATE_PROCESS_GRACE {
		t.Errorf("mode/process = %v/%v, want true/GRACE", out.GetMode(), out.GetProcess())
	}
	if out.GetGraceRemainingS() != 30 {
		t.Errorf("graceRemainingS = %d, want 30", out.GetGraceRemainingS())
	}
	if out.GetUntil() == nil {
		t.Fatal("until not preserved; want non-nil")
	}
	if !out.GetUntil().AsTime().Equal(until.AsTime()) {
		t.Errorf("until = %v, want %v", out.GetUntil().AsTime(), until.AsTime())
	}
}
