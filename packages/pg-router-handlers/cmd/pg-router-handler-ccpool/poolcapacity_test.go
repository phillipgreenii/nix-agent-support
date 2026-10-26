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
// ccpool from the dir, cannot depend on the display name), and that every
// pool is emitted in name order.
func TestEmitPoolCapacity_passesOnlyDirNeverName(t *testing.T) {
	pools := []poolEntry{
		{name: "worker", dir: "/state/pg-router-ccpool-worker"},
		{name: "totally-different-label", dir: "/state/pg-router-ccpool-review"},
	}
	var got []string
	var buf bytes.Buffer
	code := emitPoolCapacity(&buf, pools, func(_ context.Context, dir string) error {
		got = append(got, dir)
		return nil
	})
	if code != conformance.ExitOK || buf.Len() != 0 {
		t.Fatalf("code=%d out=%q, want ExitOK and no output", code, buf.String())
	}
	want := []string{"/state/pg-router-ccpool-review", "/state/pg-router-ccpool-worker"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("emitted dirs = %v, want %v", got, want)
	}
}

// TestEmitPoolCapacity_onePoolFailureDoesNotStopOthers: a failing pool is
// reported and flips the exit code, but the healthy pool is still emitted.
func TestEmitPoolCapacity_onePoolFailureDoesNotStopOthers(t *testing.T) {
	pools := []poolEntry{
		{name: "feedback", dir: "/state/feedback"},
		{name: "worker", dir: "/state/worker"},
	}
	var emitted []string
	var buf bytes.Buffer
	code := emitPoolCapacity(&buf, pools, func(_ context.Context, dir string) error {
		if dir == "/state/feedback" {
			return errors.New("pool dir unreadable")
		}
		emitted = append(emitted, dir)
		return nil
	})
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want ExitError", code)
	}
	if len(emitted) != 1 || emitted[0] != "/state/worker" {
		t.Errorf("healthy pool must still be emitted; got %v", emitted)
	}
	if !strings.Contains(buf.String(), `role="feedback"`) || !strings.Contains(buf.String(), "pool dir unreadable") {
		t.Errorf("failure not reported: %q", buf.String())
	}
}

func TestRunPoolCapacity_requiresAtLeastOnePool(t *testing.T) {
	if code := runPoolCapacity(nil); code != conformance.ExitUsage {
		t.Errorf("runPoolCapacity(nil) = %d, want ExitUsage", code)
	}
}

func TestRunPoolCapacity_rejectsMalformedPoolFlag(t *testing.T) {
	if code := runPoolCapacity([]string{"--pool", "not-a-pair"}); code != conformance.ExitUsage {
		t.Errorf("runPoolCapacity(malformed --pool) = %d, want ExitUsage", code)
	}
}

func TestRunPoolCapacity_rejectsTrailingArgs(t *testing.T) {
	if code := runPoolCapacity([]string{"--pool", "review=/state/review", "extra"}); code != conformance.ExitUsage {
		t.Errorf("runPoolCapacity(trailing arg) = %d, want ExitUsage", code)
	}
}
