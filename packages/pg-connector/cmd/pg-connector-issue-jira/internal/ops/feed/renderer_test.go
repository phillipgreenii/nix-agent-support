package feed

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feedop"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func decodeIssue(t *testing.T, raw json.RawMessage, err error) schema.Issue {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var is schema.Issue
	if err := json.Unmarshal(raw, &is); err != nil {
		t.Fatal(err)
	}
	return is
}

func renderRow(t *testing.T, rd feedop.Renderer, row store.ChangeRow) schema.Issue {
	t.Helper()
	raw, err := rd.Row(context.Background(), row)
	return decodeIssue(t, raw, err)
}

func renderReplay(t *testing.T, rd feedop.Renderer, ref string) schema.Issue {
	t.Helper()
	raw, err := rd.Replay(context.Background(), ref)
	return decodeIssue(t, raw, err)
}

func putSummary(t *testing.T, st *store.Store, id string, fetched time.Time, c groups.SummaryContent) {
	t.Helper()
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(context.Background(), store.GroupRow{Kind: groups.EntityKind, ID: id, Group: groups.Summary, Content: body, FetchedAt: fetched}); err != nil {
		t.Fatal(err)
	}
}

func TestRenderer_RowIsTheStoredSummaryWithTheRowsOwnVersionAndTheFetchedTime(t *testing.T) {
	st := openStore(t)
	fetched := time.Date(2026, 10, 5, 13, 14, 15, 0, time.FixedZone("x", 3600))
	putSummary(t, st, "A-1", fetched, groups.SummaryContent{Issue: schema.Issue{ID: "A-1", Title: "now", State: "Done", StatusCategory: "done", AsOf: "stale-text", Stale: true, Version: 99}})

	rd := NewRenderer(st)
	got := renderRow(t, rd, store.ChangeRow{ID: "A-1", Version: 3})
	if got.ID != "A-1" || got.Title != "now" || got.Version != 3 {
		t.Errorf("entity = %+v, want the stored summary at the row's own version 3", got)
	}
	if got.AsOf != "2026-10-05T12:14:15Z" || got.Stale {
		t.Errorf("as_of = %q stale = %v, want the fetched time in UTC and not stale", got.AsOf, got.Stale)
	}
	older := renderRow(t, rd, store.ChangeRow{ID: "A-1", Version: 2})
	if older.Version != 2 || older.Title != "now" {
		t.Errorf("older row = %+v, want its own version 2 over the current summary", older)
	}
}

func TestRenderer_AnIssueWithNoStoredSummaryIsAMinimalStaleEntity(t *testing.T) {
	rd := NewRenderer(openStore(t))
	got := renderRow(t, rd, store.ChangeRow{ID: "A-9", Version: 4, Kinds: []string{"removed"}})
	want := schema.Issue{ID: "A-9", Version: 4, Stale: true}
	if got.ID != want.ID || got.Version != want.Version || !got.Stale || got.AsOf != "" || got.Title != "" {
		t.Errorf("entity = %+v, want only the id and version, empty as_of and stale", got)
	}
}

func TestRenderer_AGoneIssueRendersItsLastStoredSummary(t *testing.T) {
	st := openStore(t)
	putSummary(t, st, "A-1", t0, groups.SummaryContent{Issue: schema.Issue{ID: "A-1", Title: "last words"}, Gone: true})
	got := renderRow(t, NewRenderer(st), store.ChangeRow{ID: "A-1", Version: 5})
	if got.Title != "last words" || got.Version != 5 {
		t.Errorf("entity = %+v, want the last stored summary at the row's version", got)
	}
}

func TestRenderer_ASummaryRowWithoutAFetchedTimeIsStale(t *testing.T) {
	st := openStore(t)
	putSummary(t, st, "A-1", time.Time{}, groups.SummaryContent{Issue: schema.Issue{ID: "A-1", Title: "t"}})
	got := renderRow(t, NewRenderer(st), store.ChangeRow{ID: "A-1", Version: 1})
	if !got.Stale || got.AsOf != "" {
		t.Errorf("entity = %+v, want empty as_of and stale", got)
	}
}

func TestRenderer_UndecodableStoredSummaryIsAnUnavailableError(t *testing.T) {
	st := openStore(t)
	if err := st.PutGroup(context.Background(), store.GroupRow{Kind: groups.EntityKind, ID: "A-1", Group: groups.Summary, Content: []byte("{not json"), FetchedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRenderer(st).Row(context.Background(), store.ChangeRow{ID: "A-1"}); err == nil {
		t.Error("an undecodable summary must be an error, not an empty entity")
	}
}

func TestRenderer_ReplayUsesTheEntitysCurrentVersionAndFoldsTheReference(t *testing.T) {
	r := newRig(t)
	r.seed()
	ent, ok, err := r.st.GetEntity(context.Background(), groups.EntityKind, "A-1")
	if err != nil || !ok {
		t.Fatalf("entity A-1: %v %v", ok, err)
	}
	got := renderReplay(t, NewRenderer(r.st), "a-1")
	if got.ID != "A-1" || got.Version != ent.Version || got.AsOf == "" {
		t.Errorf("replay entity = %+v, want A-1 at version %d with an as_of", got, ent.Version)
	}
	none := renderReplay(t, NewRenderer(r.st), "Z-9")
	if none.ID != "Z-9" || none.Version != 0 || !none.Stale {
		t.Errorf("replay of an unknown reference = %+v", none)
	}
}
