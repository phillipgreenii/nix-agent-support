package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const focusFullFacts = `{"issue_show":{"state":"open","status_category":"new","labels":["a","b"],"metadata":{"source_id":"7","n":3},` +
	`"assignee":"me","parent":"p-1","issue_type":"task","deps":[{"id":"x","type":"blocks"}],"due_date":"2026-11-01","priority":"2",` +
	`"created_at":"2026-10-01T00:00:00Z","title":"T","comments":[{"body":"big comment"}],"description":"long text"},` +
	`"pr_show":{"state":"open","merged":true,"review_requests":["r1"],"title":"PT","body":"long body"},` +
	`"thread_show":{"last_reply_at":"2026-11-01T00:00:00Z","messages":["m"]},"issue_deps":{"ids":["x"]}}`

func TestListEntitiesForFocusCarriesOnlyTheFocusMembers(t *testing.T) {
	s := OpenForTest(t)
	for id, facts := range map[string]string{"1": focusFullFacts, "2": "", "3": "not json", "4": `{"pr_show":{"state":"closed"}}`} {
		if err := s.UpsertEntity(Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: facts, AsOf: "2026-11-01T00:00:00Z", ContentHash: "h" + id}); err != nil {
			t.Fatalf("upsert facts %q: %v", facts, err)
		}
	}
	full, err := s.ListEntities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pruned, err := s.ListEntitiesForFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 4 || len(pruned) != 4 {
		t.Fatalf("%d full rows, %d pruned rows", len(full), len(pruned))
	}
	for i := range full {
		f, p := full[i], pruned[i]
		if f.EntityID != p.EntityID {
			t.Fatalf("row order differs: %s vs %s", f.EntityID, p.EntityID)
		}
		f.Facts, p.Facts = "", ""
		if f.EntityID != "" && !equalEntity(f, p) {
			t.Fatalf("row %s: non-facts fields differ:\n%+v\n%+v", f.EntityID, f, p)
		}
	}
	// Row 1: the members are carried verbatim and nothing else.
	var got map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(pruned[0].Facts), &got); err != nil {
		t.Fatalf("pruned facts do not decode: %v: %s", err, pruned[0].Facts)
	}
	for _, k := range []string{"comments", "description"} {
		if _, ok := got["issue_show"][k]; ok {
			t.Errorf("issue_show.%s must not be carried", k)
		}
	}
	if _, ok := got["pr_show"]["body"]; ok {
		t.Error("pr_show.body must not be carried")
	}
	want := map[string]string{
		"issue_show":  `{"state":"open","status_category":"new","labels":["a","b"],"metadata":{"source_id":"7","n":3},"assignee":"me","parent":"p-1","issue_type":"task","deps":[{"id":"x","type":"blocks"}],"due_date":"2026-11-01","priority":"2","created_at":"2026-10-01T00:00:00Z","title":"T"}`,
		"pr_show":     `{"state":"open","merged":true,"review_requests":["r1"],"title":"PT"}`,
		"thread_show": `{"last_reply_at":"2026-11-01T00:00:00Z"}`,
	}
	for section, w := range want {
		raw, _ := json.Marshal(got[section])
		var a, b any
		_ = json.Unmarshal(raw, &a)
		_ = json.Unmarshal([]byte(w), &b)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("%s = %s, want %s", section, ja, jb)
		}
	}
	// Empty and non-JSON facts yield an empty document, and writing them did not fail.
	if pruned[1].Facts != "" || pruned[2].Facts != "" {
		t.Errorf("empty/invalid facts pruned to %q and %q, want empty", pruned[1].Facts, pruned[2].Facts)
	}
}

func equalEntity(a, b Entity) bool {
	aw, bw := a.Watched, b.Watched
	a.Watched, b.Watched = nil, nil
	if (aw == nil) != (bw == nil) || (aw != nil && *aw != *bw) {
		return false
	}
	return a == b
}

func TestListEntitiesForFocusIsServedFromTheCoveringIndexAndReadsNoFacts(t *testing.T) {
	base := OpenForTest(t)
	base.SeedLiveSizeForTest(t, 200, 37000, 10)
	st, cnt := base.WithCounterForTest(t)
	rows, err := st.sql.QueryContext(context.Background(), `EXPLAIN QUERY PLAN SELECT repo, entity_type, entity_id, focus_facts, as_of, stale, content_hash, head_sha, version, hydrated_at, active, list_fp, first_seen_at, backend, watched, read_attempt_at, read_ok_at FROM entity INDEXED BY entity_focus ORDER BY repo, entity_type, entity_id`)
	if err != nil {
		t.Fatal(err)
	}
	var plan string
	for rows.Next() {
		var a, b, c int
		var d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		plan += d + "\n"
	}
	_ = rows.Close()
	if !strings.Contains(plan, "COVERING INDEX entity_focus") {
		t.Fatalf("plan does not use the covering index:\n%s", plan)
	}
	b0 := cnt.Bytes()
	got, err := st.ListEntitiesForFocus(context.Background())
	if err != nil || len(got) != 200 {
		t.Fatalf("ListEntitiesForFocus = %d rows, %v", len(got), err)
	}
	if read := cnt.Bytes() - b0; read > 1<<20 {
		t.Fatalf("read %d bytes for 200 rows of 37 KB facts (7.4 MB); the facts must not be read", read)
	}
}

// A store written before the column existed gains it, and its index, on the
// next ordinary open, with every existing row's pruned document populated and
// user_version untouched; a read-only open of such a store falls back to the
// full facts.
func TestFocusStorageIsAddedToAnExistingStoreWithoutBackfillOrVersionChange(t *testing.T) {
	path, before := legacyCutoverStore(t)
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ro.ListEntitiesForFocus(context.Background())
	_ = ro.Close()
	if err != nil || len(got) != 1 || got[0].Facts == "" {
		t.Fatalf("read-only fallback = (%+v, %v), want the one row with its full facts", got, err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if v := userVersion(t, s); v != before {
		t.Fatalf("user_version %d, want %d", v, before)
	}
	if err := s.UpsertEntity(Entity{Repo: "o/r", EntityType: "issue", EntityID: "z", Facts: focusFullFacts, AsOf: "2026-11-01T00:00:00Z", ContentHash: "h"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListEntitiesForFocus(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("ListEntitiesForFocus = (%d rows, %v)", len(got), err)
	}
	for _, e := range got {
		if e.EntityID == "z" && !strings.Contains(e.Facts, `"issue_show"`) {
			t.Errorf("row %s has no pruned document: %q", e.EntityID, e.Facts)
		}
	}
	// Idempotent.
	if err := ensureEntityFocusStorage(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// Rows that predate the column (focus_facts NULL) are backfilled by the ensure
// step, and a later write of the facts refreshes the document through the
// trigger.
func TestEnsureEntityFocusStorageBackfillsRowsAndTheTriggerKeepsThemCurrent(t *testing.T) {
	s := OpenForTest(t)
	if err := s.UpsertEntity(Entity{Repo: "o/r", EntityType: "pr", EntityID: "1", Facts: `{"pr_show":{"state":"open","title":"A"}}`, AsOf: "2026-11-01T00:00:00Z", ContentHash: "h1"}); err != nil {
		t.Fatal(err)
	}
	s.ExecForTest(t, `UPDATE entity SET focus_facts = NULL`)
	if err := ensureEntityFocusStorage(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListEntitiesForFocus(context.Background())
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Facts, `"title":"A"`) {
		t.Fatalf("after backfill = (%+v, %v)", got, err)
	}
	if err := s.UpsertEntity(Entity{Repo: "o/r", EntityType: "pr", EntityID: "1", Facts: `{"pr_show":{"state":"closed","title":"B"}}`, AsOf: "2026-11-02T00:00:00Z", ContentHash: "h2"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListEntitiesForFocus(context.Background())
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Facts, `"title":"B"`) || !strings.Contains(got[0].Facts, `"closed"`) {
		t.Fatalf("after update = (%+v, %v)", got, err)
	}
}
