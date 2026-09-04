package main

import (
	"testing"

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
	if code := o.ExitCode(); code != 2 {
		t.Fatalf("ExitCode = %d, want 2", code)
	}
}

func TestFanOutOutcome_ExitCode_Disabled_StillCountsAsNotSucceeded(t *testing.T) {
	o := FanOutOutcome{Sources: []SourceResult{
		{Source: "a", Status: SourceSucceeded},
		{Source: "b", Status: SourceDisabled},
	}}
	if code := o.ExitCode(); code != 2 {
		t.Fatalf("ExitCode = %d, want 2", code)
	}
}

func TestFanOutOutcome_ExitCode_TotalFailure(t *testing.T) {
	o := FanOutOutcome{Sources: []SourceResult{
		{Source: "a", Status: SourceDegraded},
		{Source: "b", Status: SourceDisabled},
	}}
	if code := o.ExitCode(); code != 3 {
		t.Fatalf("ExitCode = %d, want 3", code)
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

func TestTargetedExitCode_OtherErrorsAreOne(t *testing.T) {
	for _, sentinel := range []error{
		scriptout.ErrUnauthenticated,
		scriptout.ErrUnavailable,
		scriptout.ErrUnknownOp,
		scriptout.ErrVersionMismatch,
	} {
		err := scriptout.WrapError(sentinel, "detail")
		if code := TargetedExitCode(err); code != 1 {
			t.Errorf("TargetedExitCode(%v) = %d, want 1", sentinel, code)
		}
	}
}
