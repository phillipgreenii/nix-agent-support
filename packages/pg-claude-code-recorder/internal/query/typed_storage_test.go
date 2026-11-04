package query_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

const typedFixture = `{"type":"user","uuid":"t1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","cwd":"/example/work/sub","gitBranch":"main","version":"1.0.0","entrypoint":"cli","userType":"external","message":{"role":"user","content":"hello"}}
{"type":"assistant","uuid":"t2","parentUuid":"t1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:01Z","cwd":"/example/work/sub","gitBranch":"main","version":"1.0.0","message":{"id":"m1","model":"example-model-a","role":"assistant","usage":{"input_tokens":3,"output_tokens":10,"cache_read_input_tokens":7,"service_tier":"standard"},"content":[{"type":"text","text":"answer one"}]}}
{"type":"assistant","uuid":"t3","parentUuid":"t2","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:02Z","cwd":"/example/other","gitBranch":"feature","version":"1.1.0","message":{"id":"m2","model":"example-model-b","role":"assistant","usage":{"input_tokens":4,"output_tokens":5,"service_tier":"priority","speed":"fast"},"content":[{"type":"text","text":"answer two"}]}}
{"type":"assistant","uuid":"t4","parentUuid":"t3","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:03Z","message":{"id":"m3","model":"example-model-a","role":"assistant","content":[{"type":"text","text":"no usage block"}]}}
`

// typedKits parses the same lines into the running layout (every value in
// fields) and the new layout (record: and ext: values in their own columns).
func typedKits(t *testing.T) (old, nw *testkit.Kit) {
	t.Helper()
	old = testkit.New(t)
	nw = testkit.NewWithStoreOptions(t, store.Options{ExtensionTables: true})
	for _, k := range []*testkit.Kit{old, nw} {
		k.Add("example-proj", "example-session-1.jsonl", typedFixture)
		k.Parse()
	}
	return old, nw
}

var typedRequests = []string{
	// filter: eq, in, contains, under, range, exists, not, any
	`{"where":{"field":"cwd","eq":"/example/work/sub"},"select":["uuid","cwd"],"order":[{"by":"uuid"}]}`,
	`{"where":{"field":"cwd","eq":"/nowhere"},"select":["uuid"]}`,
	`{"where":{"field":"git_branch","in":["main","absent"]},"select":["uuid","git_branch"],"order":[{"by":"uuid"}]}`,
	`{"where":{"field":"version","in":[]},"select":["uuid"]}`,
	`{"where":{"field":"cwd","contains":"work"},"select":["uuid"],"order":[{"by":"uuid"}]}`,
	`{"where":{"field":"cwd","under":"/example/work"},"select":["uuid"],"order":[{"by":"uuid"}]}`,
	`{"where":{"field":"version","range":{"gte":"1.1.0"}},"select":["uuid","version"],"order":[{"by":"uuid"}]}`,
	`{"where":{"field":"entrypoint","exists":true},"select":["uuid","entrypoint","user_type"]}`,
	`{"where":{"field":"git_branch","exists":false},"select":["uuid"],"order":[{"by":"uuid"}]}`,
	`{"where":{"not":{"field":"cwd","eq":"/example/other"}},"select":["uuid"],"order":[{"by":"uuid"}]}`,
	`{"where":{"any":[{"field":"cwd","eq":"/example/other"},{"field":"version","eq":"1.0.0"}]},"select":["uuid"],"order":[{"by":"uuid"}]}`,
	`{"where":{"field":"usage.output_tokens","range":{"gte":10}},"select":["uuid","usage.output_tokens"]}`,
	`{"where":{"field":"usage.output_tokens","eq":5},"select":["uuid"]}`,
	`{"where":{"field":"usage.input_tokens","in":[3,4]},"select":["uuid","usage.input_tokens"],"order":[{"by":"uuid"}]}`,
	`{"where":{"field":"usage.service_tier","eq":"priority"},"select":["uuid","usage.service_tier","usage.speed"]}`,
	`{"where":{"field":"usage.cache_read_input_tokens","exists":true},"select":["uuid","usage.cache_read_input_tokens"]}`,
	`{"where":{"field":"usage.cache_read_input_tokens","exists":false},"select":["uuid"],"order":[{"by":"uuid"}]}`,
	// select and order by typed fields: order sorts by value, not by id
	`{"select":["uuid","cwd","git_branch","usage.output_tokens"],"order":[{"by":"cwd"},{"by":"uuid"}]}`,
	`{"select":["uuid","version"],"order":[{"by":"version","dir":"desc"},{"by":"uuid"}]}`,
	`{"select":["uuid","usage.service_tier"],"order":[{"by":"usage.service_tier"},{"by":"uuid"}]}`,
	// group and aggregate
	`{"group":["cwd"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"cwd"}]}`,
	`{"where":{"field":"type","eq":"assistant"},"group":["version","model"],"aggregate":[{"fn":"sum","field":"usage.output_tokens","as":"out"},{"fn":"count","as":"n"}],"order":[{"by":"version"},{"by":"model"}]}`,
	`{"group":["usage.service_tier"],"aggregate":[{"fn":"sum","field":"usage.input_tokens","as":"in"},{"fn":"max","field":"usage.cache_read_input_tokens","as":"cr"}],"order":[{"by":"usage.service_tier"}]}`,
	`{"group":["entrypoint","user_type"],"aggregate":[{"fn":"count"}],"order":[{"by":"entrypoint"}]}`,
}

// A file in the new layout answers every request exactly as the running layout
// does: filter, group, order and select over the record: and ext: fields.
func TestTypedStorageAnswersEqualTheFieldsLayout(t *testing.T) {
	old, nw := typedKits(t)
	if !nw.Store.HasExt() || old.Store.HasExt() {
		t.Fatal("the fixtures are not in the layouts under test")
	}
	// The new file stores the usage values in the extension table, not in fields.
	if n := nw.Count(`SELECT COUNT(*) FROM ext_assistant_usage`); n != 2 {
		t.Fatalf("ext rows %d, want 2", n)
	}
	if n := nw.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name LIKE '%usage.%'`); n != 0 {
		t.Fatalf("%d usage fields rows in the new layout", n)
	}
	if n := old.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name LIKE '%usage.%'`); n == 0 {
		t.Fatal("the running layout lost its usage fields rows")
	}
	for _, req := range typedRequests {
		want, got := js(q(t, old, req)), js(q(t, nw, req))
		if strings.ReplaceAll(want, `"as_of":`, "") == "" {
			t.Fatal("empty answer")
		}
		if stripAsOf(want) != stripAsOf(got) {
			t.Errorf("%s\n fields: %s\n typed:  %s", req, want, got)
		}
	}
	// Equality over nonempty answers, not two empty results.
	for _, i := range []int{0, 2, 7, 11, 14, 17, 20, 21} {
		if a := q(t, nw, typedRequests[i]); len(a.Rows) == 0 {
			t.Errorf("%s: no rows", typedRequests[i])
		}
	}
}

func stripAsOf(s string) string {
	i := strings.Index(s, `"as_of":"`)
	if i < 0 {
		return s
	}
	j := strings.Index(s[i+9:], `"`)
	return s[:i] + s[i+9+j+1:]
}

// The new layout reads the interned id, not the fields row: with the row gone
// the value is still answered; the running layout never reads the id.
func TestRecordFieldsAreReadFromTheLookupIds(t *testing.T) {
	old, nw := typedKits(t)
	for _, k := range []*testkit.Kit{old, nw} {
		err := k.Store.WithWrite("", func(tx *sql.Tx) error {
			_, err := tx.Exec(`DELETE FROM fields WHERE name_id IN (SELECT id FROM field_names WHERE name IN ('cwd','git_branch','version'))`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := `{"where":{"field":"cwd","eq":"/example/work/sub"},"select":["uuid","cwd","git_branch","version"],"order":[{"by":"uuid"}]}`
	if a := q(t, nw, req); len(a.Rows) != 2 || a.Rows[0][1] != "/example/work/sub" || a.Rows[1][3] != "1.0.0" {
		t.Fatalf("new layout: %s", js(a))
	}
	if a := q(t, old, req); len(a.Rows) != 0 {
		t.Fatalf("the running layout read something other than fields: %s", js(a))
	}
}

// A raw value in a fields row (the conversion was lossy, so the typed column is
// NULL) is a present value to select, group, order and exists, and never
// satisfies a typed comparison.
func TestMismatchRawValueIsPresentNotMissing(t *testing.T) {
	_, nw := typedKits(t)
	missing := len(q(t, nw, `{"where":{"field":"usage.output_tokens","exists":false},"select":["uuid"]}`).Rows)
	err := nw.Store.WithWrite("", func(tx *sql.Tx) error {
		for _, s := range []string{
			`UPDATE ext_assistant_usage SET output_tokens=NULL WHERE input_tokens=3`,
			`INSERT INTO field_names(name) VALUES('message.usage.output_tokens') ON CONFLICT(name) DO NOTHING`,
			`INSERT INTO fields(rec_id,name_id,s) SELECT rec_id,(SELECT id FROM field_names WHERE name='message.usage.output_tokens'),'1e999' FROM ext_assistant_usage WHERE output_tokens IS NULL`,
		} {
			if _, err := tx.Exec(s); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if nw.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name='message.usage.output_tokens'`) != 1 {
		t.Fatal("the mismatch row was not written")
	}
	a := q(t, nw, `{"where":{"field":"usage.output_tokens","exists":true},"select":["uuid","usage.output_tokens"],"order":[{"by":"uuid"}]}`)
	if len(a.Rows) != 2 || a.Rows[0][1] != "1e999" || a.Rows[1][1] != int64(5) {
		t.Fatalf("exists/select: %s", js(a))
	}
	a = q(t, nw, `{"where":{"field":"usage.output_tokens","exists":false},"select":["uuid"]}`)
	if len(a.Rows) != missing {
		t.Fatalf("a mismatch looks missing: %s", js(a))
	}
	a = q(t, nw, `{"where":{"field":"usage.output_tokens","range":{"gte":0}},"select":["uuid"]}`)
	if len(a.Rows) != 1 {
		t.Fatalf("a raw value satisfied a typed comparison: %s", js(a))
	}
	a = q(t, nw, `{"group":["usage.output_tokens"],"aggregate":[{"fn":"count","as":"n"}],"where":{"field":"usage.output_tokens","exists":true},"order":[{"by":"usage.output_tokens"}]}`)
	if len(a.Rows) != 2 {
		t.Fatalf("group: %s", js(a))
	}
}
