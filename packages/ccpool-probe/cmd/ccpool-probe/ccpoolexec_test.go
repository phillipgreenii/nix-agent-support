package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListCcpoolSessionsEmpty(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_empty")
	rows, err := listCcpoolSessions(context.Background(), "", "", noopWarn)
	if err != nil {
		t.Fatalf("listCcpoolSessions: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0", len(rows))
	}
}

func TestListCcpoolSessionsNeedsInput(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_needs_input_one")
	rows, err := listCcpoolSessions(context.Background(), "", "needs_input", noopWarn)
	if err != nil {
		t.Fatalf("listCcpoolSessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ExternalID != "sess-1" || rows[0].State != "needs_input" {
		t.Fatalf("got %+v", rows[0])
	}
}

func TestListCcpoolSessionsMixedStates(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_mixed_states")
	rows, err := listCcpoolSessions(context.Background(), "", "", noopWarn)
	if err != nil {
		t.Fatalf("listCcpoolSessions: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
}

func TestListCcpoolSessionsFailure(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_fail")
	_, err := listCcpoolSessions(context.Background(), "", "", noopWarn)
	if err == nil {
		t.Fatalf("expected an error")
	}
	// A ccpool that ran and exited non-zero is NOT a killed call: it must
	// not be retried or mistaken for host-load noise (pg2-zzf54).
	if !errors.Is(err, errCcpoolFailed) || errors.Is(err, errCcpoolKilled) {
		t.Fatalf("exit-1 failure must be errCcpoolFailed and not errCcpoolKilled, got %v", err)
	}
}

// TestListCcpoolSessionsHonorsExplicitTimeout proves this probe's own
// ccpool subprocess calls respect an explicit context deadline rather
// than hanging on a wedged ccpool process -- the "ccpool subprocess" half
// of "every external call in run MUST carry an explicit timeout"
// (connector_test.go's TestListEscalatedHonorsExplicitTimeout is the
// pg-connector half).
func TestListCcpoolSessionsHonorsExplicitTimeout(t *testing.T) {
	withCcpoolFactory(t, "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := listCcpoolSessions(ctx, "", "", noopWarn)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected a timeout error")
	}
	// The SIGKILLed child (Go ExitCode() == -1, empty stderr) is classified
	// as a killed call, the retryable class (pg2-zzf54), and still is a
	// ccpool failure for every other caller.
	if !errors.Is(err, errCcpoolKilled) || !errors.Is(err, errCcpoolFailed) {
		t.Fatalf("a deadline-killed call must be errCcpoolKilled (wrapping errCcpoolFailed), got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("listCcpoolSessions did not respect its context deadline: took %v", elapsed)
	}
}

// TestListCcpoolSessionsArgvShape proves the real argv this probe sends
// to ccpool -- --all, the pgrouter.pool=pg-router filter, and (when
// requested) --state -- rather than trusting a code read of the
// hardcoded constants.
func TestListCcpoolSessionsArgvShape(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_empty")
	got := recordedArgs(t, func() {
		_, _ = listCcpoolSessions(context.Background(), "", "needs_input", noopWarn)
	})
	for _, want := range []string{"--all", "--filter", pgRouterPoolFilter, "--json", "--state", "needs_input"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in argv, got %s", want, got)
		}
	}
}

func TestListCcpoolSessionsArgvOmitsStateWhenUnset(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_empty")
	got := recordedArgs(t, func() {
		_, _ = listCcpoolSessions(context.Background(), "", "", noopWarn)
	})
	if strings.Contains(got, "--state") {
		t.Errorf("expected no --state flag when unset, got %s", got)
	}
}

// TestListCcpoolSessionsScopesChildToPoolDir proves the pool directory is
// really exported to the child as CCPOOL_POOL (bead pg2-bkzrc): the fake
// ccpool answers differently per pool, so a probe that ignored poolDir
// would see an empty list for the review pool.
func TestListCcpoolSessionsScopesChildToPoolDir(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_by_pool")
	root := t.TempDir()

	rows, err := listCcpoolSessions(context.Background(), filepath.Join(root, "pg-router-ccpool-review"), "needs_input", noopWarn)
	if err != nil {
		t.Fatalf("review pool: %v", err)
	}
	if len(rows) != 1 || rows[0].ExternalID != "sess-r1" || !rows[0].Live {
		t.Fatalf("review pool: got %+v, want its one live needs_input session", rows)
	}

	rows, err = listCcpoolSessions(context.Background(), filepath.Join(root, "pg-router-ccpool-worker"), "needs_input", noopWarn)
	if err != nil {
		t.Fatalf("worker pool: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("worker pool: got %+v, want none", rows)
	}

	t.Setenv("CCPOOL_POOL", "")
	rows, err = listCcpoolSessions(context.Background(), "", "needs_input", noopWarn)
	if err != nil || len(rows) != 0 {
		t.Fatalf("ambient pool: rows=%+v err=%v, want none", rows, err)
	}
}
