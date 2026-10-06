package freshness

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func ts(d time.Duration) *time.Time { u := t0.Add(d); return &u }

func ledgerRow(backend, query string, refreshed *time.Time) LedgerRow {
	return LedgerRow{Type: "pr", Backend: backend, Query: query, RefreshedAt: refreshed}
}

func mustUpdate(t *testing.T, st *store.Store, rows ...LedgerRow) {
	t.Helper()
	if err := Update(st, rows); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func mustSources(t *testing.T, st *store.Store, cfg *config.Config, now time.Time) []Row {
	t.Helper()
	rows, err := Sources(st, cfg, now)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	return rows
}

func TestParseLedgerShow(t *testing.T) {
	// The exact shape `pg-connector ledger show` prints: refreshed_at and
	// last_error are present and null when absent (INV-LEDGER-FRESH-4).
	rows, err := ParseLedgerShow([]byte(`[
	  {"type":"pr","backend":"pg-connector-pr-github","query":"mine","cursor":null,"index_size":3,"version":9,"consumers":{},
	   "refreshed_at":"2026-10-06T11:59:00Z","last_error":{"at":"2026-10-06T11:58:00Z","code":"unavailable"}},
	  {"type":"pr","backend":"pg-connector-pr-github","query":"team","cursor":null,"index_size":0,"version":0,"consumers":{},
	   "refreshed_at":null,"last_error":null}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RefreshedAt == nil || rows[0].LastError == nil || rows[0].LastError.Code != "unavailable" {
		t.Fatalf("rows[0] = %+v", rows[0])
	}
	if rows[1].RefreshedAt != nil || rows[1].LastError != nil {
		t.Errorf("rows[1] = %+v, want null stamps", rows[1])
	}
	if _, err := ParseLedgerShow([]byte(`not json`)); err == nil {
		t.Error("ParseLedgerShow(garbage) = nil error")
	}
}

func TestMergeIsMonotonicAndIdempotent(t *testing.T) {
	old := Record{Backend: "b", Query: "q", LastSuccessAt: ts(-time.Minute), LastError: &LedgerError{At: t0.Add(-2 * time.Minute), Code: "x"}}
	older := Record{Backend: "b", Query: "q", LastSuccessAt: ts(-time.Hour), LastError: &LedgerError{At: t0.Add(-3 * time.Minute), Code: "y"}}
	got := Merge(old, older)
	if !got.LastSuccessAt.Equal(*old.LastSuccessAt) {
		t.Errorf("a success time moved backwards: %v", got.LastSuccessAt)
	}
	if got.LastError.Code != "x" {
		t.Errorf("an older error replaced a newer one: %+v", got.LastError)
	}
	newer := Record{Backend: "b", Query: "q", LastSuccessAt: ts(0)}
	got = Merge(old, newer)
	if !got.LastSuccessAt.Equal(t0) {
		t.Errorf("a newer success was not taken: %v", got.LastSuccessAt)
	}
	if got.LastError == nil || got.LastError.Code != "x" {
		t.Errorf("an absent incoming error cleared the stored one: %+v", got.LastError)
	}
	if again := Merge(got, got); !again.equal(got) {
		t.Errorf("Merge(x, x) = %+v, want %+v", again, got)
	}
	if got := Merge(Record{Backend: "b"}, Record{Backend: "b", LastSuccessAt: ts(0)}); got.LastSuccessAt == nil {
		t.Error("success from a stored record with none was dropped")
	}
}

func TestUpdateRecordsAndIsIdempotent(t *testing.T) {
	st := store.OpenForTest(t)
	rows := []LedgerRow{
		ledgerRow("pg-connector-pr-github", "mine", ts(-time.Minute)),
		ledgerRow("pg-connector-pr-github", "team", ts(-2*time.Minute)),
	}
	mustUpdate(t, st, rows...)
	before, _ := st.ListMetaPrefix(MetaKeyPrefix)
	if len(before) != 2 {
		t.Fatalf("recorded %d keys, want 2: %v", len(before), before)
	}
	if _, ok := before["source_fetch.pg-connector-pr-github.mine"]; !ok {
		t.Errorf("key shape source_fetch.<backend>.<query> missing: %v", before)
	}
	mustUpdate(t, st, rows...)
	after, _ := st.ListMetaPrefix(MetaKeyPrefix)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed on an idempotent re-run: %q -> %q", k, v, after[k])
		}
	}
}

func TestUpdateNeverMovesASuccessBackwards(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(t, st, ledgerRow("b", "q", ts(-time.Minute)))
	// A reordered or stale read, and a ledger that was cleared (no success).
	mustUpdate(t, st, ledgerRow("b", "q", ts(-time.Hour)))
	mustUpdate(t, st, ledgerRow("b", "q", nil))
	rows := mustSources(t, st, nil, t0)
	if len(rows) != 1 || rows[0].AgeSeconds == nil || *rows[0].AgeSeconds != 60 {
		t.Fatalf("rows = %+v, want age 60s kept", rows)
	}
}

func TestUpdateRecordsASourceWithNoSuccessAsUnknown(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(t, st, ledgerRow("pg-connector-pr-github", "mine", nil))
	rows := mustSources(t, st, nil, t0)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the one never-succeeded source", rows)
	}
	r := rows[0]
	if r.LastSuccessAt != nil || r.AgeSeconds != nil || !r.Stale {
		t.Errorf("INV-FRESH-4: row = %+v, want null last_success_at and age_seconds with stale true", r)
	}
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if v, ok := m["age_seconds"]; !ok || v != nil {
		t.Errorf("age_seconds must serialize as null, got %v (present=%v)", v, ok)
	}
}

func TestUpdateSkipsRowsWithoutBackendOrQuery(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(t, st, LedgerRow{Type: "pr", Query: "q"}, LedgerRow{Type: "pr", Backend: "b"})
	if kv, _ := st.ListMetaPrefix(MetaKeyPrefix); len(kv) != 0 {
		t.Errorf("recorded %v for rows with no backend or query", kv)
	}
}

func TestUpdateFoldsDuplicateKeysConservatively(t *testing.T) {
	st := store.OpenForTest(t)
	// The same backend and query under two entity types.
	mustUpdate(
		t, st,
		LedgerRow{Type: "pr", Backend: "b", Query: "q", RefreshedAt: ts(-time.Minute)},
		LedgerRow{Type: "issue", Backend: "b", Query: "q", RefreshedAt: ts(-time.Hour)},
	)
	rows := mustSources(t, st, nil, t0)
	if len(rows) != 1 || *rows[0].AgeSeconds != 3600 {
		t.Fatalf("rows = %+v, want the older (3600s) of the duplicates", rows)
	}
}

func TestUpdateKeepsInstancesApart(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(
		t, st,
		LedgerRow{Type: "issue", Backend: "b", Query: "q", Instance: "one", RefreshedAt: ts(-time.Minute)},
		LedgerRow{Type: "issue", Backend: "b", Query: "q", Instance: "two", RefreshedAt: ts(-time.Hour)},
	)
	if kv, _ := st.ListMetaPrefix(MetaKeyPrefix); len(kv) != 2 {
		t.Fatalf("recorded %v, want one key per instance", kv)
	}
	rows := mustSources(t, st, nil, t0)
	if len(rows) != 1 || *rows[0].AgeSeconds != 3600 {
		t.Fatalf("rows = %+v, want the backend aged by its stalest instance", rows)
	}
}

func TestSourcesAggregatesTheOldestQuery(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(
		t, st,
		ledgerRow("pg-connector-pr-github", "mine", ts(-time.Minute)),
		ledgerRow("pg-connector-pr-github", "team", ts(-10*time.Minute)),
		ledgerRow("pg-connector-issue-jira", "mine", ts(-30*time.Second)),
	)
	rows := mustSources(t, st, nil, t0)
	if len(rows) != 2 || rows[0].Source != "pg-connector-issue-jira" || rows[1].Source != "pg-connector-pr-github" {
		t.Fatalf("rows = %+v, want two sources sorted by name", rows)
	}
	if rows[0].Label != "issue-jira" || *rows[0].AgeSeconds != 30 || rows[0].Stale {
		t.Errorf("issue-jira row = %+v", rows[0])
	}
	if rows[1].Label != "pr-github" || *rows[1].AgeSeconds != 600 || rows[1].Stale {
		t.Errorf("pr-github row = %+v, want the oldest query's age (600s), not stale at 10m", rows[1])
	}
	if want := t0.Add(-10 * time.Minute).Format(time.RFC3339); *rows[1].LastSuccessAt != want {
		t.Errorf("last_success_at = %s, want %s", *rows[1].LastSuccessAt, want)
	}
}

func TestSourcesOneUnknownQueryMakesTheBackendUnknown(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(
		t, st,
		ledgerRow("b", "mine", ts(-time.Minute)),
		ledgerRow("b", "team", nil),
	)
	rows := mustSources(t, st, nil, t0)
	if len(rows) != 1 || rows[0].AgeSeconds != nil || !rows[0].Stale {
		t.Fatalf("rows = %+v, want fail-closed unknown and stale", rows)
	}
}

func TestSourcesThresholdBoundary(t *testing.T) {
	cfg := &config.Config{}
	for name, tc := range map[string]struct {
		age   time.Duration
		stale bool
	}{
		"just inside":          {15*time.Minute - time.Second, false},
		"exactly at the bound": {15 * time.Minute, false},
		"just past":            {15*time.Minute + time.Second, true},
		"long past":            {time.Hour, true},
	} {
		t.Run(name, func(t *testing.T) {
			st := store.OpenForTest(t)
			mustUpdate(t, st, ledgerRow("b", "q", ts(-tc.age)))
			rows := mustSources(t, st, cfg, t0)
			if rows[0].Stale != tc.stale {
				t.Errorf("age %v: stale = %v, want %v", tc.age, rows[0].Stale, tc.stale)
			}
		})
	}
}

func TestSourcesHonorsConfiguredThresholdsAndLabels(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(
		t, st,
		ledgerRow("pg-connector-pr-github", "mine", ts(-20*time.Minute)),
		ledgerRow("pg-connector-issue-jira", "mine", ts(-20*time.Minute)),
		ledgerRow("pg-connector-thread-slack", "mine", ts(-20*time.Minute)),
	)
	cfg := &config.Config{Freshness: config.FreshnessConfig{
		SourceStaleAfter: "10m",
		Sources: map[string]config.FreshnessSourceConfig{
			"pr-github":                 {Label: "Pull requests", StaleAfter: "1h"}, // short name
			"pg-connector-issue-jira":   {Label: "Jira"},                            // full name, label only
			"pg-connector-thread-slack": {StaleAfter: "bogus"},                      // unusable override ignored
		},
	}}
	got := map[string]Row{}
	for _, r := range mustSources(t, st, cfg, t0) {
		got[r.Source] = r
	}
	if r := got["pg-connector-pr-github"]; r.Label != "Pull requests" || r.Stale {
		t.Errorf("pr-github = %+v, want the label override and the 1h per-source threshold", r)
	}
	if r := got["pg-connector-issue-jira"]; r.Label != "Jira" || !r.Stale {
		t.Errorf("issue-jira = %+v, want label Jira and the global 10m threshold", r)
	}
	if r := got["pg-connector-thread-slack"]; r.Label != "thread-slack" || !r.Stale {
		t.Errorf("thread-slack = %+v, want defaults", r)
	}
}

func TestSourcesClockSkewAndMalformedRows(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(t, st, ledgerRow("b", "q", ts(time.Minute))) // success "in the future"
	if err := st.SetMeta(MetaKeyPrefix+"junk.q", "{not json"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta(MetaKeyPrefix+"empty.q", `{"backend":""}`); err != nil {
		t.Fatal(err)
	}
	rows := mustSources(t, st, nil, t0)
	if len(rows) != 1 || *rows[0].AgeSeconds != 0 || rows[0].Stale {
		t.Fatalf("rows = %+v, want one fresh row (age clamped to 0), malformed rows skipped", rows)
	}
}

func TestSourcesEmptyIsNonNil(t *testing.T) {
	st := store.OpenForTest(t)
	rows := mustSources(t, st, nil, t0)
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %#v, want a non-nil empty slice", rows)
	}
	b, _ := json.Marshal(rows)
	if string(b) != "[]" {
		t.Errorf("serialized = %s, want []", b)
	}
}

func TestBuildReport(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(
		t, st,
		ledgerRow("pg-connector-pr-github", "mine", ts(-time.Minute)),
		ledgerRow("pg-connector-issue-jira", "mine", ts(-time.Hour)),
	)
	rep, err := Build(st, &config.Config{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SchemaVersion != 1 || rep.Now != "2026-10-06T12:00:00Z" || rep.StaleAfterSeconds != 900 || !rep.AnyStale || len(rep.Sources) != 2 {
		t.Errorf("report = %+v", rep)
	}

	fresh := store.OpenForTest(t)
	mustUpdate(t, fresh, ledgerRow("b", "q", ts(-time.Minute)))
	rep, err = Build(fresh, nil, t0)
	if err != nil || rep.AnyStale {
		t.Errorf("fresh report = %+v, %v; want any_stale false", rep, err)
	}
	empty, err := Build(store.OpenForTest(t), nil, t0)
	if err != nil || empty.AnyStale || empty.Sources == nil {
		t.Errorf("empty report = %+v, %v; want no stale source and a non-nil sources", empty, err)
	}
}
