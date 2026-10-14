//go:build contract

package sync

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Real-bd regression guard for the review-request reopen against a claim held
// by a dead review session (bead pg2-9rq3r).
//
// WHY THIS FILE EXISTS (global rule V-3): when the head of a PR advances, a
// completed review-pr bead is reopened in one `issue update --status open
// --clear-assignee ...`. bd >= 1.3.1 refuses to change the assignee of an
// in_progress bead another actor holds, EVEN AFTER the holder's lease expired
// ("cannot reassign <id>: held by ... (in_progress)"), so a review session that
// died without releasing its claim made every retry fail until a person
// released it, burning the whole retry budget and paging critical. Whether the
// refusal fires, what it says, and that a guarded write naming the observed
// holder passes are properties of the REAL tool, so these tests drive
// Syncer.Sync through the real pg-connector and pg-connector-issue-beads
// binaries against a real `bd` in a throwaway embedded store (same hermetic
// fixture as closure_realbd_contract_test.go: allowlisted environment, private
// HOME, no shared dolt server).
//
// Expiring a lease: bd offers no TTL knob and `bd sql` is unsupported on an
// embedded store, so the test moves time instead of waiting out the real TTL.
// The lease lives in the `leases` table of the store's private embedded dolt
// database; the test sets its lease_expires_at into the past with the `dolt`
// CLI, then reads it back through `bd show --json` to prove bd sees the lease
// as expired. Nothing outside the temp store is touched.
//
// Behind the `contract` build tag and wired into the commit-time and pre-land
// hooks by `pg-desk-closure-contract-test` in flake.nix. Run it directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run ReviewReopen_RealBD ./internal/sync/

const reviewReopenHolder = "dead-review-session-review"

// bdAs runs the real bd as actor in the throwaway workspace.
func (f *closureRealBD) bdAs(actor string, args ...string) string {
	f.t.Helper()
	return f.bd(append([]string{"--actor", actor}, args...)...)
}

// leaseExpiry is the lease_expires_at bd reports for id ("" when none).
func (f *closureRealBD) leaseExpiry(id string) time.Time {
	f.t.Helper()
	out := f.bd("show", id, "--json")
	_, rest, ok := strings.Cut(out, `"lease_expires_at": "`)
	if !ok {
		f.t.Fatalf("bd show %s --json carries no lease_expires_at:\n%s", id, out)
	}
	raw, _, _ := strings.Cut(rest, `"`)
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		f.t.Fatalf("lease_expires_at %q of %s: %v", raw, id, err)
	}
	return at
}

// assignee is the bead's assignee as the real bd reports it ("" when none).
func (f *closureRealBD) assignee(id string) string {
	f.t.Helper()
	out := f.bd("show", id, "--json")
	_, rest, ok := strings.Cut(out, `"assignee": "`)
	if !ok {
		return ""
	}
	who, _, _ := strings.Cut(rest, `"`)
	return who
}

// expireLease moves id's claim lease into the past, in the store's private
// embedded dolt database, and proves through bd itself that it now reads as
// expired.
func (f *closureRealBD) expireLease(id string) {
	f.t.Helper()
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		f.t.Skip("dolt not on PATH: cannot expire a lease in the embedded store")
	}
	dbs, err := filepath.Glob(filepath.Join(f.dir, ".beads", "embeddeddolt", "*", ".dolt"))
	if err != nil || len(dbs) != 1 {
		f.t.Fatalf("want exactly one embedded dolt database under %s, got %v (err %v)", f.dir, dbs, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), closureRealBDDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, doltPath, "sql", "-q",
		`UPDATE leases SET lease_expires_at = '2020-01-01 00:00:00' WHERE issue_id = '`+id+`'`)
	cmd.Dir = filepath.Dir(dbs[0])
	cmd.Env = f.env
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("expire lease of %s: %v\n%s", id, err, out)
	}
	if at := f.leaseExpiry(id); !at.Before(time.Now()) {
		f.t.Fatalf("bd still reports the lease of %s as live (%s) after expiring it", id, at)
	}
}

// reviewReopenScene is a merge-request anchor with a review-pr child that a
// review session claimed and then died holding, plus the ledger rows of a
// prior sync run in which the PR's head has since advanced and settled.
type reviewReopenScene struct {
	f              *closureRealBD
	s              *Syncer
	anchor, review string
}

func newReviewReopenScene(t *testing.T) *reviewReopenScene {
	t.Helper()
	f := newClosureRealBD(t)
	anchor := f.create("anchor", "merge-request", "")
	review := f.create("review-pr: "+fixturePRKey, "task", anchor)
	f.bdAs(reviewReopenHolder, "update", review, "--claim")
	if st, who := f.status(review), f.assignee(review); st != "in_progress" || who != reviewReopenHolder {
		t.Fatalf("setup: review %s is %q held by %q, want in_progress held by %q", review, st, who, reviewReopenHolder)
	}

	s := f.syncer()
	seedRealBDLedger(t, s, anchor, "")
	if err := s.store.UpsertLedger(store.LedgerEntry{
		Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: KindReviewRequest,
		BeadID: review, LastSyncedContentHash: "old-hash", LastSyncedAt: "2026-09-16T00:00:00Z",
		LastReviewedHeadSHA: "old-sha",
		// new-sha was first seen a day before the fixed clock: its settle
		// window has elapsed, so this run reopens.
		FirstSeenHeadSHA: "new-sha", FirstSeenHeadAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed review ledger: %v", err)
	}
	return &reviewReopenScene{f: f, s: s, anchor: anchor, review: review}
}

// sync runs one pg-desk sync of the PR at its advanced head.
func (sc *reviewReopenScene) sync(t *testing.T) error {
	t.Helper()
	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: "new-sha"}
	return sc.s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeChanged, facts, interpFor("mine", nil))
}

// reviewUpdates is the `issue update` calls pg-desk issued for the review bead.
func (sc *reviewReopenScene) reviewUpdates() [][]string {
	var out [][]string
	for _, a := range sc.f.calls {
		if len(a) >= 3 && a[0] == "issue" && a[1] == "update" && a[2] == sc.review {
			out = append(out, a)
		}
	}
	return out
}

// TestReviewReopen_RealBD_ExpiredLeaseHolder_ReopensInOneRun: the holder's
// session died and its lease expired. One sync run must reopen the bead with
// the dead claim released, with no retry and no sync_error.
func TestReviewReopen_RealBD_ExpiredLeaseHolder_ReopensInOneRun(t *testing.T) {
	sc := newReviewReopenScene(t)
	sc.f.expireLease(sc.review)

	if err := sc.sync(t); err != nil {
		t.Fatalf("Sync against a review bead held by an expired lease: %v\ncalls: %v", err, sc.f.calls)
	}
	if got := len(sc.reviewUpdates()); got != 1 {
		t.Fatalf("review bead updates = %d (%v), want exactly one", got, sc.reviewUpdates())
	}
	if st := sc.f.status(sc.review); st != "open" {
		t.Fatalf("review bead status = %q, want open", st)
	}
	if who := sc.f.assignee(sc.review); who != "" {
		t.Fatalf("review bead still assigned to %q, want the dead claim released", who)
	}
	led, _, _ := sc.s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
	if led.LastReviewedHeadSHA != "new-sha" {
		t.Fatalf("ledger last requested head = %q, want new-sha (the reopen was recorded)", led.LastReviewedHeadSHA)
	}
}

// TestReviewReopen_RealBD_LiveLeaseHolder_LeftUntouchedAndNonTransient: the
// holder's lease is live, so it may be working. pg-desk must not take the
// claim, and the refusal must be classified non-transient (no automatic
// retry, no retry-budget burn).
func TestReviewReopen_RealBD_LiveLeaseHolder_LeftUntouchedAndNonTransient(t *testing.T) {
	sc := newReviewReopenScene(t)
	if at := sc.f.leaseExpiry(sc.review); !at.After(time.Now()) {
		t.Fatalf("setup: lease of %s already expired (%s)", sc.review, at)
	}

	err := sc.sync(t)
	if err == nil {
		t.Fatalf("Sync succeeded although a live holder owns %s; calls: %v", sc.review, sc.f.calls)
	}
	var ce *ConnectorError
	if !errors.As(err, &ce) {
		t.Fatalf("error chain carries no *ConnectorError: %#v", err)
	}
	if !strings.Contains(ce.Detail, "cannot reassign "+sc.review) || !strings.Contains(ce.Detail, "held by") {
		t.Fatalf("connector detail = %q, want real bd's held-claim refusal for %s", ce.Detail, sc.review)
	}
	if got := Classify(err); got != ClassNonTransient {
		t.Fatalf("Classify(real bd held-claim refusal) = %q, want non-transient (detail %q)", got, ce.Detail)
	}
	policy := RetryPolicy{MaxRetries: 10, InitialBackoff: time.Minute, MaxBackoff: 30 * time.Minute}
	next := policy.NextState(store.SyncRetry{}, err, time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	if next.State != store.SyncRetryNonTransient || next.NextRetryAt != "" || next.Attempts != 1 {
		t.Fatalf("retry state = %+v, want non-transient after the first attempt with no retry scheduled", next)
	}

	if st, who := sc.f.status(sc.review), sc.f.assignee(sc.review); st != "in_progress" || who != reviewReopenHolder {
		t.Fatalf("live claim was disturbed: review is %q held by %q, want in_progress held by %q", st, who, reviewReopenHolder)
	}
	led, _, _ := sc.s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
	if led.LastReviewedHeadSHA != "old-sha" {
		t.Fatalf("ledger last requested head = %q, want old-sha (no reopen happened)", led.LastReviewedHeadSHA)
	}
}

// TestReviewReopen_RealBD_LeaseExpiresLater_ReopensOnNextRun: the same bead,
// first refused while the lease is live, is reopened by the next run once the
// lease has expired, so a person never has to release it.
func TestReviewReopen_RealBD_LeaseExpiresLater_ReopensOnNextRun(t *testing.T) {
	sc := newReviewReopenScene(t)
	if err := sc.sync(t); err == nil {
		t.Fatalf("first run succeeded under a live lease; calls: %v", sc.f.calls)
	}
	sc.f.expireLease(sc.review)
	sc.f.calls = nil
	if err := sc.sync(t); err != nil {
		t.Fatalf("second run after the lease expired: %v\ncalls: %v", err, sc.f.calls)
	}
	if st, who := sc.f.status(sc.review), sc.f.assignee(sc.review); st != "open" || who != "" {
		t.Fatalf("review is %q held by %q, want open and unassigned", st, who)
	}
}
