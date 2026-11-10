package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of the heartbeat catch-up (docs/behavior/pg-desk/reinterpret.md, "The
// heartbeat catch-up"; INV-REINTERPRET-10 and INV-OPERATOR-13), over a real
// on-disk store and the real heartbeat command.

// withReleaseVersion makes the build version a non-dev one, so the catch-up
// runs.
func withReleaseVersion(t *testing.T) {
	t.Helper()
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = "v-test"
}

// withHeartbeatClock replaces the budget clock with one that reads a fixed
// offset after the first call (the heartbeat's start).
func withHeartbeatClock(t *testing.T, sinceStart time.Duration) {
	t.Helper()
	orig := heartbeatClock
	t.Cleanup(func() { heartbeatClock = orig })
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	calls := 0
	heartbeatClock = func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(sinceStart)
	}
}

func seedOpen(t *testing.T, rs *riStore, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		rs.pr(t, i, riFacts("open", false, "me"))
	}
}

func behindCount(t *testing.T, rs *riStore) int {
	t.Helper()
	rs.seed.SetInterpStamp(riStamp())
	keys, known, err := rs.seed.InterpretationsBehind(riRepo, 0)
	if err != nil || !known {
		t.Fatalf("behind: known=%v err=%v", known, err)
	}
	return len(keys)
}

func TestCatchUpConstantsArePinned(t *testing.T) {
	if CatchUpBudget != 10*time.Second {
		t.Errorf("CatchUpBudget = %s, want 10s (it MUST NOT be raised)", CatchUpBudget)
	}
	if CatchUpMaxRows != 25 {
		t.Errorf("CatchUpMaxRows = %d, want 25", CatchUpMaxRows)
	}
}

// The budget is the plan's 10 s capped by what the period leaves after the
// ledger read and the margin: 60 - 30 - 5 = 25 s from the start.
func TestCatchUpBudgetLeft(t *testing.T) {
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		elapsed time.Duration
		want    time.Duration
	}{
		{0, CatchUpBudget},
		{15 * time.Second, CatchUpBudget},
		{16 * time.Second, 9 * time.Second},
		{24 * time.Second, time.Second},
		{25 * time.Second, 0},
		{45 * time.Second, -20 * time.Second}, // the probe's own worst case
	} {
		if got := catchUpBudgetLeft(start, start.Add(c.elapsed)); got != c.want {
			t.Errorf("elapsed %s: budget = %s, want %s", c.elapsed, got, c.want)
		}
	}
}

// The catch-up changes a stale open PR, and the heartbeat still stamps
// liveness, records the source fetches and exits 0.
func TestHeartbeatCatchUpReinterpretsAndHeartbeatCompletes(t *testing.T) {
	withReleaseVersion(t)
	rs := newRIStore(t)
	seedOpen(t, rs, 2)
	withHeartbeatLedger(t, heartbeatLedgerJSON, nil)
	stderr, err := runHeartbeat(t)
	if err != nil {
		t.Fatalf("heartbeat: %v\n%s", err, stderr)
	}
	if got := behindCount(t, rs); got != 0 {
		t.Errorf("behind = %d, want 0", got)
	}
	if row := rs.row(t, 1); row.InterpVersion != riStamp() {
		t.Errorf("stamp = %q, want %q", row.InterpVersion, riStamp())
	}
	if _, found, _ := rs.seed.GetMeta(store.MetaKeyLastHeartbeat); !found {
		t.Error("liveness not stamped")
	}
	if _, found, _ := rs.seed.GetMeta("source_fetch.pg-connector-pr-github.team"); !found {
		t.Error("source fetch not recorded")
	}
	if stderr != "" {
		t.Errorf("a pass that finished says nothing, got %q", stderr)
	}
}

// Row cap: a pass re-interprets at most CatchUpMaxRows rows, names the
// remaining count, and later heartbeats finish.
func TestHeartbeatCatchUpStopsAtRowCap(t *testing.T) {
	withReleaseVersion(t)
	rs := newRIStore(t)
	seedOpen(t, rs, CatchUpMaxRows+5)
	withHeartbeatLedger(t, "[]", nil)
	stderr, err := runHeartbeat(t)
	if err != nil {
		t.Fatal(err)
	}
	if got := behindCount(t, rs); got != 5 {
		t.Errorf("behind after one pass = %d, want 5", got)
	}
	if !strings.Contains(stderr, "stopped at the row cap") || !strings.Contains(stderr, "5 remaining") {
		t.Errorf("stderr = %q, want the row cap and 5 remaining", stderr)
	}
	if _, err := runHeartbeat(t); err != nil {
		t.Fatal(err)
	}
	if got := behindCount(t, rs); got != 0 {
		t.Errorf("behind after two passes = %d, want 0", got)
	}
}

// Exactly CatchUpMaxRows behind is not a limit stop.
func TestHeartbeatCatchUpAtExactlyTheCapIsSilent(t *testing.T) {
	withReleaseVersion(t)
	rs := newRIStore(t)
	seedOpen(t, rs, CatchUpMaxRows)
	withHeartbeatLedger(t, "[]", nil)
	stderr, err := runHeartbeat(t)
	if err != nil || stderr != "" {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
	if got := behindCount(t, rs); got != 0 {
		t.Errorf("behind = %d", got)
	}
}

// Time budget: with a slow row the pass stops at the deadline between rows, the
// row in flight finishes (it is not marked failed), and the heartbeat is intact.
func TestHeartbeatCatchUpStopsAtBudget(t *testing.T) {
	withReleaseVersion(t)
	rs := newRIStore(t)
	seedOpen(t, rs, 6)
	withHeartbeatLedger(t, "[]", nil)
	origRows := catchUpBeforeRow
	t.Cleanup(func() { catchUpBeforeRow = origRows })
	catchUpBeforeRow = func(store.InterpretationKey) { time.Sleep(60 * time.Millisecond) }

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	st, err := store.Open(rs.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cmd, _ := newCatchUpCmd(ctx)
	out, err := catchUp(ctx, cmd, st, CatchUpMaxRows)
	if err != nil {
		t.Fatal(err)
	}
	if out.stoppedBy != "budget" {
		t.Fatalf("stoppedBy = %q, want budget", out.stoppedBy)
	}
	done := out.counts.Changed + out.counts.Unchanged
	if done < 1 || done >= 6 {
		t.Errorf("re-interpreted %d rows, want some but not all", done)
	}
	if out.counts.Failed != 0 {
		t.Errorf("failed = %d: a deadline must not mark a row failed", out.counts.Failed)
	}
	if out.remaining != 6-done {
		t.Errorf("remaining = %d, want %d", out.remaining, 6-done)
	}
}

// When the earlier steps ate the period the catch-up does not start; the
// heartbeat is whole.
func TestHeartbeatCatchUpSkipsWhenThePeriodIsSpent(t *testing.T) {
	withReleaseVersion(t)
	rs := newRIStore(t)
	seedOpen(t, rs, 2)
	withHeartbeatLedger(t, heartbeatLedgerJSON, nil)
	withHeartbeatClock(t, 50*time.Second)
	stderr, err := runHeartbeat(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "catch-up skipped") {
		t.Errorf("stderr = %q", stderr)
	}
	if got := behindCount(t, rs); got != 2 {
		t.Errorf("behind = %d, want 2 (nothing re-interpreted)", got)
	}
	if _, found, _ := rs.seed.GetMeta(store.MetaKeyLastHeartbeat); !found {
		t.Error("liveness not stamped")
	}
}

// A previous catch-up still running (a held lock) makes this one skip.
func TestHeartbeatCatchUpSkipsWhileAnotherRuns(t *testing.T) {
	withReleaseVersion(t)
	rs := newRIStore(t)
	seedOpen(t, rs, 2)
	withHeartbeatLedger(t, "[]", nil)
	unlock, err := store.NewLocker(store.LockerOptions{LockDir: dirOf(rs.seed.Path())}).Lock(riRepo, "catch-up", "heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := runHeartbeat(t)
	if err != nil || stderr != "" {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
	if got := behindCount(t, rs); got != 2 {
		t.Errorf("behind = %d, want 2 while another catch-up runs", got)
	}
	unlock()
	if _, err := runHeartbeat(t); err != nil {
		t.Fatal(err)
	}
	if got := behindCount(t, rs); got != 0 {
		t.Errorf("behind = %d, want 0 after the lock is released", got)
	}
}

// A dev build skips; nothing is selected or written.
func TestHeartbeatCatchUpSkipsDevBuild(t *testing.T) {
	rs := newRIStore(t)
	seedOpen(t, rs, 1)
	withHeartbeatLedger(t, "[]", nil)
	if Version != "dev" {
		t.Skip("test binary has a release version")
	}
	if _, err := runHeartbeat(t); err != nil {
		t.Fatal(err)
	}
	if got := behindCount(t, rs); got != 1 {
		t.Errorf("behind = %d, want 1", got)
	}
}

// A catch-up failure never fails the heartbeat or stops the source record.
func TestHeartbeatCatchUpFailureNeverFailsHeartbeat(t *testing.T) {
	withReleaseVersion(t)
	for name, mutate := range map[string]func(t *testing.T){
		"config load error": func(t *testing.T) {
			deskConfigLoad = func(context.Context) (*config.Config, error) { return nil, errors.New("boom") }
		},
		"no repository": func(t *testing.T) {
			deskConfigLoad = func(context.Context) (*config.Config, error) { return &config.Config{}, nil }
		},
		"schema 1 store": func(t *testing.T) {
			open, _ := newStoreAt(t, "old")
			deskStoreOpen = open
		},
	} {
		t.Run(name, func(t *testing.T) {
			rs := newRIStore(t)
			seedOpen(t, rs, 1)
			withHeartbeatLedger(t, heartbeatLedgerJSON, nil)
			mutate(t)
			stderr, err := runHeartbeat(t)
			if err != nil {
				t.Fatalf("heartbeat failed: %v", err)
			}
			if !strings.Contains(stderr, "interpretation catch-up") {
				t.Errorf("stderr = %q, want one catch-up line", stderr)
			}
		})
	}
}

// An unreadable behind query (a store with no interpretation table) is an
// error line, not a failed heartbeat.
func TestHeartbeatCatchUpQueryErrorIsOneLine(t *testing.T) {
	withReleaseVersion(t)
	rs := newRIStore(t)
	seedOpen(t, rs, 1)
	rs.exec(t, `ALTER TABLE interpretation RENAME COLUMN entity_id TO entity_idx`)
	withHeartbeatLedger(t, "[]", nil)
	stderr, err := runHeartbeat(t)
	if err != nil {
		t.Fatalf("heartbeat failed: %v", err)
	}
	if !strings.Contains(stderr, "interpretation catch-up:") {
		t.Errorf("stderr = %q", stderr)
	}
}

// The manual command has no row cap and no budget.
func TestReinterpretOpenIgnoresHeartbeatLimits(t *testing.T) {
	rs := newRIStore(t)
	seedOpen(t, rs, CatchUpMaxRows+5)
	if _, _, err := riRun(t, "--open"); err != nil {
		t.Fatal(err)
	}
	if got := behindCount(t, rs); got != 0 {
		t.Errorf("behind = %d, want 0", got)
	}
}

func dirOf(path string) string { return filepath.Dir(path) }

// newCatchUpCmd is a command whose context is ctx, as the heartbeat's is.
func newCatchUpCmd(ctx context.Context) (*cobra.Command, *bytes.Buffer) {
	var errb bytes.Buffer
	c := &cobra.Command{}
	c.SetContext(ctx)
	c.SetErr(&errb)
	c.SetOut(io.Discard)
	return c, &errb
}

// A lock that cannot be created (not a contention) is reported as an error line.
func TestCatchUpLockFailureIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	rs := newRIStore(t)
	dir := dirOf(rs.seed.Path())
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	st, err := store.Open(rs.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cmd, _ := newCatchUpCmd(context.Background())
	if _, err := catchUp(context.Background(), cmd, st, 1); err == nil {
		t.Fatal("error = nil, want the lock failure")
	}
}

// Nothing behind: the catch-up is silent and writes nothing.
func TestHeartbeatCatchUpIdleIsSilent(t *testing.T) {
	withReleaseVersion(t)
	newRIStore(t)
	withHeartbeatLedger(t, "[]", nil)
	stderr, err := runHeartbeat(t)
	if err != nil || stderr != "" {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
}
