package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Review settle window (bead pg2-a9yhn): a head that differs from the last
// requested one is reopened only after it has been the PR's head for
// sync.review_settle_window.

var settleT0 = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

// settleSyncer builds a Syncer over a shared store whose clock reads at, with
// the given settle window ("" = the default).
func settleSyncer(st *store.Store, mode, window string, at time.Time) *Syncer {
	cfg := testConfig(mode)
	cfg.Sync.ReviewSettleWindow = window
	return New(cfg, st, WithClock(interpret.FixedClock(at)))
}

// seedReview seeds a review-request ledger row whose last request was for
// lastSHA, with no pending head.
func seedReview(t *testing.T, st *store.Store, lastSHA string) {
	t.Helper()
	if err := st.UpsertLedger(store.LedgerEntry{
		Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: KindReviewRequest,
		BeadID: "bd-review-s", LastSyncedContentHash: "old-hash", LastSyncedAt: "2026-09-16T00:00:00Z",
		LastReviewedHeadSHA: lastSHA,
	}); err != nil {
		t.Fatalf("seed review ledger: %v", err)
	}
}

// syncHead runs one Sync of the PR at head, with the clock at the given time.
func syncHead(t *testing.T, st *store.Store, mode, window string, at time.Time, head string) {
	t.Helper()
	s := settleSyncer(st, mode, window, at)
	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: head}
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeChanged, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync(head=%s at %s): %v", head, at.Format(time.RFC3339), err)
	}
}

// reviewReopens returns the `issue update` calls that reopened the review bead.
func reviewReopens(t *testing.T, recordFile string) []string {
	t.Helper()
	var out []string
	for _, r := range readCallRecords(t, recordFile) {
		joined := strings.Join(r.Args, " ")
		if r.verb() == "issue update" && strings.Contains(joined, "bd-review-s") && strings.Contains(joined, "--status open") {
			out = append(out, joined)
		}
	}
	return out
}

func reviewLedger(t *testing.T, st *store.Store) store.LedgerEntry {
	t.Helper()
	l, found, err := st.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
	if err != nil || !found {
		t.Fatalf("GetLedger review-request: found=%v err=%v", found, err)
	}
	return l
}

// TestSettle_BurstOfThreePushesProducesOneReview: three pushes inside the
// window yield exactly one reopen, for the head the burst ended on.
func TestSettle_BurstOfThreePushesProducesOneReview(t *testing.T) {
	recordFile := withFactory(t)
	st := store.OpenForTest(t)
	seedReview(t, st, "sha-0")

	// Three pushes 30s apart (push 1 is seen at t0, push 3 at t0+60s).
	syncHead(t, st, ModeApply, "", settleT0, "sha-1")
	syncHead(t, st, ModeApply, "", settleT0.Add(30*time.Second), "sha-2")
	syncHead(t, st, ModeApply, "", settleT0.Add(60*time.Second), "sha-3")
	if got := reviewReopens(t, recordFile); len(got) != 0 {
		t.Fatalf("a burst inside the window reopened the review %d time(s): %v", len(got), got)
	}
	led := reviewLedger(t, st)
	if led.LastReviewedHeadSHA != "sha-0" || led.FirstSeenHeadSHA != "sha-3" || led.FirstSeenHeadAt != "2026-09-17T12:01:00Z" {
		t.Fatalf("pending state after the burst = %+v, want last=sha-0 pending=sha-3@12:01:00", led)
	}

	// Still inside the window of the LAST push (sha-3 seen at 12:01:00, window 2m).
	syncHead(t, st, ModeApply, "", settleT0.Add(2*time.Minute+59*time.Second), "sha-3")
	if got := reviewReopens(t, recordFile); len(got) != 0 {
		t.Fatalf("reopened before the last push settled: %v", got)
	}

	// The head has now been sha-3 for the full window.
	syncHead(t, st, ModeApply, "", settleT0.Add(3*time.Minute), "sha-3")
	got := reviewReopens(t, recordFile)
	if len(got) != 1 || !strings.Contains(got[0], "head_sha=sha-3") {
		t.Fatalf("settled head: reopen calls = %v, want exactly one, for sha-3", got)
	}
	led = reviewLedger(t, st)
	if led.LastReviewedHeadSHA != "sha-3" || led.FirstSeenHeadSHA != "" || led.FirstSeenHeadAt != "" {
		t.Fatalf("ledger after the reopen = %+v, want last=sha-3 and no pending head", led)
	}

	// A later run at the same head is a no-op.
	syncHead(t, st, ModeApply, "", settleT0.Add(10*time.Minute), "sha-3")
	if got := reviewReopens(t, recordFile); len(got) != 1 {
		t.Fatalf("a settled head was reopened again: %v", got)
	}
}

// TestSettle_StillSettlingRunWritesNothing: a run inside the window neither
// calls pg-connector for the review bead nor rewrites the ledger row.
func TestSettle_StillSettlingRunWritesNothing(t *testing.T) {
	withFactory(t)
	st := store.OpenForTest(t)
	seedReview(t, st, "sha-0")
	syncHead(t, st, ModeApply, "", settleT0, "sha-1")
	before := reviewLedger(t, st)
	syncHead(t, st, ModeApply, "", settleT0.Add(time.Minute), "sha-1")
	if after := reviewLedger(t, st); after != before {
		t.Fatalf("a still-settling run rewrote the ledger row: before %+v after %+v", before, after)
	}
}

// TestSettle_ZeroWindowReopensAtOnce: sync.review_settle_window: 0 restores
// the pre-pg2-a9yhn behavior.
func TestSettle_ZeroWindowReopensAtOnce(t *testing.T) {
	recordFile := withFactory(t)
	st := store.OpenForTest(t)
	seedReview(t, st, "sha-0")
	syncHead(t, st, ModeApply, "0", settleT0, "sha-1")
	got := reviewReopens(t, recordFile)
	if len(got) != 1 || !strings.Contains(got[0], "head_sha=sha-1") {
		t.Fatalf("zero window: reopen calls = %v, want one for sha-1", got)
	}
	if led := reviewLedger(t, st); led.LastReviewedHeadSHA != "sha-1" || led.FirstSeenHeadSHA != "" {
		t.Fatalf("zero window ledger = %+v", led)
	}
}

// TestSettle_PushBackToRequestedHeadForgetsPendingHead: a force-push back to
// the head already requested drops the pending head, so a later reappearance
// of the abandoned SHA starts a fresh timer instead of inheriting the old one.
func TestSettle_PushBackToRequestedHeadForgetsPendingHead(t *testing.T) {
	recordFile := withFactory(t)
	st := store.OpenForTest(t)
	seedReview(t, st, "sha-0")
	syncHead(t, st, ModeApply, "", settleT0, "sha-1")
	syncHead(t, st, ModeApply, "", settleT0.Add(time.Minute), "sha-0") // back to the requested head
	if led := reviewLedger(t, st); led.FirstSeenHeadSHA != "" || led.FirstSeenHeadAt != "" {
		t.Fatalf("pending head survived a push back to the requested head: %+v", led)
	}
	// sha-1 reappears long after its first sighting: it must NOT count as settled.
	syncHead(t, st, ModeApply, "", settleT0.Add(time.Hour), "sha-1")
	if got := reviewReopens(t, recordFile); len(got) != 0 {
		t.Fatalf("a reappearing head inherited a stale timer and reopened: %v", got)
	}
	if led := reviewLedger(t, st); led.FirstSeenHeadSHA != "sha-1" || led.FirstSeenHeadAt != "2026-09-17T13:00:00Z" {
		t.Fatalf("fresh timer = %+v, want pending sha-1@13:00:00", led)
	}
}

// TestSettle_PlanModeRecordsPendingAndWritesNothing: plan mode runs the same
// settle logic (so plan and apply agree on when a reopen happens) and calls no
// pg-connector write.
func TestSettle_PlanModeRecordsPendingAndWritesNothing(t *testing.T) {
	recordFile := withFactory(t)
	st := store.OpenForTest(t)
	seedReview(t, st, "sha-0")
	syncHead(t, st, ModePlan, "", settleT0, "sha-1")
	if led := reviewLedger(t, st); led.FirstSeenHeadSHA != "sha-1" || led.LastReviewedHeadSHA != "sha-0" {
		t.Fatalf("plan mode ledger = %+v, want pending sha-1, last sha-0", led)
	}
	syncHead(t, st, ModePlan, "", settleT0.Add(5*time.Minute), "sha-1")
	if led := reviewLedger(t, st); led.LastReviewedHeadSHA != "sha-1" || led.FirstSeenHeadSHA != "" {
		t.Fatalf("plan mode did not advance after the window: %+v", led)
	}
	if recs := readCallRecords(t, recordFile); len(recs) != 0 {
		t.Fatalf("plan mode invoked pg-connector: %+v", recs)
	}
}

// TestSettle_UnreadableFirstSeenRestartsTimer: a pending head with an
// unparseable timestamp is treated as unseen (timer restarts), never as
// settled.
func TestSettle_UnreadableFirstSeenRestartsTimer(t *testing.T) {
	recordFile := withFactory(t)
	st := store.OpenForTest(t)
	if err := st.UpsertLedger(store.LedgerEntry{
		Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: KindReviewRequest,
		BeadID: "bd-review-s", LastReviewedHeadSHA: "sha-0",
		FirstSeenHeadSHA: "sha-1", FirstSeenHeadAt: "garbage",
	}); err != nil {
		t.Fatal(err)
	}
	syncHead(t, st, ModeApply, "", settleT0, "sha-1")
	if got := reviewReopens(t, recordFile); len(got) != 0 {
		t.Fatalf("unreadable first-seen time counted as settled: %v", got)
	}
	if led := reviewLedger(t, st); led.FirstSeenHeadAt != "2026-09-17T12:00:00Z" {
		t.Fatalf("timer not restarted: %+v", led)
	}
}

// TestLedgerMeansLastRequestedHead pins the decision on bead pg2-a9yhn item 2:
// last_reviewed_head_sha is the head of the last REQUEST, so a request that was
// declined (its bead closed with no review) is not re-requested for the same
// head, however long ago, and a NEW head is requested once it settles.
func TestLedgerMeansLastRequestedHead(t *testing.T) {
	recordFile := withFactory(t)
	st := store.OpenForTest(t)
	seedReview(t, st, "sha-0")

	// The bead for sha-0 is closed (declined); the head is unchanged for days.
	for _, at := range []time.Time{settleT0, settleT0.Add(24 * time.Hour), settleT0.Add(72 * time.Hour)} {
		syncHead(t, st, ModeApply, "", at, "sha-0")
	}
	if got := reviewReopens(t, recordFile); len(got) != 0 {
		t.Fatalf("a request for an unchanged head was re-issued: %v", got)
	}

	// A new head is requested after it settles.
	syncHead(t, st, ModeApply, "", settleT0.Add(73*time.Hour), "sha-1")
	syncHead(t, st, ModeApply, "", settleT0.Add(73*time.Hour+2*time.Minute), "sha-1")
	if got := reviewReopens(t, recordFile); len(got) != 1 {
		t.Fatalf("new head: reopen calls = %v, want one", got)
	}
}

func TestSettleDue(t *testing.T) {
	window := 2 * time.Minute
	row := func(mut func(*store.LedgerEntry)) store.LedgerEntry {
		l := store.LedgerEntry{
			Kind: KindReviewRequest, BeadID: "bd-1",
			FirstSeenHeadSHA: "sha-1", FirstSeenHeadAt: settleT0.Format(rfc3339),
		}
		mut(&l)
		return l
	}
	for name, tc := range map[string]struct {
		l      store.LedgerEntry
		window time.Duration
		now    time.Time
		want   bool
	}{
		"elapsed":            {row(func(*store.LedgerEntry) {}), window, settleT0.Add(2 * time.Minute), true},
		"not yet":            {row(func(*store.LedgerEntry) {}), window, settleT0.Add(119 * time.Second), false},
		"no pending head":    {row(func(l *store.LedgerEntry) { l.FirstSeenHeadSHA, l.FirstSeenHeadAt = "", "" }), window, settleT0.Add(time.Hour), false},
		"zero window":        {row(func(*store.LedgerEntry) {}), 0, settleT0.Add(time.Hour), false},
		"unparseable time":   {row(func(l *store.LedgerEntry) { l.FirstSeenHeadAt = "x" }), window, settleT0.Add(time.Hour), false},
		"planned row":        {row(func(l *store.LedgerEntry) { l.BeadID = "" }), window, settleT0.Add(time.Hour), false},
		"other kind of row":  {row(func(l *store.LedgerEntry) { l.Kind = KindAnchor }), window, settleT0.Add(time.Hour), false},
		"feedback cycle row": {row(func(l *store.LedgerEntry) { l.Kind = KindFeedbackCycle }), window, settleT0.Add(time.Hour), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := SettleDue(tc.l, tc.window, tc.now); got != tc.want {
				t.Fatalf("SettleDue = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDefaultConfigSettleWindow: the Syncer picks up the documented default
// when sync.review_settle_window is unset.
func TestDefaultConfigSettleWindow(t *testing.T) {
	cfg := testConfig(ModeApply)
	if got := cfg.ReviewSettleWindow(); got != config.DefaultReviewSettleWindow {
		t.Fatalf("default window = %s, want 2m", got)
	}
}
