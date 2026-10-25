package store

import (
	"fmt"
	"testing"
	"time"
)

// TestCountPRAnchors seeds, on a real schema-2 store, every kind of row the
// open-anchor view must count or leave out, so each filter is shown by a row
// that it removes: the anchor's type and state, the link's relation, the PR's
// hidden annotation, the PR's closed or merged facts, and a PR that is absent.
func TestCountPRAnchors(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.sql.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	pr := func(n int, facts, hydratedAt string) string {
		id := fmt.Sprintf("o/r#%d", n)
		mustExec(`INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, content_hash, first_seen_at, hydrated_at)
		          VALUES ('o/r', 'pr', ?, ?, '2026-10-10T00:00:00Z', 'h', '2026-09-01T00:00:00Z', NULLIF(?, ''))`, id, facts, hydratedAt)
		return id
	}
	anchor := func(id, issueType, state, prID, relation string, active int) {
		t.Helper()
		facts := fmt.Sprintf(`{"issue_show":{"issue_type":%q,"state":%q}}`, issueType, state)
		mustExec(`INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, content_hash, active)
		          VALUES ('o/r', 'issue', ?, ?, '2026-10-10T00:00:00Z', 'h', ?)`, id, facts, active)
		mustExec(`INSERT INTO xref (repo, from_type, from_id, to_type, to_id, origin, relation, first_seen, last_confirmed)
		          VALUES ('o/r', 'issue', ?, 'pr', ?, 'derived:work-item', ?, 't', 't')`, id, prID, relation)
	}
	hide := func(prID string) {
		mustExec(`INSERT INTO annotation (repo, entity_type, entity_id, key, value, origin, set_by, set_at)
		          VALUES ('o/r','pr',?,'hidden','{"value":true,"reason":"x"}','pg-desk','t','2026-10-01T00:00:00Z')`, prID)
	}

	// Counted, open in the store: hydrated 2 days ago (the oldest of the set)
	// and 1 hour ago.
	anchor("a1", AnchorIssueType, "open", pr(1, `{"pr_show":{"state":"open"}}`, "2026-10-09T00:00:00Z"), "work", 1)
	anchor("a2", AnchorIssueType, "in_progress", pr(2, `{"pr_show":{"state":"open"}}`, "2026-10-10T23:00:00Z"), "work", 1)
	// Counted twice over one PR: two anchors, one PR, one count.
	anchor("a3", AnchorIssueType, "open", "o/r#2", "work", 1)
	// Counted, never hydrated: the age runs from first_seen_at (2026-09-01).
	anchor("a4", AnchorIssueType, "open", pr(3, `{}`, ""), "work", 1)
	// Stale: the store shows the PR merged (hydrated 3 h ago) and closed (6 h ago).
	anchor("a5", AnchorIssueType, "open", pr(4, `{"pr_show":{"state":"open","merged":true}}`, "2026-10-10T21:00:00Z"), "work", 1)
	anchor("a6", AnchorIssueType, "open", pr(5, `{"pr_show":{"state":"closed"}}`, "2026-10-10T18:00:00Z"), "work", 1)
	// Hidden PRs: counted apart, and not stale or aged even when closed.
	anchor("a7", AnchorIssueType, "open", pr(6, `{"pr_show":{"state":"open"}}`, "2026-08-01T00:00:00Z"), "work", 1)
	hide("o/r#6")
	anchor("a8", AnchorIssueType, "open", pr(7, `{"pr_show":{"state":"closed"}}`, "2026-08-01T00:00:00Z"), "work", 1)
	hide("o/r#7")
	// Left out: a closed anchor, a child item (another type), an anchor whose
	// link is not a "work" link, an inactive anchor, and facts that decode to
	// nothing.
	anchor("c1", AnchorIssueType, "closed", pr(8, `{"pr_show":{"state":"open"}}`, "2026-01-01T00:00:00Z"), "work", 1)
	anchor("c2", "task", "open", pr(9, `{"pr_show":{"state":"open"}}`, "2026-01-01T00:00:00Z"), "work", 1)
	anchor("c3", AnchorIssueType, "open", pr(10, `{"pr_show":{"state":"open"}}`, "2026-01-01T00:00:00Z"), "source", 1)
	anchor("c4", AnchorIssueType, "open", pr(11, `{"pr_show":{"state":"open"}}`, "2026-01-01T00:00:00Z"), "work", 0)
	mustExec(`INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, content_hash) VALUES ('o/r', 'issue', 'c5', 'not json', 't', 'h')`)
	mustExec(`INSERT INTO xref (repo, from_type, from_id, to_type, to_id, origin, relation, first_seen, last_confirmed)
	          VALUES ('o/r', 'issue', 'c5', 'pr', 'o/r#12', 'derived:work-item', 'work', 't', 't')`)
	// An open anchor whose PR row is absent from the store: counted open, but
	// it has no age to report.
	anchor("a9", AnchorIssueType, "open", "o/r#99", "work", 1)

	got, err := s.CountPRAnchors(now)
	if err != nil {
		t.Fatalf("CountPRAnchors: %v", err)
	}
	// Open PRs (hidden excluded): #1, #2, #3, #4, #5, #99 = 6; hidden: #6, #7.
	want := PRAnchors{
		Open:       6,
		OpenHidden: 2,
		Stale:      2,
		// The stale PRs were hydrated 3 h and 6 h before now: the oldest is 6 h.
		StaleOldestAgeSeconds: 6 * 3600,
		// Among the open ones: #3 never hydrated, so from first_seen_at 2026-09-01.
		OldestHydrationAgeSeconds: int(now.Sub(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)).Seconds()),
	}
	if got != want {
		t.Errorf("CountPRAnchors = %+v, want %+v", got, want)
	}
}

func TestCountPRAnchorsEmptyStoreIsZeroAndAnOldSchemaIsAnError(t *testing.T) {
	got, err := OpenNewSchemaForTest(t).CountPRAnchors(time.Now())
	if err != nil || got != (PRAnchors{}) {
		t.Errorf("empty schema-2 store: got %+v, err %v; want the zero view", got, err)
	}
	// A schema-1 store has no xref relation column: the read fails and the
	// failure is returned, never folded into a zero view.
	if got, err := OpenForTest(t).CountPRAnchors(time.Now()); err == nil {
		t.Errorf("schema-1 store: got %+v with no error; a failed read must be an error", got)
	}
}
