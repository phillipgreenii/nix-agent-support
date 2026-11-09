package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// captureInject replaces focusInject with a recorder for one test.
func captureInject(t *testing.T, fail error) *[]string {
	t.Helper()
	var got []string
	orig := focusInject
	focusInject = func(_ context.Context, ev string) error {
		got = append(got, ev)
		return fail
	}
	t.Cleanup(func() { focusInject = orig })
	return &got
}

// Bead pg2-e5g9gx: a PR newly selected is announced to the router as one
// pr.changed event shaped like the PR source's item, so the pr-change role
// runs for a PR that never changes again.
func TestSelectApplyAnnouncesEachNewPRRowOnce(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.showDraft(t)
	got := captureInject(t, nil)

	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	if len(*got) != 2 {
		t.Fatalf("announced %d events, want 2: %v", len(*got), *got)
	}
	var ev struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		At      string `json:"at"`
		Expires string `json:"expiresAt"`
		Payload struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Metadata struct {
				EntityID string `json:"entity_id"`
			} `json:"metadata"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte((*got)[0]), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "pr.changed" || ev.Payload.Type != "pr" || ev.Payload.Metadata.EntityID != "o/r#1" ||
		!strings.HasPrefix(ev.Payload.ID, "o/r#1@") || len(ev.Payload.ID) != len("o/r#1@")+12 ||
		ev.At == "" || ev.Expires == "" || !strings.Contains(ev.ID, "o/r#1") {
		t.Errorf("event = %s", (*got)[0])
	}

	// A re-select inserts nothing new, so it announces nothing (idempotent).
	*got = nil
	_, _, err = sel(t, "ok", "--apply")
	mustOK(t, err)
	if len(*got) != 0 {
		t.Errorf("re-select announced %v, want none", *got)
	}
}

func TestSelectDryRunAnnouncesNothing(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	got := captureInject(t, nil)
	_, _, err := sel(t, "ok", "--dry-run")
	mustOK(t, err)
	if len(*got) != 0 {
		t.Errorf("dry run announced %v", *got)
	}
}

// A failed injection warns and never changes the lock's exit code.
func TestSelectAnnounceFailureWarnsAndKeepsExitOK(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	captureInject(t, errors.New("no running core"))
	_, errOut, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	mustContain(t, errOut, "selection not announced to the decider", "pg-pr-change apply o/r#1")
}

// Bead pg2-2qyrca: locking a plan with a quiet PR and a quiet issue (neither
// changes afterwards) reaches the decider with no manual backfill. The PR is
// announced with exactly one pr.changed injection; the issue gets exactly one
// annotation_changed change_log row, which the issue change feed delivers as
// an issue.changed item. A re-run of select --apply writes and announces
// nothing, so the doctor "unminted selections" gate has nothing to fail on.
func TestSelectApplyQuietPRAndIssueReachDeciderExactlyOnce(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	withHydrator(t, &writingHydrator{st: f.st, write: func(st *store.Store, typ, id string) error {
		e := store.Entity{Repo: showRepo, EntityType: typ, EntityID: id, Facts: `{"issue_show":{"id":"J-9","state":"In Progress","title":"quiet"}}`, AsOf: showAt, ContentHash: "hJ-9"}
		if _, err := st.WriteEntityStateWithLog(e, 0, showAt, true, []string{"created"}, "sync", showAt); err != nil {
			return err
		}
		// A selected issue is a watched one: only a watched issue's
		// annotation reaches the issue change feed (INV-ICD-16).
		on := true
		return st.SetEntityColumns(showRepo, typ, id, nil, &on)
	}})
	got := captureInject(t, nil)

	_, _, err := sel(t, "ok +J-9", "--apply")
	mustOK(t, err)
	if len(*got) != 1 || !strings.Contains((*got)[0], `"entity_id":"o/r#1"`) {
		t.Fatalf("injections = %v, want exactly one for o/r#1", *got)
	}
	prHist := kindsOf(t, f.st, "pr", "o/r#1")
	issHist := kindsOf(t, f.st, "issue", "J-9")
	count := func(h []string) (n int) {
		for _, k := range h {
			if k == "annotation_changed@pg-desk" {
				n++
			}
		}
		return n
	}
	if count(prHist) != 1 || count(issHist) != 1 {
		t.Fatalf("annotation_changed rows pr=%v issue=%v, want one each", prHist, issHist)
	}

	// Re-run: nothing is written and nothing is announced.
	*got = nil
	_, _, err = sel(t, "ok", "--apply")
	mustOK(t, err)
	if len(*got) != 0 {
		t.Errorf("re-run announced %v", *got)
	}
	if h := kindsOf(t, f.st, "pr", "o/r#1"); len(h) != len(prHist) {
		t.Errorf("re-run changed pr history: %v -> %v", prHist, h)
	}
	if h := kindsOf(t, f.st, "issue", "J-9"); len(h) != len(issHist) {
		t.Errorf("re-run changed issue history: %v -> %v", issHist, h)
	}
}

// A PR the lock removes from the plan is announced with a removal event whose
// id differs from the selection event of the same period, so the router queue
// does not fold the two: select then strike in one period yields two distinct
// event ids, and the removal carries the focus_removed kind.
func TestStrikeAfterSelectAnnouncesRemovalWithADistinctEventID(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	got := captureInject(t, nil)

	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	_, _, err = sel(t, "-1", "--apply")
	mustOK(t, err)
	if len(*got) != 2 {
		t.Fatalf("announced %d events, want select + removal: %v", len(*got), *got)
	}
	type evt struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Payload struct {
			ID       string `json:"id"`
			Metadata struct {
				EntityID string   `json:"entity_id"`
				Kinds    []string `json:"kinds"`
			} `json:"metadata"`
		} `json:"payload"`
	}
	var sele, rem evt
	if err := json.Unmarshal([]byte((*got)[0]), &sele); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte((*got)[1]), &rem); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sele.ID, "focus-select:o/r#1@") {
		t.Errorf("selection id = %q", sele.ID)
	}
	if !strings.HasPrefix(rem.ID, "focus-remove:o/r#1@"+showToday+"@") || rem.ID == sele.ID {
		t.Errorf("removal id = %q, want a focus-remove id distinct from %q", rem.ID, sele.ID)
	}
	if rem.Type != "pr.changed" || rem.Payload.Metadata.EntityID != "o/r#1" ||
		len(rem.Payload.Metadata.Kinds) != 1 || rem.Payload.Metadata.Kinds[0] != "focus_removed" ||
		!strings.HasPrefix(rem.Payload.ID, "o/r#1@") || rem.Payload.ID == sele.Payload.ID {
		t.Errorf("removal event = %s", (*got)[1])
	}
	if a := f.annotation(t, focus.Key{Type: "pr", ID: "o/r#1"}); a != "none" {
		t.Errorf("annotation = %q, want none before the decider runs", a)
	}
}

// The event id is stable for one lock run (a retried injection folds) and a
// later removal at another time is a new event.
func TestRemovalEventIDIsStablePerRunAndNewPerRun(t *testing.T) {
	k := focus.Key{Type: "pr", ID: "o/r#1"}
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	a, err := removalEvent(k, showToday, t0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := removalEvent(k, showToday, t0)
	c, _ := removalEvent(k, showToday, t0.Add(time.Minute))
	id := func(s string) string {
		var e struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(s), &e)
		return e.ID
	}
	if a != b {
		t.Errorf("same run built two different events:\n%s\n%s", a, b)
	}
	if id(a) == id(c) {
		t.Errorf("a later removal reused id %q", id(a))
	}
}

func TestRemovalIsNotAnnouncedOnDryRun(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	got := captureInject(t, nil)
	_, _, err = sel(t, "-1", "--dry-run")
	mustOK(t, err)
	if len(*got) != 0 {
		t.Errorf("dry-run strike announced %v", *got)
	}
}

// A failed removal injection warns and keeps the exit code.
func TestRemovalAnnounceFailureWarnsAndKeepsExitOK(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	captureInject(t, errors.New("no running core"))
	_, errOut, err := sel(t, "-1", "--apply")
	mustOK(t, err)
	mustContain(t, errOut, "removal not announced to the decider", "pg-pr-change apply o/r#1")
}
