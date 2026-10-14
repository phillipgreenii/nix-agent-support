package passthrough

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestHandlers_ExactlySearchAndListActivityWithTheirOwnSchemaVersions(t *testing.T) {
	hs := Handlers(Deps{GH: &fakeGH{}})
	keys := make([]string, 0, len(hs))
	for k := range hs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"list_activity", "search"}; !slices.Equal(keys, want) {
		t.Fatalf("handler keys = %v, want %v (auth_status is the identity package's)", keys, want)
	}
	if got := hs["search"].SchemaVersion; got != schema.SearchSchemaVersion {
		t.Errorf("search schema version = %d, want SearchSchemaVersion %d", got, schema.SearchSchemaVersion)
	}
	if got := hs["list_activity"].SchemaVersion; got != schema.ActivitySchemaVersion {
		t.Errorf("list_activity schema version = %d, want ActivitySchemaVersion %d", got, schema.ActivitySchemaVersion)
	}
	for k, h := range hs {
		if h.Handle == nil {
			t.Errorf("%s has no handler", k)
		}
	}
	if !slices.Equal(Ops, []string{"search", "list_activity"}) {
		t.Errorf("Ops = %v", Ops)
	}
}

// ActivityKinds is the old backend's vocabulary, in its order.
func TestActivityKindsIsTheOldVocabulary(t *testing.T) {
	want := []string{"pr.opened", "pr.merged", "pr.closed", "pr.reviewed", "pr.commented"}
	if !slices.Equal(ActivityKinds, want) {
		t.Errorf("ActivityKinds = %v, want %v", ActivityKinds, want)
	}
}

// The old argument decoding comes through the reused dispatch tables.
func TestHandlers_DecodeArgumentsAsTheOldBackendDid(t *testing.T) {
	hs := Handlers(Deps{GH: &fakeGH{}})
	ctx := context.Background()
	for _, tc := range []struct {
		op, args string
	}{
		{"search", `{"query": 7}`},
		{"search", `{"query": "   "}`},
		{"list_activity", `{}`},
		{"list_activity", `{"before":"not a time"}`},
		{"list_activity", `{"since":"2026-10-01T00:00:00Z","before":"2026-09-01T00:00:00Z"}`},
	} {
		if _, err := hs[tc.op].Handle(ctx, json.RawMessage(tc.args)); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("%s %s: err = %v, want invalid_argument", tc.op, tc.args, err)
		}
	}
}

var asOfRe = regexp.MustCompile(`"as_of": ?"[^"]*"`)

// The golden files were produced by running the OLD backend (cmd/pg-connector-pr-github)
// over the same canned gh output (cannedGH); the daemon's answers equal them,
// apart from the read time.
func TestGolden_AnswersEqualTheOldBackends(t *testing.T) {
	h := newHarness(t, 100, cannedReply(false))
	for _, tc := range []struct{ op, args, golden string }{
		{"search", `{"query":"repo:o/r is:pr is:open"}`, "golden_search.json"},
		{"list_activity", activityArgs, "golden_list_activity.json"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			v, err := h.handlers()[tc.op].Handle(context.Background(), json.RawMessage(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = asOfRe.ReplaceAll(got, []byte(`"as_of": "<as_of>"`))
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			if err != nil {
				t.Fatal(err)
			}
			// Compared as JSON values: the formatter may lay the golden file out
			// differently from json.MarshalIndent.
			var gotV, wantV any
			if err := json.Unmarshal(got, &gotV); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want, &wantV); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotV, wantV) {
				t.Errorf("answer differs from %s:\n got: %s\nwant: %s", tc.golden, got, want)
			}
		})
	}
}

// dumpTables renders every table of the store: name, columns and every row.
func dumpTables(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = rows.Close()
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "table %s\n", n)
		r, err := db.Query("SELECT * FROM " + n)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "  %v\n", vals)
		}
		_ = r.Close()
	}
	return b.String()
}

// Nothing is stored: a daemon's store is identical before and after a call of each
// op. The handlers hold no store (Deps has none), and the daemon's own governor
// persists nothing through them here either, so this pins that no table of a store
// that sits next to them changes, and that the answer is not cached.
func TestHandlers_StoreTablesAreIdenticalAfterEachOp(t *testing.T) {
	st, _, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite3"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	before := dumpTables(t, st.DB())
	if !strings.Contains(before, "table entity_group") {
		t.Fatalf("the store dump does not list the entity tables:\n%s", before)
	}

	h := newHarness(t, 100, cannedReply(true))
	for _, tc := range []struct{ op, args string }{
		{"search", `{"query":"repo:o/r is:pr is:open"}`},
		{"list_activity", activityArgs},
	} {
		if _, err := h.handlers()[tc.op].Handle(context.Background(), json.RawMessage(tc.args)); err != nil {
			t.Fatalf("%s: %v", tc.op, err)
		}
		if after := dumpTables(t, st.DB()); after != before {
			t.Errorf("%s changed the store:\nbefore:\n%s\nafter:\n%s", tc.op, before, after)
		}
	}

	// Not cached either: a second identical call reaches gh again.
	calls := h.inner.count()
	if _, err := h.handlers()["search"].Handle(context.Background(), json.RawMessage(`{"query":"repo:o/r is:pr is:open"}`)); err != nil {
		t.Fatal(err)
	}
	if h.inner.count() != calls+1 {
		t.Errorf("a repeated search made %d gh calls, want 1 (nothing cached)", h.inner.count()-calls)
	}
}
