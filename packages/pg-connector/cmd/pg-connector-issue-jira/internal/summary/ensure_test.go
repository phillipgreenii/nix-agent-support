package summary

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func ensure(t *testing.T, h *harness, key string, opts EnsureOptions) (Result, error) {
	t.Helper()
	return h.svc.Ensure(context.Background(), key, opts)
}

func TestEnsureNonJiraKeyIsNotFoundWithZeroOriginCalls(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, k := range []string{"pg2-abc", "ABC", "", "A-1; DROP", `A-1") OR ("B-2`, "123-4"} {
		_, err := ensure(t, h, k, EnsureOptions{})
		if !errors.Is(err, scriptout.ErrNotFound) {
			t.Errorf("Ensure(%q) = %v, want not_found", k, err)
		}
	}
	if h.fake.count() != 0 {
		t.Errorf("origin calls = %d, want 0", h.fake.count())
	}
}

func TestEnsureUnknownKeyFetchesOnceAtClass1TracksItAndASecondEnsureIsALocalHit(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.fake.set("A-9", "indeterminate", "2026-10-04T10:00:00.000+0000")

	r, err := ensure(t, h, "a-9", EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.FetchedNow || r.Stale || r.Content.Issue.ID != "A-9" || r.Content.Issue.CommentCount != 1 || r.AgeSeconds != 0 {
		t.Errorf("result = %+v", r)
	}
	if r.Row.ID != "A-9" || !r.Row.FetchedAt.Equal(t0) {
		t.Errorf("row = %+v, want the stored summary row fetched at t0", r.Row)
	}
	if h.fake.count() != 1 {
		t.Fatalf("calls = %d, want 1", h.fake.count())
	}
	if want := `key in ("A-9")`; h.fake.call(0)[2] != want {
		t.Errorf("jql = %q, want %q", h.fake.call(0)[2], want)
	}
	sp := h.spendList()
	if len(sp) != 1 || sp[0].Class != governor.ClassInteractive || sp[0].Purpose != governor.PurposeInteractive ||
		sp[0].Group != groups.Summary || sp[0].BatchSize != 1 {
		t.Errorf("spend = %+v, want one class 1 interactive spend of group summary", sp)
	}
	e, ok := h.entityRow("A-9")
	if !ok || !e.KeepAlive || !e.LastAccess.Equal(t0) {
		t.Errorf("entity = %+v ok=%v, want tracked (keep_alive, last_access now)", e, ok)
	}
	if c, ok := h.queuedClass("A-9"); !ok || c != governor.ClassKeepAlive {
		t.Errorf("next refresh queued = %v class %v, want class 4", ok, c)
	}

	h.clk.Advance(10 * time.Second)
	r2, err := ensure(t, h, "A-9", EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r2.FetchedNow || r2.Stale || r2.AgeSeconds != 10 || r2.Content.Issue.ID != "A-9" {
		t.Errorf("second result = %+v, want a local hit aged 10s", r2)
	}
	if h.fake.count() != 1 {
		t.Errorf("a local hit called the origin: calls = %d", h.fake.count())
	}

	if _, err := ensure(t, h, "A-9", EnsureOptions{Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if h.fake.count() != 2 {
		t.Errorf("Fresh did not force a fetch: calls = %d", h.fake.count())
	}
}

func TestEnsureStaleStoredSummaryIsRefetched(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("A-1", "indeterminate")
	h.fake.set("A-1", "indeterminate")
	h.clk.Advance(2 * time.Minute)
	r, err := ensure(t, h, "A-1", EnsureOptions{})
	if err != nil || !r.FetchedNow || r.Stale {
		t.Fatalf("Ensure = %+v, %v, want a fetch of the expired summary", r, err)
	}
}

func TestEnsureMemberIsNotRebookedByTheSummaryService(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.fake.set("M-1", "indeterminate")
	h.setMember("M-1", true)
	if _, err := ensure(t, h, "M-1", EnsureOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.queuedClass("M-1"); ok {
		t.Error("a query member was booked by Ensure")
	}
}

func TestEnsureEmptySearchIsNotFoundAndTombstonesAKnownIssue(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("K-1", "indeterminate")
	h.clk.Advance(2 * time.Minute)
	if _, err := ensure(t, h, "K-1", EnsureOptions{}); !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("Ensure(known, absent) = %v, want not_found", err)
	}
	if e, _ := h.entityRow("K-1"); e.TombstonedAt.IsZero() {
		t.Error("the vanished issue was not tombstoned")
	}
	if len(h.obs.gone) != 1 {
		t.Errorf("observer gone = %v", h.obs.gone)
	}

	if _, err := ensure(t, h, "U-1", EnsureOptions{}); !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("Ensure(unknown, absent) = %v, want not_found", err)
	}
	if _, ok := h.entityRow("U-1"); ok {
		t.Error("a read of a missing issue created an entity (it would be tracked forever)")
	}
}

func TestEnsureDuringAPauseServesStoredStaleAndAnswersUnavailableForAnUnknownKey(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("A-1", "indeterminate")
	h.fake.set("A-1", "indeterminate")
	h.fake.set("B-7", "indeterminate")
	h.clk.Advance(2 * time.Minute) // stored, past fresh_until, inside the 7 day expiry
	if err := h.gov.Pause("requests", 3*time.Hour, "jira answered HTTP 429"); err != nil {
		t.Fatal(err)
	}

	r, err := ensure(t, h, "A-1", EnsureOptions{})
	if err != nil {
		t.Fatalf("Ensure(stored) during a pause = %v, want it served stale", err)
	}
	if !r.Stale || r.FetchedNow || r.AgeSeconds != 120 || r.Content.Issue.ID != "A-1" {
		t.Errorf("result = %+v, want stale, not fetched, aged 120s", r)
	}

	_, err = ensure(t, h, "B-7", EnsureOptions{})
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "paused") {
		t.Errorf("Ensure(unknown) during a pause = %v, want unavailable naming the pause", err)
	}
	if h.fake.count() != 0 {
		t.Errorf("pjira was called %d times during the pause", h.fake.count())
	}
}

func TestEnsureStoredSummaryPastTheExpiryIsNotServedStale(t *testing.T) {
	h := newHarness(t, harnessOpts{expiry: time.Hour})
	h.seed("A-1", "indeterminate")
	h.clk.Advance(2 * time.Hour)
	if err := h.gov.Pause("requests", 24*time.Hour, "jira answered HTTP 429"); err != nil {
		t.Fatal(err)
	}
	if _, err := ensure(t, h, "A-1", EnsureOptions{}); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("Ensure = %v, want unavailable once the stored summary is past the expiry", err)
	}
}

func TestEnsureOriginFailureServesStaleOrNamesTheCause(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("A-1", "indeterminate")
	h.clk.Advance(2 * time.Minute)
	h.fake.err = errors.New("pjira search: status 503 Service Unavailable")

	r, err := ensure(t, h, "A-1", EnsureOptions{})
	if err != nil || !r.Stale {
		t.Fatalf("Ensure(stored) with the origin failing = %+v, %v, want stale", r, err)
	}
	_, err = ensure(t, h, "B-7", EnsureOptions{})
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "503") {
		t.Errorf("Ensure(unknown) = %v, want unavailable naming the cause", err)
	}
}

func TestEnsureTruncatedSearchIsAFailureNotAnAbsence(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("K-1", "indeterminate")
	h.clk.Advance(2 * time.Minute)
	h.fake.truncated = true
	r, err := ensure(t, h, "K-1", EnsureOptions{})
	if err != nil || !r.Stale {
		t.Fatalf("Ensure = %+v, %v, want the stored summary served stale", r, err)
	}
	if e, _ := h.entityRow("K-1"); !e.TombstonedAt.IsZero() {
		t.Error("a truncated search tombstoned the issue")
	}
}

func TestEnsureTombstonedIssueHasNothingToServeStale(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("K-1", "indeterminate")
	if err := h.pers.MarkGone(context.Background(), "K-1", governor.PurposeQuery); err != nil {
		t.Fatal(err)
	}
	h.fake.err = errors.New("pjira search: boom")
	if _, err := ensure(t, h, "K-1", EnsureOptions{}); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("Ensure = %v, want unavailable (a vanished issue's old summary is not served)", err)
	}
}

func TestEnsureThatLosesTheRaceToAWriteBooksNothingAndIsMarkedStale(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.fake.set("A-9", "indeterminate")
	h.fake.beforeReturn = func() {
		persist(t, h.pers, issue("A-9", "indeterminate", "written meanwhile"))
	}
	r, err := ensure(t, h, "A-9", EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.FetchedNow || !r.Stale {
		t.Errorf("result = %+v, want fetched but stale", r)
	}
	if got := string(h.groupRow("A-9").Content); !strings.Contains(got, "written meanwhile") {
		t.Errorf("stored content = %s, want the write to stand", got)
	}
	if _, ok := h.queuedClass("A-9"); ok {
		t.Error("Ensure re-booked after losing the race: the write notice's re-pull owns it")
	}
}

func TestEnsureCancelledContextReturnsItsError(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.seed("K-1", "indeterminate")
	h.clk.Advance(2 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	h.fake.beforeReturn = cancel
	h.fake.err = errors.New("pjira: killed")
	if _, err := h.svc.Ensure(ctx, "K-1", EnsureOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Ensure = %v, want context.Canceled", err)
	}
}
