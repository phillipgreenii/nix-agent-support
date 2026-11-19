package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

func TestPoolFlag_setParsesNameDir(t *testing.T) {
	var f poolFlag
	if err := f.Set("review=/state/review"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Set("worker=/state/worker"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := []poolEntry{{name: "review", dir: "/state/review"}, {name: "worker", dir: "/state/worker"}}
	if len(f.entries) != len(want) || f.entries[0] != want[0] || f.entries[1] != want[1] {
		t.Errorf("entries = %+v, want %+v", f.entries, want)
	}
}

func TestPoolFlag_setRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"noequals", "=novalue", "noname="} {
		var f poolFlag
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%q) should have failed", bad)
		}
	}
}

// TestEmitPoolCapacity_passesOnlyDirNeverName proves the emitter sees only the
// dir half of --pool name=dir (so the metric's pool attribute, derived by
// ccpool from the dir, cannot depend on the display name), in name order, in
// ONE emit call for every pool (one ccpool process, hence one recorder dial).
func TestEmitPoolCapacity_passesOnlyDirNeverName(t *testing.T) {
	pools := []poolEntry{
		{name: "worker", dir: "/state/pg-router-ccpool-worker"},
		{name: "totally-different-label", dir: "/state/pg-router-ccpool-review"},
	}
	var calls [][]string
	var buf bytes.Buffer
	code := emitPoolCapacity(&buf, pools, func(_ context.Context, dirs []string) error {
		calls = append(calls, dirs)
		return nil
	})
	if code != conformance.ExitOK || buf.Len() != 0 {
		t.Fatalf("code=%d out=%q, want ExitOK and no output", code, buf.String())
	}
	want := "/state/pg-router-ccpool-review,/state/pg-router-ccpool-worker"
	if len(calls) != 1 || strings.Join(calls[0], ",") != want {
		t.Errorf("emit calls = %v, want one call with %s", calls, want)
	}
}

// TestEmitPoolCapacity_failureIsReportedWithRoles: a failed emit flips the exit
// code and the line names the roles and the cause.
func TestEmitPoolCapacity_failureIsReportedWithRoles(t *testing.T) {
	pools := []poolEntry{
		{name: "feedback", dir: "/state/feedback"},
		{name: "worker", dir: "/state/worker"},
	}
	var buf bytes.Buffer
	code := emitPoolCapacity(&buf, pools, func(context.Context, []string) error {
		return errors.New("pool dir unreadable")
	})
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want ExitError", code)
	}
	if !strings.Contains(buf.String(), "feedback") || !strings.Contains(buf.String(), "worker") || !strings.Contains(buf.String(), "pool dir unreadable") {
		t.Errorf("failure not reported: %q", buf.String())
	}
}
