package query_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

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

func rowsOf(t *testing.T, k *testkit.Kit, qs string) string {
	t.Helper()
	rs, err := k.Store.Read().QueryContext(context.Background(), qs)
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
