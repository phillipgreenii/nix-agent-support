package links

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// ReadLinked is the shared linked-entity read helper (also the intended
// reader for the Phase 6 composite view's links[]): one entry per
// (direction, entity, relation) holding every origin that claims it, from
// both ends of the edge.
func TestReadLinkedMergesOriginsAndBothDirections(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putDerived(t, st, "pr", testPR, "issue", "ABC-42", "jira", "jira-key")
	if err := st.AddExternalXref(store.XrefLink{
		Repo: testRepo, FromType: "pr", FromID: testPR, ToType: "issue", ToID: "ABC-42", Relation: "jira",
		Actor: "operator", ActedAt: "2026-10-01T12:00:00Z", Reason: "same incident",
		FirstSeen: "2026-10-01T12:00:00Z", LastConfirmed: "2026-10-01T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	putDerived(t, st, "thread", "C1/100.1", "pr", testPR, "mentions", "thread-refs")

	got, degraded, err := ReadLinked(st, testRepo, "pr", testPR)
	if err != nil || degraded {
		t.Fatalf("ReadLinked: degraded=%v err=%v", degraded, err)
	}
	want := []Linked{
		{Direction: DirOut, Type: "issue", ID: "ABC-42", Relation: "jira", Origins: []Origin{
			{Origin: "derived:jira-key"},
			{Origin: "external:operator", At: "2026-10-01T12:00:00Z", Reason: "same incident"},
		}},
		{Direction: DirIn, Type: "thread", ID: "C1/100.1", Relation: "mentions", Origins: []Origin{{Origin: "derived:thread-refs"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadLinked =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSnapshotURL(t *testing.T) {
	cases := []struct {
		typ, facts, want string
	}{
		{"pr", `{"pr_show":{"url":"https://a.example.invalid/p/1"}}`, "https://a.example.invalid/p/1"},
		{"issue", `{"issue_show":{"url":"https://a.example.invalid/i/1"}}`, "https://a.example.invalid/i/1"},
		{"thread", `{"thread_show":{"permalink":"https://a.example.invalid/t/1"}}`, "https://a.example.invalid/t/1"},
		{"thread", `{"thread_show":{"url":"https://a.example.invalid/t/2"}}`, "https://a.example.invalid/t/2"},
		{"pr", `{}`, ""},
		{"pr", `not json`, ""},
		{"pr", `{"pr_show":{"url":"ftp://a.example.invalid/x"}}`, ""},
		{"pr", `{"pr_show":{"url":"https://a.example.invalid/has space"}}`, ""},
		{"alert", `{"pr_show":{"url":"https://a.example.invalid/x"}}`, ""},
	}
	for _, c := range cases {
		if got := SnapshotURL(c.typ, c.facts); got != c.want {
			t.Errorf("SnapshotURL(%s, %s) = %q; want %q", c.typ, c.facts, got, c.want)
		}
	}
}

// ReadDetailed adds the linked entity's url and stored state on top of
// ReadLinked's edges and reports when the links were last confirmed.
func TestReadDetailedJoinsStoredSnapshotAndTemplateURL(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	var derived []store.XrefLink
	for _, key := range []string{"ABC-42", "ABC-43"} {
		derived = append(derived, store.XrefLink{
			Repo: testRepo, FromType: "pr", FromID: testPR, ToType: "issue", ToID: key,
			Relation: "jira", Origin: "derived:jira-key", FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z",
		})
	}
	if err := st.ReplaceDerivedXrefs(testRepo, "pr", testPR, derived); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertEntity(store.Entity{
		Repo: testRepo, EntityType: "issue", EntityID: "ABC-43", AsOf: "2026-10-01T00:00:00Z",
		Facts: `{"issue_show":{"title":"ABC-43: adopt me","state":"closed","labels":["x"],"metadata":{"k":"v"},"assignee":"bob","closed_by":"nobody"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	d := Deps{Store: st, Repo: testRepo, Patterns: []string{`[A-Z]+-\d+`}, IssueURLTemplate: "https://tracker.example.invalid/browse/%s"}
	got, err := ReadDetailed(d, "pr", testPR)
	if err != nil || got.Degraded || len(got.Links) != 2 || got.LinksAsOf == "" {
		t.Fatalf("ReadDetailed = %+v, %v", got, err)
	}
	unstored, stored := got.Links[0], got.Links[1]
	if unstored.ID != "ABC-42" || unstored.Stored || unstored.State != "" || unstored.URL != "https://tracker.example.invalid/browse/ABC-42" {
		t.Errorf("unstored link = %+v", unstored)
	}
	if unstored.Title != "" || stored.Title != "ABC-43: adopt me" {
		t.Errorf("titles = %q (unstored), %q (stored); want \"\" and the stored title", unstored.Title, stored.Title)
	}
	if !stored.Stored || stored.State != "closed" || stored.Assignee != "bob" || stored.Metadata["k"] != "v" || !reflect.DeepEqual(stored.Labels, []string{"x"}) {
		t.Errorf("stored link = %+v", stored)
	}
}
