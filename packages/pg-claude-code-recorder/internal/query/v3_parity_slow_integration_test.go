//go:build integration

package query_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// The rebuilt version 3 file holds the same records and the same facts as the
// version 2 file, and answers every request of the representative set alike.
// The differences are exactly the ones the design lists, asserted below.
func TestV3RebuildHoldsTheSameRecordsFactsAndAnswersAsV2(t *testing.T) {
	v2, v3 := parityKits(t)
	if v2.Store.SchemaVersion(t.Context()) != store.SchemaVersion || v3.Store.SchemaVersion(t.Context()) != store.SchemaVersion {
		t.Fatalf("schema versions %d and %d", v2.Store.SchemaVersion(t.Context()), v3.Store.SchemaVersion(t.Context()))
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
