package attention

import (
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func putIssue(t *testing.T, st *store.Store, id, facts string) {
	t.Helper()
	if err := st.UpsertEntity(store.Entity{Repo: testRepo, EntityType: "issue", EntityID: id, Facts: facts, AsOf: "2026-10-06T11:00:00Z"}); err != nil {
		t.Fatal(err)
	}
}

func projectedRefs(t *testing.T, st *store.Store) []string {
	t.Helper()
	views, err := Project(st, testRepo, baseConfig())
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	var refs []string
	for _, v := range views {
		refs = append(refs, v.Ref())
	}
	sort.Strings(refs)
	return refs
}

// An issue entity needs no interpretation row to be projected, and its view
// carries the entity row, so a pure rule can read its facts.
func TestProjectIssueEntityWithoutInterpretationRow(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "K-1", `{"issue_show":{"id":"K-1","state":"In Progress"}}`)

	views, err := Project(st, testRepo, baseConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Type != "issue" || views[0].ID != "K-1" {
		t.Fatalf("views = %+v, want exactly issue K-1", views)
	}
	v := views[0]
	if v.Entity == nil || v.Panel != "" || v.Degraded {
		t.Errorf("view = %+v, want an entity-backed, non-degraded view with no panel", v)
	}
	if want := time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC); !v.AsOf.Equal(want) {
		t.Errorf("AsOf = %v, want %v from the entity row", v.AsOf, want)
	}
}

func TestProjectEntityOnlyRowSelection(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	// pr with an interpretation row: projected once, through the row.
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	// pr entity with NO interpretation row: not projected (a PR rule reads the interpretation).
	if err := st.UpsertEntity(store.Entity{Repo: testRepo, EntityType: "pr", EntityID: testRepo + "#2", Facts: "{}", AsOf: "2026-10-06T11:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	// issue with an interpretation row (the generic pipeline writes one): projected once, not twice.
	putIssue(t, st, "K-1", `{"issue_show":{"state":"To Do"}}`)
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: testRepo, EntityType: "issue", EntityID: "K-1", Ownership: "mine",
		Enrichment: "{}", Urgency: "{}", Dispositions: "[]", AsOf: "2026-10-06T11:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// issue with no interpretation row: projected.
	putIssue(t, st, "K-2", `{"issue_show":{"state":"In Progress"}}`)
	// issue in another repo: not projected.
	if err := st.UpsertEntity(store.Entity{Repo: "other/repo", EntityType: "issue", EntityID: "K-3", Facts: "{}", AsOf: "2026-10-06T11:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	want := []string{"issue:K-1", "issue:K-2", "pr:" + testRepo + "#1"}
	got := projectedRefs(t, st)
	if len(got) != len(want) {
		t.Fatalf("projected = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("projected = %v, want %v", got, want)
		}
	}
}

func TestProjectSkipsInactiveEntityOnlyIssue(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "K-1", `{"issue_show":{"state":"In Progress"}}`)
	if _, err := st.WriteEntityStateWithLog(store.Entity{
		Repo: testRepo, EntityType: "issue", EntityID: "K-2", Facts: `{"issue_show":{"state":"In Progress"}}`, AsOf: "2026-10-06T11:00:00Z",
	}, 0, "2026-10-06T11:00:00Z", false, nil, "pg-desk", "2026-10-06T11:00:00Z"); err != nil {
		t.Fatalf("write inactive entity: %v", err)
	}
	if got := projectedRefs(t, st); len(got) != 1 || got[0] != "issue:K-1" {
		t.Errorf("projected = %v, want only the active issue", got)
	}
}

func TestViewIssueFactsIsNotAvailableForOtherTypesOrBadFacts(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "K-1", `not json`)
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	views, err := Project(st, testRepo, baseConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if _, ok := v.IssueFacts(); ok {
			t.Errorf("%s: IssueFacts ok = true, want false", v.Ref())
		}
	}
}

func TestIssueEntitiesRaiseNothingFromPRRules(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "K-1", `{"issue_show":{"state":"In Progress"}}`)
	res := evaluate(t, st, baseConfig())
	if len(res.Items) != 0 {
		t.Errorf("items = %+v, want none: this issue carries no operator facts and no PR rule applies to an issue", res.Items)
	}
	tr, ok := res.Traces[Ref("issue", "K-1")]
	if !ok {
		t.Fatal("an issue entity must be traceable so `attention explain` can describe it")
	}
	if why := tr.NotRaised[KindReviewRequested]; why != "not a pr" {
		t.Errorf("explain reason = %q, want %q", why, "not a pr")
	}
}
