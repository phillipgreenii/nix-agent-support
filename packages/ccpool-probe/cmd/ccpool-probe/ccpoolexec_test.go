package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestListCcpoolSessionsEmpty(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_empty")
	rows, err := listCcpoolSessions(context.Background(), "", noopWarn)
	if err != nil {
		t.Fatalf("listCcpoolSessions: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0", len(rows))
	}
}

func TestListCcpoolSessionsNeedsInput(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_needs_input_one")
	rows, err := listCcpoolSessions(context.Background(), "needs_input", noopWarn)
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
	rows, err := listCcpoolSessions(context.Background(), "", noopWarn)
	if err != nil {
		t.Fatalf("listCcpoolSessions: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
}

func TestListCcpoolSessionsFailure(t *testing.T) {
	withCcpoolFactory(t, "ccpool_list_fail")
	_, err := listCcpoolSessions(context.Background(), "", noopWarn)
	if err == nil {
		t.Fatalf("expected an error")
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
	_, err := listCcpoolSessions(ctx, "", noopWarn)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected a timeout error")
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
		_, _ = listCcpoolSessions(context.Background(), "needs_input", noopWarn)
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
		_, _ = listCcpoolSessions(context.Background(), "", noopWarn)
	})
	if strings.Contains(got, "--state") {
		t.Errorf("expected no --state flag when unset, got %s", got)
	}
}
