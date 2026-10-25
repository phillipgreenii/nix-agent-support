package detail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
)

// seeded is a harness whose issue A-1 is watched, stored (summary and detail)
// at t0 and drained, with the origin serving it.
func seeded(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.fake.set(fakeIssue{Key: issueKey, Category: openCat, Updated: updated1})
	h.watch(issueKey)
	h.persist(summaryOf(issueKey, openCat, "title", updated1))
	h.drain()
	if h.fake.count() != 1 {
		t.Fatalf("seeding made %d pjira calls, want 1", h.fake.count())
	}
	return h
}

func TestAFirstSightingQueuesOneDetailBaselineFetchAndAppendsNoRow(t *testing.T) {
	h := newHarness(t)
	h.fake.set(fakeIssue{Key: issueKey, Category: openCat, Updated: updated1})
	h.watch(issueKey)

	st := h.persist(summaryOf(issueKey, openCat, "title", updated1))
	if !st.First {
		t.Fatal("not a first sighting")
	}
	if c, ok := h.queuedDetail(issueKey); !ok || c != governor.ClassKeepAlive {
		t.Fatalf("detail queued = %v class %v, want a class 4 background task", ok, c)
	}
	if _, ok := h.detailRow(issueKey); ok || h.fake.count() != 0 {
		t.Fatalf("Revalidate itself fetched: row %v, %d calls", ok, h.fake.count())
	}

	h.drain()
	if h.fake.count() != 1 {
		t.Fatalf("pjira calls = %d, want one detail fetch", h.fake.count())
	}
	if _, ok := h.detailRow(issueKey); !ok {
		t.Error("no detail row after the baseline fetch")
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("the baseline appended rows: %+v", rows)
	}
	if h.pending() != 0 {
		t.Error("a pending row survived the baseline")
	}
}

func TestAFirstSightingOfAnIssueNobodyKeepsAliveQueuesNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// An entity closed long ago, seen again with no stored summary: the
	// keep-alive rule (tracked until closed for terminal_ttl) says no.
	if err := h.st.EnsureEntity(ctx, groups.EntityKind, issueKey); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetClosedAt(ctx, groups.EntityKind, issueKey, t0); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(48 * time.Hour)
	if st := h.persist(summaryOf(issueKey, closedCat, "done", updated1)); !st.First {
		t.Fatal("not a first sighting")
	}
	if c, ok := h.queuedDetail(issueKey); ok {
		t.Errorf("a detail task was queued at class %v for an issue past terminal_ttl", c)
	}
}

func TestAChangedUpdatedTokenQueuesADetailRefreshAtClass2AndAppendsNoRow(t *testing.T) {
	h := seeded(t)
	h.clk.Advance(time.Minute)
	before, _ := h.detailRow(issueKey)

	h.persist(summaryOf(issueKey, openCat, "title", updated2)) // Updated alone differs
	if c, ok := h.queuedDetail(issueKey); !ok || c != governor.ClassInvalidated {
		t.Fatalf("detail queued = %v class %v, want class 2", ok, c)
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Fatalf("a change to Updated alone appended rows: %+v", rows)
	}

	h.drain()
	if h.fake.count() != 2 {
		t.Fatalf("pjira calls = %d, want 2 (baseline plus the refresh)", h.fake.count())
	}
	after, _ := h.detailRow(issueKey)
	if after.Generation <= before.Generation || !after.FetchedAt.Equal(h.clk.Now()) {
		t.Errorf("detail row not re-pulled: before %+v after %+v", before, after)
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("the refresh appended rows: %+v", rows)
	}
	if h.pending() != 0 {
		t.Error("the detail refresh did not settle the pending row")
	}

	// Control: the harness does append a row for a real field change, so the
	// absence above is a result and not a blind spot.
	h.persist(summaryOf(issueKey, openCat, "a new title", "2026-10-05T10:11:14.000+0000"))
	h.drain() // the class 2 detail refresh settles the pending row
	rows := h.changeRows()
	if len(rows) != 1 || len(rows[0].Kinds) != 1 || rows[0].Kinds[0] != classify.KindTitleChanged {
		t.Errorf("rows after a title change = %+v, want one title_changed row: the watched-issue control is blind", rows)
	}
}

func TestAnUnchangedTokenExtendsFreshUntilButNeverPastMaxAgeAt(t *testing.T) {
	h := seeded(t) // detail fetched at t0: fresh until +10m, max age at +30m
	row0, _ := h.detailRow(issueKey)
	if !row0.FreshUntil.Equal(t0.Add(10*time.Minute)) || !row0.MaxAgeAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("seed schedule = %v / %v", row0.FreshUntil, row0.MaxAgeAt)
	}

	h.clk.Advance(5 * time.Minute)
	h.persist(summaryOf(issueKey, openCat, "title", updated1)) // same token
	row, _ := h.detailRow(issueKey)
	if want := t0.Add(15 * time.Minute); !row.FreshUntil.Equal(want) {
		t.Errorf("FreshUntil = %v, want now + detail_ttl = %v", row.FreshUntil, want)
	}
	if !row.MaxAgeAt.Equal(row0.MaxAgeAt) || row.Generation <= row0.Generation {
		t.Errorf("extension changed max_age_at or kept the generation: %+v -> %+v", row0, row)
	}
	if string(row.Content) != string(row0.Content) || row.Fingerprint != row0.Fingerprint {
		t.Error("extension changed the stored content")
	}
	if h.fake.count() != 1 {
		t.Errorf("an extension made a pjira call: %d", h.fake.count())
	}
	if c, ok := h.queuedDetail(issueKey); !ok || c < governor.ClassQuery {
		t.Errorf("after an extension the next refresh is queued = %v class %v, want a background task", ok, c)
	}

	h.clk.Advance(20 * time.Minute) // now t0+25m: now+10m would pass max age
	h.persist(summaryOf(issueKey, openCat, "title", updated1))
	row, _ = h.detailRow(issueKey)
	if !row.FreshUntil.Equal(row0.MaxAgeAt) {
		t.Errorf("FreshUntil = %v, want capped at max_age_at %v", row.FreshUntil, row0.MaxAgeAt)
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("revalidation appended rows: %+v", rows)
	}
}

func TestAtTheMaximumAgeTheNextRevalidationQueuesARePull(t *testing.T) {
	h := seeded(t)
	h.clk.Advance(31 * time.Minute) // past max_age_at (+30m)
	before, _ := h.detailRow(issueKey)

	h.persist(summaryOf(issueKey, openCat, "title", updated1))
	after, _ := h.detailRow(issueKey)
	if after.Generation != before.Generation || !after.FreshUntil.Equal(before.FreshUntil) {
		t.Errorf("a row at its maximum age was extended: %+v -> %+v", before, after)
	}
	if _, ok := h.queuedDetail(issueKey); !ok {
		t.Fatal("no re-pull queued at the maximum age")
	}
	h.drain()
	if h.fake.count() != 2 {
		t.Fatalf("pjira calls = %d, want the re-pull as the second", h.fake.count())
	}
	pulled, _ := h.detailRow(issueKey)
	if !pulled.FetchedAt.Equal(h.clk.Now()) || !pulled.MaxAgeAt.Equal(h.clk.Now().Add(30*time.Minute)) {
		t.Errorf("re-pulled row = %+v, want a fresh schedule", pulled)
	}
}

func TestATerminalIssueIsNeitherExtendedNorRepulled(t *testing.T) {
	h := newHarness(t)
	h.fake.set(fakeIssue{Key: issueKey, Category: closedCat, Updated: updated1})
	h.persist(summaryOf(issueKey, closedCat, "done", updated1)) // closed_at = t0
	h.drain()
	before, ok := h.detailRow(issueKey)
	if !ok {
		t.Fatal("no detail row for the freshly closed issue (it is tracked for terminal_ttl)")
	}

	h.clk.Advance(25 * time.Hour) // closed for longer than terminal_ttl
	if e := h.entity(issueKey); !e.ClosedAt.Equal(t0) {
		t.Fatalf("closed_at = %v", e.ClosedAt)
	}
	for _, c := range []groups.SummaryContent{
		summaryOf(issueKey, closedCat, "done", updated1), // unchanged token
		summaryOf(issueKey, closedCat, "done", updated2), // changed token
	} {
		h.persist(c)
		after, _ := h.detailRow(issueKey)
		if after.Generation != before.Generation || !after.FreshUntil.Equal(before.FreshUntil) {
			t.Errorf("a terminal issue's detail was touched: %+v -> %+v", before, after)
		}
		if class, ok := h.queuedDetail(issueKey); ok && class < governor.ClassQuery {
			t.Errorf("a terminal issue has a refresh queued at class %v", class)
		}
	}
}

func TestAnIssueClosedForLessThanTerminalTTLIsStillTracked(t *testing.T) {
	h := seeded(t)
	h.fake.set(fakeIssue{Key: issueKey, Category: closedCat, Updated: updated2})
	h.clk.Advance(time.Minute)
	h.persist(summaryOf(issueKey, closedCat, "title", updated2)) // closes now, token changed
	if c, ok := h.queuedDetail(issueKey); !ok || c != governor.ClassInvalidated {
		t.Errorf("detail queued = %v class %v, want a class 2 refresh", ok, c)
	}
}

func TestARevalidationLeavesAnAlreadyQueuedClass2RefreshAlone(t *testing.T) {
	h := seeded(t)
	h.clk.Advance(5 * time.Minute)
	before, _ := h.detailRow(issueKey)
	if err := h.svc.Group().QueueGroup(context.Background(), issueKey, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	h.persist(summaryOf(issueKey, openCat, "title", updated1)) // unchanged token
	after, _ := h.detailRow(issueKey)
	if after.Generation != before.Generation || !after.FreshUntil.Equal(before.FreshUntil) {
		t.Errorf("a row with a refresh already queued was extended: %+v -> %+v", before, after)
	}
	if c, ok := h.queuedDetail(issueKey); !ok || c != governor.ClassInvalidated {
		t.Errorf("the queued refresh = %v class %v, want it kept at class 2", ok, c)
	}
}

func TestAnUnchangedTokenWithNoStoredDetailDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.persist(summaryOf(issueKey, openCat, "title", updated1)) // first sighting queues the baseline
	h.sched.Cancel(issueKey, groups.Detail)
	h.persist(summaryOf(issueKey, openCat, "title", updated1))
	if _, ok := h.queuedDetail(issueKey); ok {
		t.Error("revalidation queued a detail the issue never had")
	}
	if errs := h.errList(); len(errs) != 0 {
		t.Errorf("errors = %v", errs)
	}
}

func TestRevalidateIgnoresAGoneOrUnnamedSummary(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	gone := summaryOf(issueKey, openCat, "t", updated1)
	gone.Gone = true
	h.svc.Revalidate(ctx, nil, gone, groupsStored(true))
	h.svc.Revalidate(ctx, nil, groups.SummaryContent{}, groupsStored(true))
	if _, ok := h.queuedDetail(issueKey); ok {
		t.Error("a gone or unnamed summary queued a detail")
	}
}

func TestAnErrorIsToldToOnErrorAndTheEntityIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("store is down")
	h.svc.cfg.KeepAlive = failingKeepAlive{err: boom}
	h.svc.Revalidate(context.Background(), nil, summaryOf(issueKey, openCat, "t", updated1), groupsStored(true))
	if errs := h.errList(); len(errs) != 1 || !errors.Is(errs[0], boom) {
		t.Errorf("errors = %v, want the keep-alive failure", errs)
	}
	h.svc.Revalidate(context.Background(), nil, summaryOf(issueKey, openCat, "t", updated1), groupsStored(true))
	h.svc.cfg.OnError = nil
	h.svc.Revalidate(context.Background(), nil, summaryOf(issueKey, openCat, "t", updated1), groupsStored(true)) // must not panic
}

func TestAnExtensionLosesToAnInvalidationThatLandsFirst(t *testing.T) {
	h := seeded(t)
	h.clk.Advance(5 * time.Minute)
	row, _ := h.detailRow(issueKey)
	// An invalidation lands between the read and the commit: the extension's
	// token is stale, so its commit stores nothing.
	if err := groupkit.Invalidate(context.Background(), h.st, groups.EntityKind, issueKey, groups.Detail, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	tok := groupkit.Token{Kind: groups.EntityKind, ID: issueKey, Group: groups.Detail, Generation: row.Generation}
	row.FreshUntil = h.clk.Now().Add(time.Hour)
	if stored, err := groupkit.Commit(context.Background(), h.st, tok, row); err != nil || stored {
		t.Fatalf("a stale extension was stored: %v, %v", stored, err)
	}
}

func TestAGoneIssueTombstonesTheEntityAndAWatchedOneGetsARemovedRow(t *testing.T) {
	h := seeded(t)
	h.fake.remove(issueKey)
	if err := h.svc.Group().QueueGroup(context.Background(), issueKey, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	h.drain()
	if e := h.entity(issueKey); e.TombstonedAt.IsZero() {
		t.Error("an empty search result did not tombstone the issue")
	}
	rows := h.changeRows()
	if len(rows) != 1 || rows[0].ID != issueKey {
		t.Fatalf("rows = %+v, want one", rows)
	}
	found := false
	for _, k := range rows[0].Kinds {
		found = found || k == classify.KindRemoved
	}
	if !found {
		t.Errorf("row kinds = %v, want %q", rows[0].Kinds, classify.KindRemoved)
	}
}

func TestAnUnwatchedGoneIssueIsTombstonedWithNoRow(t *testing.T) {
	h := newHarness(t)
	h.fake.set(fakeIssue{Key: issueKey, Category: openCat, Updated: updated1})
	h.persist(summaryOf(issueKey, openCat, "t", updated1))
	h.drain()
	h.fake.remove(issueKey)
	_ = h.svc.Group().QueueGroup(context.Background(), issueKey, groups.Detail, governor.ClassInvalidated, time.Time{})
	h.drain()
	if e := h.entity(issueKey); e.TombstonedAt.IsZero() {
		t.Error("not tombstoned")
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("an unwatched issue got rows: %+v", rows)
	}
}

func TestA429LeavesThePreviousDetailInPlaceAndRecordsLastError(t *testing.T) {
	h := seeded(t)
	before, _ := h.detailRow(issueKey)
	h.clk.Advance(time.Minute)
	h.fake.setErr(errors.New(text429))
	if err := h.svc.Group().QueueGroup(context.Background(), issueKey, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	h.drain()

	after, _ := h.detailRow(issueKey)
	if string(after.Content) != string(before.Content) || after.Fingerprint != before.Fingerprint || !after.FetchedAt.Equal(before.FetchedAt) {
		t.Errorf("the previous detail was replaced: %+v -> %+v", before, after)
	}
	if after.LastError == "" {
		t.Error("last_error was not recorded")
	}
	if rows := h.changeRows(); len(rows) != 0 {
		t.Errorf("a failed fetch appended rows: %+v", rows)
	}
	// The origin runner (not the fetcher or the policy) paused the background
	// classes; the answer is not an authentication failure.
	if _, _, paused := h.runner.Paused(); !paused {
		t.Error("the origin runner did not pause after the 429")
	}
	if un, _ := h.auth.Unauthenticated(); un {
		t.Error("a rate-limit answer marked the daemon unauthenticated")
	}
}

func TestAnAuthenticationFailureMarksTheDaemonAndKeepsTheDetail(t *testing.T) {
	h := seeded(t)
	before, _ := h.detailRow(issueKey)
	h.fake.setErr(errors.New("pjira: search: status 401 Unauthorized"))
	if err := h.svc.Group().QueueGroup(context.Background(), issueKey, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	h.drain()
	after, _ := h.detailRow(issueKey)
	if string(after.Content) != string(before.Content) || after.LastError == "" {
		t.Errorf("row = %+v, want the old content and a last_error", after)
	}
	if un, _ := h.auth.Unauthenticated(); !un {
		t.Error("an authentication failure did not mark the daemon")
	}
}

// groupsStored is the Stored the persister hands AfterCommit for a first
// sighting (or not).
func groupsStored(first bool) summary.Stored { return summary.Stored{First: first} }

// failingKeepAlive is a keep-alive rule whose Retain fails.
type failingKeepAlive struct{ err error }

func (k failingKeepAlive) Retain(context.Context, string) (bool, error) { return false, k.err }
func (k failingKeepAlive) Access(context.Context, string) error         { return nil }
