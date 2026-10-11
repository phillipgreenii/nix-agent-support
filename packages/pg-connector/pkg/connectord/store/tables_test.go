package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The fixture store for schema version N is testdata/vN.sqlite3: a store as the
// binary that shipped version N would have left it, with one sample row in
// every table so a later migration can be checked to keep data. It is
// generated, never hand-edited: run
//
//	PG_CONNECTOR_STORE_UPDATE_FIXTURES=1 go test ./pkg/connectord/store -run TestGenerateFixture
//
// and commit the result. A shipped fixture MUST NOT be regenerated: a new
// schema version adds a new vN.sqlite3 instead.
const updateFixturesEnv = "PG_CONNECTOR_STORE_UPDATE_FIXTURES"

func TestGenerateFixture(t *testing.T) {
	if os.Getenv(updateFixturesEnv) == "" {
		t.Skip("set " + updateFixturesEnv + "=1 to regenerate the current version's fixture")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "gen.db")
	s, _ := mustOpen(t, path, fastOpts())
	for _, q := range []string{
		`INSERT INTO query VALUES ('open-prs:org/repo', 1, '["PR_1","PR_2"]', 1, 1760000000000, 1760000060000, 1760000000000, '')`,
		`INSERT INTO alias VALUES ('PR_1', 'org/repo#1', 1760000000000)`,
		`INSERT INTO entity VALUES ('pr', 'PR_1', 3, 1760000000000, 1, 'MERGEABLE', NULL)`,
		`INSERT INTO entity_group (kind, id, group_name, content, fingerprint, fetched_at, fresh_until, max_age_at,
		   last_error, generation, fetched_for_head, fetched_for_base)
		 VALUES ('pr', 'PR_1', 'summary', x'7b227469746c65223a2253616d706c65227d', 'fp-1', 1760000000000, 1760000060000,
		   1760003600000, '', 2, 'head-sha', 'base-sha')`,
		`INSERT INTO budget (bucket, at, remaining, reset_at, spent, purpose)
		 VALUES ('graphql', 1760000000000, 4900, 1760003600000, 100, 'background')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "v1.sqlite3"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertFixtureData checks the sample rows of the version 1 fixture through
// the typed accessors.
func assertFixtureData(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	q, ok, err := s.GetQuery(ctx, "open-prs:org/repo")
	if err != nil || !ok || !q.Configured || !q.Complete || !slices.Equal(q.IDs, []string{"PR_1", "PR_2"}) ||
		q.FreshUntil.UnixMilli() != 1760000060000 {
		t.Errorf("query = %+v ok=%v err=%v", q, ok, err)
	}
	a, ok, err := s.AliasByRef(ctx, "org/repo#1")
	if err != nil || !ok || a.NodeID != "PR_1" {
		t.Errorf("alias = %+v ok=%v err=%v", a, ok, err)
	}
	e, ok, err := s.GetEntity(ctx, "pr", "PR_1")
	if err != nil || !ok || e.Version != 3 || !e.KeepAlive || e.LastKnownMergeable != "MERGEABLE" || !e.TombstonedAt.IsZero() {
		t.Errorf("entity = %+v ok=%v err=%v", e, ok, err)
	}
	g, ok, err := s.GetGroup(ctx, "pr", "PR_1", "summary")
	if err != nil || !ok || string(g.Content) != `{"title":"Sample"}` || g.Fingerprint != "fp-1" || g.Generation != 2 ||
		g.FetchedForHead != "head-sha" || g.FetchedForBase != "base-sha" || g.MaxAgeAt.UnixMilli() != 1760003600000 {
		t.Errorf("group = %+v ok=%v err=%v", g, ok, err)
	}
	b, ok, err := s.LatestBudget(ctx, "graphql")
	if err != nil || !ok || b.Remaining != 4900 || b.Spent != 100 || b.Purpose != "background" {
		t.Errorf("budget = %+v ok=%v err=%v", b, ok, err)
	}
}

func TestQueryAccessors(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())

	if _, ok, err := s.GetQuery(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing query: ok=%v err=%v", ok, err)
	}
	fetched := time.Date(2026, 10, 11, 1, 2, 3, 0, time.UTC)
	for _, q := range []QueryRow{
		{
			Key: "b", Configured: true, IDs: []string{"3", "1", "2"}, Complete: true, FetchedAt: fetched,
			FreshUntil: fetched.Add(time.Minute), LastSuccessAt: fetched,
		},
		{Key: "a", IDs: nil, LastError: "upstream timeout"},
	} {
		if err := s.PutQuery(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	b, ok, err := s.GetQuery(ctx, "b")
	if err != nil || !ok || !slices.Equal(b.IDs, []string{"3", "1", "2"}) || !b.FetchedAt.Equal(fetched) || !b.Configured {
		t.Errorf("b = %+v ok=%v err=%v (id order must be preserved)", b, ok, err)
	}
	a, _, _ := s.GetQuery(ctx, "a")
	if a.IDs == nil || len(a.IDs) != 0 || !a.FetchedAt.IsZero() || a.LastError != "upstream timeout" {
		t.Errorf("a = %+v, want empty non-nil ids, zero times", a)
	}
	// Upsert replaces.
	if err := s.PutQuery(ctx, QueryRow{Key: "b", IDs: []string{"9"}}); err != nil {
		t.Fatal(err)
	}
	if b, _, _ = s.GetQuery(ctx, "b"); !slices.Equal(b.IDs, []string{"9"}) || b.Configured {
		t.Errorf("after upsert b = %+v", b)
	}
	all, err := s.ListQueries(ctx, false)
	if err != nil || len(all) != 2 || all[0].Key != "a" || all[1].Key != "b" {
		t.Errorf("all = %+v err=%v", all, err)
	}
	if err := s.PutQuery(ctx, QueryRow{Key: "c", Configured: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.ListQueries(ctx, true)
	if err != nil || len(cfg) != 1 || cfg[0].Key != "c" {
		t.Errorf("configured = %+v err=%v", cfg, err)
	}
	if err := s.DeleteQuery(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetQuery(ctx, "c"); ok {
		t.Error("c survived DeleteQuery")
	}
}

func TestAliasAccessors(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if _, ok, err := s.AliasByNodeID(ctx, "nope"); err != nil || ok {
		t.Fatalf("missing alias: ok=%v err=%v", ok, err)
	}
	for _, a := range []AliasRow{
		{NodeID: "N1", Ref: "org/repo#7", SeenAt: time.UnixMilli(100)},
		{NodeID: "N2", Ref: "org/repo#7", SeenAt: time.UnixMilli(200)}, // a re-created node
	} {
		if err := s.PutAlias(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if a, ok, _ := s.AliasByRef(ctx, "org/repo#7"); !ok || a.NodeID != "N2" {
		t.Errorf("by ref = %+v, want the most recently seen node", a)
	}
	if err := s.PutAlias(ctx, AliasRow{NodeID: "N1", Ref: "org/other#1", SeenAt: time.UnixMilli(300)}); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := s.AliasByNodeID(ctx, "N1"); a.Ref != "org/other#1" || a.SeenAt.UnixMilli() != 300 {
		t.Errorf("after upsert N1 = %+v", a)
	}
	if err := s.DeleteAlias(ctx, "N1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.AliasByNodeID(ctx, "N1"); ok {
		t.Error("N1 survived DeleteAlias")
	}
}

func TestEntityAndGroupAccessors(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	now := time.Date(2026, 10, 11, 1, 2, 3, 0, time.UTC)

	if _, ok, err := s.GetEntity(ctx, "pr", "x"); err != nil || ok {
		t.Fatalf("missing entity: ok=%v err=%v", ok, err)
	}
	for _, id := range []string{"B", "A"} {
		if err := s.PutEntity(ctx, EntityRow{Kind: "pr", ID: id, Version: 1, LastAccess: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutEntity(ctx, EntityRow{Kind: "other", ID: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEntity(ctx, EntityRow{
		Kind: "pr", ID: "A", Version: 2, KeepAlive: true, TombstonedAt: now,
		LastKnownMergeable: "CONFLICTING",
	}); err != nil {
		t.Fatal(err)
	}
	e, _, _ := s.GetEntity(ctx, "pr", "A")
	if e.Version != 2 || !e.KeepAlive || !e.TombstonedAt.Equal(now) || e.LastKnownMergeable != "CONFLICTING" || !e.LastAccess.IsZero() {
		t.Errorf("entity A = %+v", e)
	}
	list, err := s.ListEntities(ctx, "pr")
	if err != nil || len(list) != 2 || list[0].ID != "A" || list[1].ID != "B" {
		t.Errorf("list = %+v err=%v (kind-scoped, ordered by id)", list, err)
	}

	for _, g := range []string{"summary", "detail"} {
		if err := s.PutGroup(ctx, GroupRow{
			Kind: "pr", ID: "A", Group: g, Content: []byte(g), Fingerprint: "f-" + g,
			FetchedAt: now, FreshUntil: now.Add(time.Minute), MaxAgeAt: now.Add(time.Hour), Generation: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutGroup(ctx, GroupRow{Kind: "pr", ID: "B", Group: "summary", Content: []byte("b")}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, GroupRow{
		Kind: "pr", ID: "A", Group: "summary", Content: []byte("v2"), Generation: 2,
		LastError: "stale",
	}); err != nil {
		t.Fatal(err)
	}
	g, ok, err := s.GetGroup(ctx, "pr", "A", "summary")
	if err != nil || !ok || string(g.Content) != "v2" || g.Generation != 2 || g.LastError != "stale" || !g.FetchedAt.IsZero() {
		t.Errorf("group = %+v ok=%v err=%v (upsert replaces every column)", g, ok, err)
	}
	groups, err := s.ListGroups(ctx, "pr", "A")
	if err != nil || len(groups) != 2 || groups[0].Group != "detail" || groups[1].Group != "summary" {
		t.Errorf("groups = %+v err=%v", groups, err)
	}
	if err := s.DeleteGroup(ctx, "pr", "A", "detail"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetGroup(ctx, "pr", "A", "detail"); ok {
		t.Error("detail survived DeleteGroup")
	}

	if err := s.DeleteEntity(ctx, "pr", "A"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetEntity(ctx, "pr", "A"); ok {
		t.Error("entity A survived")
	}
	if gs, _ := s.ListGroups(ctx, "pr", "A"); len(gs) != 0 {
		t.Errorf("DeleteEntity left groups: %+v", gs)
	}
	if _, ok, _ := s.GetGroup(ctx, "pr", "B", "summary"); !ok {
		t.Error("DeleteEntity removed another entity's group")
	}
	if _, ok, _ := s.GetEntity(ctx, "other", "A"); !ok {
		t.Error("DeleteEntity removed another kind's entity")
	}
}

func TestBudgetAccessors(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	t0 := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	if _, ok, err := s.LatestBudget(ctx, "graphql"); err != nil || ok {
		t.Fatalf("empty bucket: ok=%v err=%v", ok, err)
	}
	for i, remaining := range []int64{5000, 4800, 4700} {
		if err := s.AppendBudget(ctx, BudgetSample{
			Bucket: "graphql", At: t0.Add(time.Duration(i) * time.Minute),
			Remaining: remaining, ResetAt: t0.Add(time.Hour), Spent: 5000 - remaining, Purpose: "background",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Same instant as the latest: insertion order breaks the tie.
	if err := s.AppendBudget(ctx, BudgetSample{Bucket: "graphql", At: t0.Add(2 * time.Minute), Remaining: 4600}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendBudget(ctx, BudgetSample{Bucket: "search", At: t0, Remaining: 30}); err != nil {
		t.Fatal(err)
	}
	latest, ok, err := s.LatestBudget(ctx, "graphql")
	if err != nil || !ok || latest.Remaining != 4600 || !latest.ResetAt.IsZero() {
		t.Errorf("latest = %+v ok=%v err=%v", latest, ok, err)
	}
	since, err := s.BudgetSince(ctx, "graphql", t0.Add(time.Minute))
	if err != nil || len(since) != 3 || since[0].Remaining != 4800 || since[2].Remaining != 4600 {
		t.Errorf("since = %+v err=%v", since, err)
	}
	n, err := s.PruneBudgetBefore(ctx, t0.Add(time.Minute))
	if err != nil || n != 2 { // graphql t0 and search t0
		t.Errorf("pruned %d err=%v, want 2", n, err)
	}
	if _, ok, _ := s.LatestBudget(ctx, "search"); ok {
		t.Error("search sample survived the prune")
	}
}
