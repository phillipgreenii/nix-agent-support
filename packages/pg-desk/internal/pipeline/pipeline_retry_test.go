package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
)

// Automatic retry of a recorded sync_error (bead pg2-xb6fs): the pipeline
// counts each failed attempt, reconcile re-drives only rows whose retry is
// due, a transient failure heals on a later scheduled pass with no operator
// action, and a persistent one stops after the retry bound.

// openPRGatherer answers every read with an open PR (RemovedState "open" on
// a --change removed re-read), counting reads.
func openPRGatherer(t *testing.T, reads *int) gatherFunc {
	return func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		*reads++
		facts := minimalFacts(t, map[string]any{"author": "me", "title": "x", "state": "open", "repo": "acme/widgets", "number": 7})
		if change == gather.ChangeRemoved {
			facts.RemovedState = "open"
		}
		return facts, nil
	}
}

// unavailableErr is the error the real sync stage returns when the beads
// workspace is on an unmounted volume: pg-connector's wire code
// "unavailable" carrying bd's chdir failure.
func unavailableErr(dir string) error {
	return &sync.ConnectorError{
		Args: []string{"issue", "create", "--title", "acme/widgets#7: x"}, ExitCode: 1, Code: "unavailable",
		Detail: "unavailable: pg-connector-issue-beads: chdir " + dir + ": no such file or directory",
	}
}

func syncRetryState(t *testing.T, p *Pipeline, id string) (store.SyncRetry, bool) {
	t.Helper()
	r, found, err := p.store.GetSyncRetry(id)
	if err != nil {
		t.Fatalf("GetSyncRetry(%s): %v", id, err)
	}
	return r, found
}

func syncErrorOf(t *testing.T, p *Pipeline, id string) string {
	t.Helper()
	i, _, err := p.store.GetInterpretation("acme/widgets", "pr", id)
	if err != nil {
		t.Fatalf("GetInterpretation(%s): %v", id, err)
	}
	return i.SyncError
}

// TestPipelineRetry_TransientMissingPathSelfHeals is the bead's acceptance
// test: the beads workspace path is missing (an unmounted volume), so the
// anchor create fails; scheduled Reconcile passes retry it on the backoff
// schedule while the path stays missing, and once the path appears the next
// due pass heals the row — sync_error and retry state both cleared — with no
// operator action beyond the scheduler invoking reconcile.
func TestPipelineRetry_TransientMissingPathSelfHeals(t *testing.T) {
	clk := &steppingClock{now: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)}
	reads := 0
	p := newTestPipeline(t, openPRGatherer(t, &reads), nil)
	p.clock = clk

	beadsDir := filepath.Join(t.TempDir(), "Volumes", "gitrepos", ".beads")
	syncCalls := 0
	p.syncer = syncerFunc(func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
		syncCalls++
		if _, err := os.Stat(beadsDir); err != nil {
			return unavailableErr(beadsDir)
		}
		return nil
	})

	// The event-driven run fails: sync_error recorded, first retry in 1m.
	if err := p.Run(context.Background(), "pr", "7", gather.ChangeAdded); err == nil {
		t.Fatal("Run: want the anchor-create failure")
	}
	if syncErrorOf(t, p, "7") == "" {
		t.Fatal("sync_error not recorded")
	}
	r, found := syncRetryState(t, p, "7")
	if !found || r.State != store.SyncRetryRetrying || r.Attempts != 1 || r.MaxRetries != 10 ||
		r.NextRetryAt != clk.now.Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("retry state after the first failure = %+v (found=%v), want retrying, attempt 1, next retry in 1m", r, found)
	}

	// A pass before the backoff elapsed holds the row.
	if err := p.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile (backing off): %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("a pass inside the backoff re-drove the row (sync calls = %d)", syncCalls)
	}

	// Due, but the path is still missing: one more failed attempt, next in 2m.
	clk.now = clk.now.Add(time.Minute)
	if err := p.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile: want the still-failing retry to surface as an error")
	}
	r, _ = syncRetryState(t, p, "7")
	if syncCalls != 2 || r.State != store.SyncRetryRetrying || r.Attempts != 2 ||
		r.NextRetryAt != clk.now.Add(2*time.Minute).Format(time.RFC3339) {
		t.Fatalf("after one failed retry: sync calls = %d, state = %+v; want 2 calls, retrying, attempt 2, next retry in 2m", syncCalls, r)
	}

	// The volume comes back; the next due pass heals the row.
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	clk.now = clk.now.Add(2 * time.Minute)
	if err := p.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after the path appeared: %v", err)
	}
	if syncCalls != 3 {
		t.Fatalf("sync calls = %d, want 3", syncCalls)
	}
	if got := syncErrorOf(t, p, "7"); got != "" {
		t.Fatalf("sync_error = %q after the heal, want cleared", got)
	}
	if r, found := syncRetryState(t, p, "7"); found {
		t.Fatalf("retry state %+v survived the heal, want deleted", r)
	}

	// Healed rows are no longer candidates.
	clk.now = clk.now.Add(time.Hour)
	if err := p.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after the heal: %v", err)
	}
	if syncCalls != 3 {
		t.Fatalf("a healed row was re-driven (sync calls = %d)", syncCalls)
	}
}

// TestPipelineRetry_PersistentFailureStopsAfterMaxRetries: a transient
// failure that never clears is retried exactly MaxRetries times by
// scheduled passes, then is exhausted and held — including through its open
// anchor, which is not re-read either — until an operator's --retry-all.
func TestPipelineRetry_PersistentFailureStopsAfterMaxRetries(t *testing.T) {
	clk := &steppingClock{now: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)}
	reads := 0
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		reads++
		facts := minimalFacts(t, map[string]any{"author": "me", "title": "x", "state": "closed", "merged": true, "repo": "acme/widgets", "number": 7})
		if change == gather.ChangeRemoved {
			facts.RemovedState = "merged" // the open anchor would be a closure candidate too
		}
		return facts, nil
	}), nil)
	p.clock = clk
	p.retryPolicy = sync.RetryPolicy{MaxRetries: 3, InitialBackoff: time.Minute, MaxBackoff: 30 * time.Minute}
	seedReconcileEntity(t, p, "7", "abc123") // open anchor in the ledger

	syncCalls := 0
	p.syncer = syncerFunc(func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
		syncCalls++
		return errors.New("sync: close anchor bead-7: connection reset by peer")
	})

	if err := p.Run(context.Background(), "pr", "7", gather.ChangeRemoved); err == nil {
		t.Fatal("Run: want the closure failure")
	}
	for pass := 0; pass < 10; pass++ { // far more passes than the bound
		clk.now = clk.now.Add(time.Hour)
		_ = p.Reconcile(context.Background())
	}
	if syncCalls != 4 { // the original failure + 3 automatic retries
		t.Fatalf("sync calls = %d, want 4 (1 original + MaxRetries 3)", syncCalls)
	}
	r, _ := syncRetryState(t, p, "7")
	if r.State != store.SyncRetryExhausted || r.Attempts != 4 || r.Retries() != 3 || r.NextRetryAt != "" {
		t.Fatalf("retry state = %+v, want exhausted after 3 retries with no next retry", r)
	}
	if syncErrorOf(t, p, "7") == "" {
		t.Fatal("an exhausted row must stay a sync_error")
	}
	readsWhenExhausted := reads
	clk.now = clk.now.Add(time.Hour)
	if err := p.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile of a held row: %v", err)
	}
	if reads != readsWhenExhausted || syncCalls != 4 {
		t.Fatalf("an exhausted row was re-read (%d -> %d) or re-synced (%d)", readsWhenExhausted, reads, syncCalls)
	}

	// The operator's manual repair re-drives it regardless of the bound.
	p.retryAll = true
	_ = p.Reconcile(context.Background())
	if syncCalls != 5 {
		t.Fatalf("--retry-all: sync calls = %d, want 5", syncCalls)
	}
}

// TestPipelineRetry_NonTransientIsNeverRetriedAutomatically: an
// authentication failure needs a person, so scheduled passes never re-drive
// it; once the cause is fixed, --retry-all heals it.
func TestPipelineRetry_NonTransientIsNeverRetriedAutomatically(t *testing.T) {
	clk := &steppingClock{now: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)}
	reads := 0
	p := newTestPipeline(t, openPRGatherer(t, &reads), nil)
	p.clock = clk
	authFixed := false
	syncCalls := 0
	p.syncer = syncerFunc(func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
		syncCalls++
		if authFixed {
			return nil
		}
		return &sync.ConnectorError{Args: []string{"issue", "create"}, ExitCode: 1, Code: "unauthenticated", Detail: "unauthenticated: token expired"}
	})

	if err := p.Run(context.Background(), "pr", "7", gather.ChangeAdded); err == nil {
		t.Fatal("Run: want the auth failure")
	}
	r, _ := syncRetryState(t, p, "7")
	if r.State != store.SyncRetryNonTransient || r.NextRetryAt != "" {
		t.Fatalf("retry state = %+v, want non-transient with no next retry", r)
	}
	for pass := 0; pass < 5; pass++ {
		clk.now = clk.now.Add(time.Hour)
		if err := p.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if syncCalls != 1 {
		t.Fatalf("a non-transient row was retried automatically (sync calls = %d)", syncCalls)
	}

	authFixed = true
	p.retryAll = true
	if err := p.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile --retry-all: %v", err)
	}
	if syncCalls != 2 || syncErrorOf(t, p, "7") != "" {
		t.Fatalf("--retry-all did not heal the row: sync calls = %d, sync_error = %q", syncCalls, syncErrorOf(t, p, "7"))
	}
	if _, found := syncRetryState(t, p, "7"); found {
		t.Fatal("retry state survived the heal")
	}
}

// TestPipelineRetry_EarlierStageFailureCountsAsAttempt: a retry that fails
// before reaching sync (the PR re-read fails) still counts against the
// bound, so a row cannot be re-driven forever through a different failure.
func TestPipelineRetry_EarlierStageFailureCountsAsAttempt(t *testing.T) {
	clk := &steppingClock{now: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)}
	gatherFails := false
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		if gatherFails {
			return gather.Facts{}, errors.New("pg-connector [pr show]: exit 1: unavailable: network is unreachable")
		}
		return minimalFacts(t, map[string]any{"author": "me", "title": "x", "state": "open", "repo": "acme/widgets", "number": 7}), nil
	}), nil)
	p.clock = clk
	p.syncer = syncerFunc(func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
		return errors.New("sync: create anchor: boom")
	})

	if err := p.Run(context.Background(), "pr", "7", gather.ChangeAdded); err == nil {
		t.Fatal("Run: want the sync failure")
	}
	gatherFails = true
	clk.now = clk.now.Add(time.Hour)
	if err := p.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile: want the failed re-read to surface")
	}
	r, _ := syncRetryState(t, p, "7")
	if r.Attempts != 2 || r.State != store.SyncRetryRetrying {
		t.Fatalf("retry state = %+v, want attempt 2 (the failed re-read counted), still retrying", r)
	}
	if syncErrorOf(t, p, "7") == "" {
		t.Fatal("the recorded sync_error must survive a re-read failure")
	}
}

// TestPipelineRetry_FailureWithoutSyncErrorRecordsNothing: an ordinary
// failure of an entity with no recorded sync_error creates no retry state.
func TestPipelineRetry_FailureWithoutSyncErrorRecordsNothing(t *testing.T) {
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{}, errors.New("boom")
	}), nil)
	if err := p.Run(context.Background(), "pr", "7", gather.ChangeAdded); err == nil {
		t.Fatal("Run: want the gather failure")
	}
	if r, found := syncRetryState(t, p, "7"); found {
		t.Fatalf("retry state %+v recorded for an entity with no sync_error", r)
	}
}

// TestNewUsesConfiguredRetryPolicy: New resolves sync.retry from config.
func TestNewUsesConfiguredRetryPolicy(t *testing.T) {
	two := 2
	cfg := testCfg()
	cfg.Sync.Retry = config.SyncRetryConfig{MaxRetries: &two, InitialBackoff: "30s", MaxBackoff: "5m"}
	p := New(cfg, store.OpenForTest(t))
	if want := (sync.RetryPolicy{MaxRetries: 2, InitialBackoff: 30 * time.Second, MaxBackoff: 5 * time.Minute}); p.retryPolicy != want {
		t.Fatalf("retryPolicy = %+v, want %+v", p.retryPolicy, want)
	}
}
