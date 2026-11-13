package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/phillipgreenii/x/exitcode"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestFanOutOutcome_ExitCode_AllSucceeded(t *testing.T) {
	o := FanOutOutcome{Sources: []SourceResult{
		{Source: "a", Status: SourceSucceeded},
		{Source: "b", Status: SourceSucceeded},
	}}
	if code := o.ExitCode(); code != 0 {
		t.Fatalf("ExitCode = %d, want 0", code)
	}
}

func TestFanOutOutcome_ExitCode_Degraded(t *testing.T) {
	o := FanOutOutcome{Sources: []SourceResult{
		{Source: "a", Status: SourceSucceeded},
		{Source: "b", Status: SourceDegraded},
	}}
	if code := o.ExitCode(); code != 5 {
		t.Fatalf("ExitCode = %d, want 5", code)
	}
}

func TestFanOutOutcome_ExitCode_Disabled_CountsAsHealthy(t *testing.T) {
	// Per INV-EXIT-2, a no-credential backend answering "disabled" is a
	// well-formed negative, not a degraded state — it must not hold the
	// exit code at 2 forever on an otherwise fully healthy machine
	// [bug A2].
	o := FanOutOutcome{Sources: []SourceResult{
		{Source: "a", Status: SourceSucceeded},
		{Source: "b", Status: SourceDisabled},
	}}
	if code := o.ExitCode(); code != 0 {
		t.Fatalf("ExitCode = %d, want 0", code)
	}
}

func TestFanOutOutcome_ExitCode_AllDisabled_Healthy(t *testing.T) {
	// The exact regression this bug caused: every registered backend is a
	// correctly-configured no-credential backend (e.g. scm-git,
	// issue-beads answering auth_status), so the whole fan-out must be
	// exit 0, not a standing partial outage [bug A2].
	o := FanOutOutcome{Sources: []SourceResult{
		{Source: "a", Status: SourceDisabled},
		{Source: "b", Status: SourceDisabled},
	}}
	if code := o.ExitCode(); code != 0 {
		t.Fatalf("ExitCode = %d, want 0", code)
	}
}

func TestFanOutOutcome_ExitCode_TotalFailure(t *testing.T) {
	o := FanOutOutcome{Sources: []SourceResult{
		{Source: "a", Status: SourceDegraded},
		{Source: "b", Status: SourceDegraded},
	}}
	if code := o.ExitCode(); code != 3 {
		t.Fatalf("ExitCode = %d, want 3", code)
	}
}

func TestFanOutOutcome_ExitCode_DegradedPlusDisabled_IsPartialNotTotalFailure(t *testing.T) {
	// One degraded (real failure) plus one disabled (healthy, per A2) is
	// a partial degradation, not a total failure: at least one source is
	// healthy.
	o := FanOutOutcome{Sources: []SourceResult{
		{Source: "a", Status: SourceDegraded},
		{Source: "b", Status: SourceDisabled},
	}}
	if code := o.ExitCode(); code != 5 {
		t.Fatalf("ExitCode = %d, want 5", code)
	}
}

func TestFanOutOutcome_ExitCode_NoSources(t *testing.T) {
	o := FanOutOutcome{}
	if code := o.ExitCode(); code != 3 {
		t.Fatalf("ExitCode = %d, want 3", code)
	}
}

func TestFanOutOutcome_ExitCode_NeverOne(t *testing.T) {
	// 1 is reserved for the CLI's own generic/unexpected-failure path and
	// must never be returned by the fan-out scheme for an in-taxonomy
	// outcome, across every combination of statuses.
	statuses := []SourceStatus{SourceSucceeded, SourceDegraded, SourceDisabled}
	for _, s1 := range statuses {
		for _, s2 := range statuses {
			o := FanOutOutcome{Sources: []SourceResult{{Status: s1}, {Status: s2}}}
			if code := o.ExitCode(); code == 1 {
				t.Fatalf("ExitCode(%v,%v) = 1, must never be 1", s1, s2)
			}
		}
	}
}

func TestTargetedExitCode_Success(t *testing.T) {
	if code := TargetedExitCode(nil); code != 0 {
		t.Fatalf("TargetedExitCode(nil) = %d, want 0", code)
	}
}

func TestTargetedExitCode_NotFound(t *testing.T) {
	err := scriptout.WrapError(scriptout.ErrNotFound, "no such pr")
	if code := TargetedExitCode(err); code != 4 {
		t.Fatalf("TargetedExitCode(not_found) = %d, want 4", code)
	}
}

func TestTargetedExitCode_ClassifiesByWireError(t *testing.T) {
	// Pinned literals equal x/exitcode: Usage 2, Unavailable 3, NotFound 4,
	// Denied 8.
	for _, tc := range []struct {
		sentinel error
		want     int
	}{
		{scriptout.ErrNotFound, 4},
		{scriptout.ErrUnauthenticated, 8},
		{scriptout.ErrUnavailable, 3},
		{scriptout.ErrInvalidArgument, 2},
		{scriptout.ErrQueryNotRecognized, 2},
		{scriptout.ErrUnknownOp, 1},
		{scriptout.ErrVersionMismatch, 1},
	} {
		err := scriptout.WrapError(tc.sentinel, "detail")
		if code := TargetedExitCode(err); code != tc.want {
			t.Errorf("TargetedExitCode(%v) = %d, want %d", tc.sentinel, code, tc.want)
		}
	}
	if code := TargetedExitCode(errors.New("plain failure")); code != 1 {
		t.Errorf("TargetedExitCode(unclassified) = %d, want 1", code)
	}
}

func TestTargetedExitCode_ConfigError_IsConfig7(t *testing.T) {
	cause := errors.New("registry: parse x: bad key")
	for name, err := range map[string]error{
		"direct":  asConfigError(cause),
		"wrapped": fmt.Errorf("outer: %w", asConfigError(cause)),
	} {
		if code := TargetedExitCode(err); code != exitcode.Config {
			t.Errorf("%s: TargetedExitCode = %d, want %d", name, code, exitcode.Config)
		}
	}
	if code := TargetedExitCode(cause); code != exitcode.Error {
		t.Errorf("plain error: TargetedExitCode = %d, want %d", code, exitcode.Error)
	}
	if asConfigError(nil) != nil {
		t.Error("asConfigError(nil) must stay nil")
	}
}
