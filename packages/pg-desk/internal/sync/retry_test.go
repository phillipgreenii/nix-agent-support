package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests for the automatic-retry policy of a recorded sync_error (bead
// pg2-xb6fs): transient vs non-transient classification, the backoff
// schedule, and the retry bound.

func TestClassify(t *testing.T) {
	connErr := func(code string) error {
		return &ConnectorError{Args: []string{"issue", "create"}, ExitCode: 1, Code: code, Detail: code + ": x"}
	}
	cases := []struct {
		name string
		err  error
		want ErrorClass
	}{
		// Environmental: retried.
		{"unavailable (unmounted volume / missing path)", connErr("unavailable"), ClassTransient},
		{"targeted not_found (exit 4)", &ConnectorError{Args: []string{"issue", "update", "x"}, ExitCode: 4, Code: "not_found"}, ClassTransient},
		{"no error envelope", connErr(""), ClassTransient},
		{"exec failure", errors.New(`sync: exec pg-connector [issue create]: exec: "pg-connector": executable file not found in $PATH`), ClassTransient},
		{"timeout", fmt.Errorf("sync: create anchor: %w", context.DeadlineExceeded), ClassTransient},
		{"gather failure during a retry", errors.New("pipeline: gather pr 7: pg-connector [pr show]: exit 1: network is unreachable"), ClassTransient},
		// Needs a person: never retried.
		{"unauthenticated (auth)", connErr("unauthenticated"), ClassNonTransient},
		{"invalid_argument (validation)", connErr("invalid_argument"), ClassNonTransient},
		{"unknown_op", connErr("unknown_op"), ClassNonTransient},
		{"version_mismatch", connErr("version_mismatch"), ClassNonTransient},
		{"query_not_recognized", connErr("query_not_recognized"), ClassNonTransient},
		{"unknown sync.mode", fmt.Errorf("%w %q (want off, plan, or apply)", errUnknownMode, "sideways"), ClassNonTransient},
		// bd >= 1.3.1's refusal to close a parent with open children (bead
		// pg2-ubvmh) arrives wrapped as connector code "unavailable" but cannot
		// self-heal: needs a person, never retried.
		{"bd open-child refusal (unavailable)", &ConnectorError{
			Args: []string{"issue", "transition", "zr-7c0en", "--state", "closed"}, ExitCode: 1, Code: "unavailable",
			Detail: "unavailable: cannot close zr-7c0en: 1 open child issue(s); close children first or use --force to override",
		}, ClassNonTransient},
		{"bd open-child refusal (no error code)", &ConnectorError{
			Args: []string{"issue", "transition", "zr-7c0en"}, ExitCode: 1,
			Detail: "cannot close zr-7c0en: 3 open child issue(s); close children first",
		}, ClassNonTransient},
		{"wrapped bd open-child refusal", fmt.Errorf("pipeline: sync pr 7: %w", fmt.Errorf("sync: close anchor zr-7c0en: %w",
			&ConnectorError{ExitCode: 1, Code: "unavailable", Detail: "unavailable: cannot close zr-7c0en: 1 open child issue(s); close children first"})), ClassNonTransient},
		// The refusal is recognized by its message, not by "unavailable"
		// alone: an unrelated unavailable close failure still retries.
		{"unavailable close failure that is not the refusal", &ConnectorError{ExitCode: 1, Code: "unavailable", Detail: "unavailable: cannot close zr-7c0en: database is locked"}, ClassTransient},
		{"open-child words outside a close refusal", &ConnectorError{ExitCode: 1, Code: "unavailable", Detail: "unavailable: listing open child issue(s) failed: chdir /x: no such file or directory"}, ClassTransient},
		// Classification sees through the wrapping every sync and pipeline
		// layer adds.
		{"wrapped non-transient", fmt.Errorf("pipeline: sync pr 7: %w", fmt.Errorf("sync: create anchor: %w", connErr("unauthenticated"))), ClassNonTransient},
		{"wrapped transient", fmt.Errorf("pipeline: sync pr 7: %w", connErr("unavailable")), ClassTransient},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.err); got != c.want {
				t.Fatalf("Classify(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// TestConnectorErrorKeepsTheRecordedMessageShape: typing the connector error
// must not change the sync_error text operators and alerts already see.
func TestConnectorErrorKeepsTheRecordedMessageShape(t *testing.T) {
	args := []string{"issue", "create", "--title", "t"}
	if got, want := (&ConnectorError{Args: args, ExitCode: 4, Code: "not_found"}).Error(), "sync: pg-connector [issue create --title t]: not_found"; got != want {
		t.Errorf("exit 4: got %q, want %q", got, want)
	}
	if got, want := (&ConnectorError{Args: args, ExitCode: 1, Code: "unavailable", Detail: "unavailable: boom"}).Error(), "sync: pg-connector [issue create --title t]: exit 1: unavailable: boom"; got != want {
		t.Errorf("exit 1: got %q, want %q", got, want)
	}
}

// TestSync_UnknownModeIsNonTransient: Sync's own config error classifies as
// needing a person, and keeps its message.
func TestSync_UnknownModeIsNonTransient(t *testing.T) {
	s := newTestSyncer(t, "sideways")
	err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, gather.Facts{PRShow: prFixture(nil)}, interpFor("mine", nil))
	if err == nil || !strings.Contains(err.Error(), `sync: unknown sync.mode "sideways"`) {
		t.Fatalf("Sync: err = %v, want the unknown sync.mode error", err)
	}
	if got := Classify(err); got != ClassNonTransient {
		t.Fatalf("Classify = %q, want non-transient", got)
	}
}

// TestSync_MissingBeadsWorkspaceIsTransientAndSelfHeals drives the real
// pg-connector wire path: while the beads workspace directory is missing (an
// unmounted volume), anchor creation fails with wire code "unavailable" and
// bd's chdir error, which classifies as transient; once the directory
// appears, the same Sync succeeds.
func TestSync_MissingBeadsWorkspaceIsTransientAndSelfHeals(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	withFactory(t)
	dir := filepath.Join(t.TempDir(), "gitrepos", ".beads")
	t.Setenv("GO_HELPER_REQUIRE_DIR", dir)

	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA} // mine: needs a review request, so an anchor
	interp := interpFor("mine", nil)
	err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp)
	if err == nil {
		t.Fatal("Sync: want a failure while the beads workspace is missing")
	}
	for _, want := range []string{"exit 1", "unavailable", "chdir " + dir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
	var ce *ConnectorError
	if !errors.As(err, &ce) || ce.Code != "unavailable" {
		t.Fatalf("error chain should carry a *ConnectorError with code unavailable, got %#v", err)
	}
	if got := Classify(err); got != ClassTransient {
		t.Fatalf("Classify = %q, want transient", got)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync after the workspace reappeared: %v", err)
	}
}

// TestSync_AuthFailureIsNonTransient drives the real wire path with an
// unauthenticated backend: the failure needs a person.
func TestSync_AuthFailureIsNonTransient(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	withFactory(t)
	t.Setenv("GO_HELPER_FAIL_CODE", "unauthenticated")
	err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA}, interpFor("mine", nil))
	if err == nil {
		t.Fatal("Sync: want the injected auth failure")
	}
	if got := Classify(err); got != ClassNonTransient {
		t.Fatalf("Classify(%v) = %q, want non-transient", err, got)
	}
}

func TestRetryPolicyBackoffSchedule(t *testing.T) {
	p := RetryPolicy{MaxRetries: 10, InitialBackoff: time.Minute, MaxBackoff: 30 * time.Minute}
	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
		30 * time.Minute, 30 * time.Minute, 30 * time.Minute,
	}
	for i, w := range want {
		if got := p.Backoff(i + 1); got != w {
			t.Errorf("Backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
	// A very large attempt count stays at the cap (no overflow).
	if got := p.Backoff(500); got != 30*time.Minute {
		t.Errorf("Backoff(500) = %s, want the 30m cap", got)
	}
}

func TestRetryPolicyFor(t *testing.T) {
	if got := RetryPolicyFor(nil); got != (RetryPolicy{MaxRetries: 10, InitialBackoff: time.Minute, MaxBackoff: 30 * time.Minute}) {
		t.Errorf("RetryPolicyFor(nil) = %+v, want the defaults", got)
	}
	three := 3
	cfg := &config.Config{Sync: config.SyncConfig{Retry: config.SyncRetryConfig{MaxRetries: &three, InitialBackoff: "10s", MaxBackoff: "1m"}}}
	if got := RetryPolicyFor(cfg); got != (RetryPolicy{MaxRetries: 3, InitialBackoff: 10 * time.Second, MaxBackoff: time.Minute}) {
		t.Errorf("RetryPolicyFor(cfg) = %+v", got)
	}
	bad := &config.Config{Sync: config.SyncConfig{Retry: config.SyncRetryConfig{InitialBackoff: "soon"}}}
	if got := RetryPolicyFor(bad); got.MaxRetries != 10 || got.InitialBackoff != time.Minute {
		t.Errorf("RetryPolicyFor(invalid) = %+v, want the defaults", got)
	}
}

// TestRetryPolicyNextState_StopsAfterMaxRetries: a persistently transient
// failure is scheduled for retry until MaxRetries automatic retries have
// failed, then is exhausted with no next retry.
func TestRetryPolicyNextState_StopsAfterMaxRetries(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, InitialBackoff: time.Minute, MaxBackoff: 30 * time.Minute}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	transient := &ConnectorError{ExitCode: 1, Code: "unavailable"}

	var st store.SyncRetry
	wantWait := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute}
	for attempt := 1; attempt <= 3; attempt++ { // the original failure + retries 1 and 2
		st = p.NextState(st, transient, now)
		if st.Attempts != attempt || st.State != store.SyncRetryRetrying || st.MaxRetries != 3 {
			t.Fatalf("attempt %d: state = %+v, want retrying", attempt, st)
		}
		if want := now.Add(wantWait[attempt-1]).Format(time.RFC3339); st.NextRetryAt != want {
			t.Fatalf("attempt %d: next_retry_at = %s, want %s", attempt, st.NextRetryAt, want)
		}
		if st.LastFailedAt != now.Format(time.RFC3339) {
			t.Fatalf("attempt %d: last_failed_at = %s", attempt, st.LastFailedAt)
		}
	}
	st = p.NextState(st, transient, now) // retry 3 of 3 failed
	if st.Attempts != 4 || st.Retries() != 3 || st.State != store.SyncRetryExhausted || st.NextRetryAt != "" {
		t.Fatalf("after the 3rd retry failed: state = %+v, want exhausted with no next retry", st)
	}
	st = p.NextState(st, transient, now) // a later (event-driven or forced) failure stays exhausted
	if st.State != store.SyncRetryExhausted {
		t.Fatalf("state = %+v, want still exhausted", st)
	}
}

func TestRetryPolicyNextState_NonTransientIsNeverScheduled(t *testing.T) {
	p := RetryPolicy{MaxRetries: 10, InitialBackoff: time.Minute, MaxBackoff: 30 * time.Minute}
	st := p.NextState(store.SyncRetry{}, &ConnectorError{ExitCode: 1, Code: "unauthenticated"}, time.Now())
	if st.State != store.SyncRetryNonTransient || st.NextRetryAt != "" || st.Attempts != 1 {
		t.Fatalf("state = %+v, want non-transient with no next retry", st)
	}
}

func TestRetryPolicyNextState_ZeroMaxRetriesDisablesRetry(t *testing.T) {
	p := RetryPolicy{MaxRetries: 0, InitialBackoff: time.Minute, MaxBackoff: 30 * time.Minute}
	st := p.NextState(store.SyncRetry{}, errors.New("boom"), time.Now())
	if st.State != store.SyncRetryExhausted {
		t.Fatalf("state = %+v, want exhausted on the first failure", st)
	}
}

func TestRetryDue(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	cases := []struct {
		name  string
		r     store.SyncRetry
		found bool
		want  bool
	}{
		{"no recorded state", store.SyncRetry{}, false, true},
		{"retrying, backoff elapsed", store.SyncRetry{State: store.SyncRetryRetrying, NextRetryAt: at(-time.Second)}, true, true},
		{"retrying, exactly due", store.SyncRetry{State: store.SyncRetryRetrying, NextRetryAt: at(0)}, true, true},
		{"retrying, still backing off", store.SyncRetry{State: store.SyncRetryRetrying, NextRetryAt: at(time.Minute)}, true, false},
		{"retrying, unparseable time", store.SyncRetry{State: store.SyncRetryRetrying, NextRetryAt: "soon"}, true, true},
		{"exhausted", store.SyncRetry{State: store.SyncRetryExhausted}, true, false},
		{"non-transient", store.SyncRetry{State: store.SyncRetryNonTransient}, true, false},
	}
	for _, c := range cases {
		if got := RetryDue(c.r, c.found, now); got != c.want {
			t.Errorf("%s: RetryDue = %v, want %v", c.name, got, c.want)
		}
	}
}
