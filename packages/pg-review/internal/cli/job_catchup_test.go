package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/appcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/x/exitcode"
)

// seedScheduledRun writes a terminal scheduled run for a window that ended
// recently enough to be inside its catch-up expiry.
func seedScheduledRun(t *testing.T, state string) {
	t.Helper()
	l, err := ledger.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	to := time.Now().UTC().Add(-2 * time.Hour)
	err = l.WithTx(context.Background(), func(tx *ledger.Tx) error {
		_, err := tx.InsertRun(context.Background(), ledger.Run{
			Lens: "agent-workflow", Scope: "demo", ScheduleID: "daily", RunKey: "agent-workflow/demo/24h/x", Trigger: "scheduled",
			State: state, Analyst: "ccpool", PID: 1, EndedAt: to,
			WindowFrom: to.Add(-24 * time.Hour).Format(time.RFC3339Nano), WindowTo: to.Format(time.RFC3339Nano),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJobCatchUpNothingEligibleExitsSkipped(t *testing.T) {
	jobEnv(t, true)
	code, out, _ := run(t, "job", "catch-up")
	if code != appcode.Skipped || !strings.Contains(out, "no skipped scheduled window") {
		t.Fatalf("exit %d %q", code, out)
	}
	seedScheduledRun(t, ledger.RunFailed)
	if code, _, _ := run(t, "job", "catch-up"); code != appcode.Skipped {
		t.Errorf("failed window exit %d", code)
	}
}

func TestJobCatchUpUsageErrors(t *testing.T) {
	jobEnv(t, true)
	for _, args := range [][]string{
		{"job", "catch-up", "--force"},
		{"job", "catch-up", "a", "b"},
	} {
		if code, _, stderr := run(t, args...); code != exitcode.Usage {
			t.Errorf("%v: exit %d %s", args, code, stderr)
		}
	}
}

// A skipped window whose capacity check still fails is skipped again (exit
// Skipped) and leaves one more Skipped row under the same run key.
func TestJobCatchUpStillSkippedWritesAnotherRow(t *testing.T) {
	jobEnv(t, true)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ccpool"), []byte("#!/bin/sh\necho recorder unreachable >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	seedScheduledRun(t, ledger.RunSkipped)
	code, _, stderr := run(t, "job", "catch-up")
	if code != appcode.Skipped || !strings.Contains(stderr, "capacity check failed") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	l, err := ledger.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	ws, err := l.ScheduledWindows(context.Background())
	if err != nil || len(ws) != 1 || ws[0].Attempts != 2 || ws[0].Filed {
		t.Fatalf("%+v %v", ws, err)
	}
}
