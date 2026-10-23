package serve_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite" // edits the copy of the fixture

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
)

// A store that an older binary left behind, opened through the daemon's own open
// path (serve.Run, the store.Open call of serve.go with its Expiry and Progress),
// serves a changes poll with the same answer whatever the ladder's top is: a rung
// that grows the ladder must not change what an existing store answers.
//
// The store is the committed version 3 fixture of the store package (the ladder
// as it stood before the hoist added rungs). The scenario is one changes poll by
// a fresh consumer over the data already inside that fixture, once the daemon's
// own first runs have settled.
//
// The expected answer, testdata/changes_v3_expected.json, was generated ONCE
// against the version 3 ladder, before any later rung existed, and is never
// regenerated to make a rung pass. To write it again (a deliberate change of the
// scenario only) run
//
//	PG_CONNECTOR_SERVE_UPDATE_EXPECTATIONS=1 go test ./cmd/pg-connector-github/internal/serve -run TestStoreFixtureV3
//
// The comparison normalises what a later rung adds or what varies by run: the
// store identity (its value is replaced by a fixed token after the test has
// asserted it is a non-empty string), the schema-version fields, and the time of
// a query's last success.

const updateExpectationsEnv = "PG_CONNECTOR_SERVE_UPDATE_EXPECTATIONS"

const (
	v3FixturePath    = "../../../../pkg/connectord/store/testdata/v3.sqlite3"
	v3ExpectedPath   = "testdata/changes_v3_expected.json"
	identityToken    = "(store-identity)"
	timeToken        = "(time)"
	storeIdentityKey = "store_identity"
)

// normaliseAnswer returns the decoded answer with the volatile and
// later-rung fields replaced by fixed tokens.
func normaliseAnswer(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode the changes answer: %v", err)
	}
	if v, ok := doc[storeIdentityKey]; ok {
		if s, isString := v.(string); !isString || s == "" {
			t.Fatalf("%s = %#v, want a non-empty string", storeIdentityKey, v)
		}
		doc[storeIdentityKey] = identityToken
	}
	for k := range doc {
		if k == "schema_version" || k == "store_schema_version" {
			delete(doc, k)
		}
	}
	if rows, ok := doc["sources_freshness"].([]any); ok {
		for _, r := range rows {
			if m, isMap := r.(map[string]any); isMap {
				if _, has := m["last_success_at"]; has {
					m["last_success_at"] = timeToken
				}
			}
		}
	}
	return doc
}

func TestStoreFixtureV3_ServesAChangesPollWithTheSameAnswerAsBeforeTheLaterRungs(t *testing.T) {
	fixture, err := os.ReadFile(v3FixturePath)
	if err != nil {
		t.Fatalf("read the version 3 store fixture: %v", err)
	}
	d := servetest.NewDaemon(t, servetest.Options{Before: func(d *servetest.Daemon) {
		seed3a(d)
		if err := os.MkdirAll(d.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.StorePath(), fixture, 0o600); err != nil {
			t.Fatal(err)
		}
		dropSyntheticPendingRow(t, d.StorePath())
	}})
	p := runInProcess(t, d, nil)
	defer p.stop(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine)

	resp := pollChanges(t, d, "pr", "example-serve", servetest.QueryMine, nil)
	if resp.Error != nil {
		t.Fatalf("changes poll over the version 3 store: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	got := normaliseAnswer(t, resp.Result)

	if os.Getenv(updateExpectationsEnv) != "" {
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(v3ExpectedPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(v3ExpectedPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", v3ExpectedPath)
		return
	}

	raw, err := os.ReadFile(v3ExpectedPath)
	if err != nil {
		t.Fatalf("read the expected answer (generate it with %s=1): %v", updateExpectationsEnv, err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		t.Errorf("the changes answer over the version 3 store differs from the committed one.\ngot:\n%s\nwant:\n%s", gotJSON, raw)
	}
}

// dropSyntheticPendingRow removes the fixture's pending change row from the
// COPY the daemon opens. The store fixture's sample pending row carries a
// hand-written awaited document the change engine never writes (its Since is a
// number, the engine's is a time string), and the daemon refuses to start while
// it recovers such a row. The tables the answer reads (the log, the consumers,
// the queries) are untouched.
func dropSyntheticPendingRow(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM pending_change`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
