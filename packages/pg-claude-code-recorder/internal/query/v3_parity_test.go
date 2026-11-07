package query_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// parityKits rebuilds one corpus into the version 2 layout (the default store
// options: session text, trigram index, every value in fields) and the version
// 3 layout (the option the daemon sets), by the real parser over the real
// copies. The corpus has several sessions, a subagent file, a line with no
// session id, a tool result body, typed lookup values and usage counters.
func parityKits(t *testing.T) (v2, v3 *testkit.Kit) {
	t.Helper()
	v2 = testkit.NewWithStoreOptions(t, store.Options{})
	v3 = testkit.NewWithStoreOptions(t, store.Options{V3Layout: true})
	for _, k := range []*testkit.Kit{v2, v3} {
		k.Add("example-proj", "example-session-1.jsonl", fixture)
		k.AddAgent("example-proj", "example-session-1", "a1", `{"type":"assistant","uuid":"x1","isSidechain":true,"sessionId":"example-session-1","timestamp":"2026-10-29T20:01:00Z","message":{"id":"m9","role":"assistant","content":[{"type":"text","text":"registry subagent words"}]}}`+"\n")
		k.Add("example-proj", "example-session-2.jsonl", strings.ReplaceAll(fixture, "example-session-1", "example-session-2"))
		k.Add("example-proj", "example-session-3.jsonl", `{"type":"user","uuid":"n1","timestamp":"2026-10-29T21:00:00Z","message":{"role":"user","content":"a line with no session id registry"}}`+"\n")
		k.Add("example-proj", "example-session-4.jsonl", strings.ReplaceAll(typedFixture, "example-session-1", "example-session-4"))
		k.AddToolResultBody("example-proj", "example-session-2", "body.txt", "tool body with the word needle inside")
		k.Parse()
	}
	return v2, v3
}

func answerJSON(t *testing.T, e *query.Engine, req string) string {
	t.Helper()
	var r query.Request
	if err := json.Unmarshal([]byte(req), &r); err != nil {
		t.Fatal(err)
	}
	a, err := e.Query(context.Background(), r)
	if err != nil {
		t.Fatalf("%s: %v", req, err)
	}
	a.AsOf = ""
	return js(a)
}

func rowsOf(t *testing.T, k *testkit.Kit, qs string) string {
	t.Helper()
	rs, err := k.Store.Read().Query(qs)
	if err != nil {
		t.Fatalf("%s: %v", qs, err)
	}
	defer func() { _ = rs.Close() }()
	var out []string
	for rs.Next() {
		var a, b any
		if err := rs.Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%v=%v", a, b))
	}
	return strings.Join(out, ";")
}

func hasTable(k *testkit.Kit, name string) bool {
	return k.Count(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name) > 0
}

func hasColumn(k *testkit.Kit, table, col string) bool {
	return k.Count(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, col) > 0
}

func hasIndex(k *testkit.Kit, name string) bool {
	return k.Count(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name) > 0
}

// The rebuilt version 3 file holds the same records and the same facts as the
// version 2 file, and answers every request of the representative set alike.
// The differences are exactly the ones the design lists, asserted below.
func TestV3RebuildHoldsTheSameRecordsFactsAndAnswersAsV2(t *testing.T) {
	v2, v3 := parityKits(t)
	if v2.Store.SchemaVersion() != store.SchemaVersion || v3.Store.SchemaVersion() != store.SchemaVersion {
		t.Fatalf("schema versions %d and %d", v2.Store.SchemaVersion(), v3.Store.SchemaVersion())
	}
	if v2.Store.HasSessionText() == v3.Store.HasSessionText() {
		t.Fatal("the two stores are not in different layouts")
	}

	// Record counts: total, per source and type.
	n2, n3 := v2.Count(`SELECT COUNT(*) FROM records`), v3.Count(`SELECT COUNT(*) FROM records`)
	if n2 == 0 || n2 != n3 {
		t.Fatalf("records v2 %d, v3 %d", n2, n3)
	}
	byType := `SELECT source||'/'||type, COUNT(*) FROM records GROUP BY 1 ORDER BY 1`
	if a, b := rowsOf(t, v2, byType), rowsOf(t, v3, byType); a == "" || a != b {
		t.Fatalf("records by source and type\n v2 %s\n v3 %s", a, b)
	}

	// Per-session record counts and facts: the v2 text column against the v3
	// reference, and the derived session tables of both.
	perSession2 := `SELECT session, COUNT(*) FROM records GROUP BY session ORDER BY session`
	perSession3 := `SELECT s.session_id, COUNT(*) FROM records r JOIN sessions s ON s.session_ref=r.session_ref GROUP BY s.session_id ORDER BY s.session_id`
	want := rowsOf(t, v2, perSession2)
	if want == "" || want != rowsOf(t, v3, perSession3) {
		t.Fatalf("per-session counts\n v2 %s\n v3 %s", want, rowsOf(t, v3, perSession3))
	}
	stats := `SELECT s.session_id, st.record_count||'/'||st.last_time_ns FROM session_stats st JOIN sessions s ON s.session_ref=st.session_ref ORDER BY s.session_id`
	if a, b := rowsOf(t, v2, stats), rowsOf(t, v3, stats); a == "" || a != b {
		t.Fatalf("session_stats\n v2 %s\n v3 %s", a, b)
	}
	// session_stats equals what records say, in both layouts.
	if got := rowsOf(t, v3, `SELECT s.session_id, st.record_count FROM session_stats st JOIN sessions s ON s.session_ref=st.session_ref ORDER BY s.session_id`); got != want {
		t.Fatalf("v3 session_stats do not match its records\n stats   %s\n records %s", got, want)
	}
	files := `SELECT s.session_id, COUNT(*) FROM session_files sf JOIN sessions s ON s.session_ref=sf.session_ref GROUP BY s.session_id ORDER BY s.session_id`
	if a, b := rowsOf(t, v2, files), rowsOf(t, v3, files); a == "" || a != b {
		t.Fatalf("session_files\n v2 %s\n v3 %s", a, b)
	}
	// The lookup ids resolve to the same values record for record.
	look := `SELECT r.item||'/'||r.off||'/'||r.file_id, COALESCE((SELECT v FROM lk_cwd WHERE id=r.cwd_id),'')||'|'||COALESCE((SELECT v FROM lk_branch WHERE id=r.branch_id),'')||'|'||COALESCE((SELECT v FROM lk_version WHERE id=r.version_id),'') FROM records r ORDER BY r.file_id, r.off, r.item`
	if a, b := rowsOf(t, v2, look), rowsOf(t, v3, look); a == "" || a != b {
		t.Fatalf("lookup values\n v2 %s\n v3 %s", a, b)
	}

	// Query results: the session requests, the typed-field requests and the
	// searches, over both stores.
	// The last session request follows a link type the corpus has no record of.
	reqs := append([]string(nil), sessionRequests[:len(sessionRequests)-1]...)
	reqs = append(reqs, typedRequests...)
	reqs = append(
		reqs,
		`{"where":{"field":"type","eq":"assistant"},"select":["uuid","session_id","model"],"order":[{"by":"uuid"}]}`,
		`{"group":["source","type"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"source"},{"by":"type"}]}`,
		`{"where":{"field":"cwd","under":"/example/work"},"group":["session_id"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"session_id"}]}`,
	)
	nonEmpty := 0
	for _, req := range reqs {
		a, b := answerJSON(t, v2.Engine, req), answerJSON(t, v3.Engine, req)
		if a != b {
			t.Errorf("%s\n v2: %s\n v3: %s", req, a, b)
		}
		if strings.Contains(a, `"rows":[[`) {
			nonEmpty++
		}
	}
	if nonEmpty < len(reqs)/2 {
		t.Fatalf("only %d of %d requests returned rows: equality proves little", nonEmpty, len(reqs))
	}
	for _, req := range []string{
		`{"q":"registry"}`,
		`{"q":"registry","where":{"field":"session_id","eq":"example-session-1"}}`,
		`{"mode":"substring","q":"git commit"}`,
		`{"mode":"substring","q":"needle"}`,
		`{"mode":"substring","q":"git commit","where":{"field":"session_id","in":["example-session-1","example-session-2"]}}`,
	} {
		var r query.SearchRequest
		if err := json.Unmarshal([]byte(req), &r); err != nil {
			t.Fatal(err)
		}
		run := func(e *query.Engine) (string, int) {
			a, err := e.Search(context.Background(), r)
			if err != nil {
				t.Fatalf("%s: %v", req, err)
			}
			// Intentional difference: the v3 word index also holds the opaque
			// fields (tool bodies), so its bm25 statistics, and the score, differ.
			// The hits, their order and their snippets are what must be equal.
			for i := range a.Hits {
				if r.Mode != "substring" && a.Hits[i].Score <= 0 {
					t.Errorf("%s: hit %d has no score", req, i)
				}
				a.Hits[i].Score = 0
			}
			return js(a), len(a.Hits)
		}
		a, na := run(v2.Engine)
		b, _ := run(v3.Engine)
		if na == 0 {
			t.Errorf("%s: no hits, equality proves nothing", req)
		}
		if a != b {
			t.Errorf("%s\n v2: %s\n v3: %s", req, a, b)
		}
	}
}

// The intentional differences between the two layouts, and nothing else in the
// table set: asserting them keeps the parity test from hiding a regression
// behind "different layout".
func TestV3LayoutDifferencesAreExactlyTheDesignedOnes(t *testing.T) {
	v2, v3 := parityKits(t)
	type diff struct {
		what     string
		v2, v3   bool
		describe string
	}
	for _, d := range []diff{
		{"records.session column", hasColumn(v2, "records", "session"), hasColumn(v3, "records", "session"), "dropped: the session is read through session_ref"},
		{"records_session index", hasIndex(v2, "records_session"), hasIndex(v3, "records_session"), "dropped with the column"},
		{"records_session_source_time index", hasIndex(v2, "records_session_source_time"), hasIndex(v3, "records_session_source_time"), "dropped with the column"},
		{"fts_tri table", hasTable(v2, "fts_tri"), hasTable(v3, "fts_tri"), "dropped: opaque fields are in the word index"},
		{"ext_assistant_usage table", hasTable(v2, "ext_assistant_usage"), hasTable(v3, "ext_assistant_usage"), "added"},
		{"records_cwd index", hasIndex(v2, "records_cwd"), hasIndex(v3, "records_cwd"), "added"},
		{"records_branch index", hasIndex(v2, "records_branch"), hasIndex(v3, "records_branch"), "added"},
	} {
		wantV2 := d.what == "records.session column" || d.what == "records_session index" || d.what == "records_session_source_time index" || d.what == "fts_tri table"
		if d.v2 != wantV2 || d.v3 == wantV2 {
			t.Errorf("%s: v2 %v, v3 %v (%s)", d.what, d.v2, d.v3, d.describe)
		}
	}
	// The session_ref index serves both layouts' reads.
	if !hasIndex(v3, "records_session_ref_source_time") {
		t.Error("v3 lacks the session_ref index")
	}
	// Usage counters moved out of fields into the extension table.
	usage := `SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name LIKE '%usage.%'`
	if v2.Count(usage) == 0 || v3.Count(usage) != 0 || v3.Count(`SELECT COUNT(*) FROM ext_assistant_usage`) == 0 {
		t.Error("usage counters are not in the extension table of v3 only")
	}
	// Everything else a reader sees is the same set of tables.
	names := func(k *testkit.Kit) string {
		return rowsOf(t, k, `SELECT name, type FROM sqlite_master WHERE type='table' AND name NOT LIKE 'fts_%' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'ext_%' ORDER BY name`)
	}
	if a, b := names(v2), names(v3); a != b {
		t.Errorf("table sets differ beyond the designed differences\n v2 %s\n v3 %s", a, b)
	}
}
