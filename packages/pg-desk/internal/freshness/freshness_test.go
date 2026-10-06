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

// seen is a ledger row whose pg-router consumer last polled it at t0+d.
func seen(row LedgerRow, d time.Duration) LedgerRow {
	row.Consumers = map[string]LedgerConsumer{"pg-router": {LastSeen: ts(d)}}
	return row
}

func inst(row LedgerRow, instance string) LedgerRow {
	row.Instance = instance
	return row
}

func onlySource(t *testing.T, rows []Row, source string) Row {
	t.Helper()
	for _, r := range rows {
		if r.Source == source {
			return r
		}
	}
	t.Fatalf("no %s row in %+v", source, rows)
	return Row{}
}

func TestLedgerRowLastSeenIsTheNewestConsumer(t *testing.T) {
	row := LedgerRow{Consumers: map[string]LedgerConsumer{
		"a": {LastSeen: ts(-time.Hour)}, "b": {LastSeen: ts(-time.Minute)}, "c": {},
	}}
	if got := row.lastSeen(); got == nil || !got.Equal(t0.Add(-time.Minute)) {
		t.Errorf("lastSeen = %v, want the newest consumer's", got)
	}
	if (LedgerRow{}).lastSeen() != nil {
		t.Error("lastSeen of a row with no consumers must be nil")
	}
}

func TestUpdateRecordsLastSeenMonotonically(t *testing.T) {
	st := store.OpenForTest(t)
	row := ledgerRow("b", "q", ts(-time.Minute))
	mustUpdate(t, st, seen(row, -time.Minute))
	mustUpdate(t, st, seen(row, -time.Hour)) // stale read
	mustUpdate(t, st, row)                   // consumer info absent
	kv, _ := st.ListMetaPrefix(MetaKeyPrefix)
	var rec Record
	if err := json.Unmarshal([]byte(kv["source_fetch.b.q"]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.LastSeenAt == nil || !rec.LastSeenAt.Equal(t0.Add(-time.Minute)) {
		t.Errorf("last_seen_at = %v, want the newest seen (-1m) kept", rec.LastSeenAt)
	}
}

func TestSourcesGhostRowDoesNotKeepABackendStale(t *testing.T) {
	st := store.OpenForTest(t)
	b := "pg-connector-issue-beads"
	mustUpdate(
		t, st,
		// live instance, fresh
		seen(inst(ledgerRow(b, "work-beads", ts(-time.Minute)), "/live"), -time.Minute),
		// ghosts: never succeeded, last polled well past AbandonedAfter ago
		seen(ledgerRow(b, "work-beads", nil), -AbandonedAfter-time.Hour),
		seen(inst(ledgerRow(b, "work-beads", nil), "/gone"), -AbandonedAfter-48*time.Hour),
	)
	r := onlySource(t, mustSources(t, st, nil, t0), b)
	if r.Stale || r.AgeSeconds == nil || *r.AgeSeconds != 60 || r.LastSuccessAt == nil {
		t.Errorf("row = %+v, want fresh at 60s with the ghosts ignored", r)
	}
}

func TestSourcesGhostWithAnOldSuccessIsIgnoredToo(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(
		t, st,
		seen(inst(ledgerRow("b", "q", ts(-time.Minute)), "/live"), 0),
		seen(inst(ledgerRow("b", "q", ts(-AbandonedAfter-24*time.Hour)), "/gone"), -AbandonedAfter-time.Hour),
	)
	r := onlySource(t, mustSources(t, st, nil, t0), "b")
	if r.Stale {
		t.Errorf("row = %+v, a retired instance's old success must not hold the backend stale", r)
	}
}

func TestSourcesLiveRowThatNeverSucceededStaysStale(t *testing.T) {
	st := store.OpenForTest(t)
	// thread-slack's shape: polled a minute ago, every fetch failing.
	row := seen(ledgerRow("pg-connector-thread-slack", "involving-me", nil), -time.Minute)
	row.LastError = &LedgerError{At: t0.Add(-time.Minute), Code: "truncated"}
	mustUpdate(t, st, row)
	r := onlySource(t, mustSources(t, st, nil, t0), "pg-connector-thread-slack")
	if !r.Stale || r.AgeSeconds != nil || r.LastSuccessAt != nil {
		t.Errorf("row = %+v, want unknown and stale (INV-FRESH-4)", r)
	}
}

func TestSourcesMixedBackendLiveFailureBeatsGhosts(t *testing.T) {
	st := store.OpenForTest(t)
	mustUpdate(
		t, st,
		seen(inst(ledgerRow("b", "q1", ts(-time.Minute)), "/live"), -time.Minute),
		seen(inst(ledgerRow("b", "q2", nil), "/live"), -time.Minute), // live, never succeeded
		seen(inst(ledgerRow("b", "q1", nil), "/gone"), -AbandonedAfter-time.Hour),
	)
	r := onlySource(t, mustSources(t, st, nil, t0), "b")
	if !r.Stale || r.AgeSeconds != nil {
		t.Errorf("row = %+v, want stale: the live never-succeeded row still counts", r)
	}
}

func TestSourcesAbandonedBoundary(t *testing.T) {
	for name, tc := range map[string]struct {
		lastSeen  time.Duration
		wantStale bool
	}{
		"exactly at the bound still counts": {-AbandonedAfter, true},
		"past the bound is a ghost":         {-AbandonedAfter - time.Second, false},
	} {
		t.Run(name, func(t *testing.T) {
			st := store.OpenForTest(t)
			mustUpdate(
				t, st,
				seen(inst(ledgerRow("b", "q", ts(-time.Minute)), "/live"), 0),
				seen(inst(ledgerRow("b", "q", nil), "/other"), tc.lastSeen),
			)
			if r := onlySource(t, mustSources(t, st, nil, t0), "b"); r.Stale != tc.wantStale {
				t.Errorf("stale = %v, want %v (%+v)", r.Stale, tc.wantStale, r)
			}
		})
	}
}

func TestSourcesAllAbandonedBackendStaysStale(t *testing.T) {
	// Nothing polls the backend any more: it MUST NOT vanish or read fresh.
	st := store.OpenForTest(t)
	mustUpdate(
		t, st,
		seen(ledgerRow("b", "q", ts(-AbandonedAfter-time.Hour)), -AbandonedAfter-time.Hour),
		seen(inst(ledgerRow("b", "q", nil), "/gone"), -AbandonedAfter-time.Hour),
	)
	r := onlySource(t, mustSources(t, st, nil, t0), "b")
	if !r.Stale {
		t.Errorf("row = %+v, want stale when every row is abandoned", r)
	}
}

func TestSourcesUnknownLastSeenCountsAsLive(t *testing.T) {
	// A record written before last_seen_at existed (or with no consumer) is
	// not evidence of a ghost: fail closed.
	st := store.OpenForTest(t)
	mustUpdate(
		t, st,
		ledgerRow("b", "q", nil),
		seen(inst(ledgerRow("b", "q", ts(-time.Minute)), "/live"), 0),
	)
	if r := onlySource(t, mustSources(t, st, nil, t0), "b"); !r.Stale || r.AgeSeconds != nil {
		t.Errorf("row = %+v, want stale for the row with unknown last_seen", r)
	}
}

// TestRealLedgerShapeClassifiesGhosts replays the trimmed live ledger
// observed on 2026-10-06 (bead pg2-7nuby): six ghost rows (legacy no-instance
// rows last seen 2026-09-23 and the retired /Volumes/ziprecruiter/monorepo
// instance last seen 2026-09-25) beside live rows and the genuinely failing
// thread-slack.
func TestRealLedgerShapeClassifiesGhosts(t *testing.T) {
	now := time.Date(2026, 10, 6, 22, 58, 0, 0, time.UTC)
	rows, err := ParseLedgerShow([]byte(`[
	{"type":"issue","backend":"pg-connector-issue-beads","query":"escalated-work","consumers":{"pg-router":{"cursor":12,"last_seen":"2026-09-23T15:21:30.248975-04:00"}},"refreshed_at":null,"last_error":null},
	{"type":"issue","backend":"pg-connector-issue-beads","query":"escalated-work","instance":"/Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter","consumers":{"pg-router":{"cursor":95,"last_seen":"2026-10-06T18:57:21.508035-04:00"}},"refreshed_at":"2026-10-06T18:57:21.505029-04:00","last_error":null},
	{"type":"issue","backend":"pg-connector-issue-beads","query":"escalated-work","instance":"/Volumes/ziprecruiter/monorepo","consumers":{"pg-router":{"cursor":0,"last_seen":"2026-09-25T19:49:57.172894-04:00"}},"refreshed_at":null,"last_error":null},
	{"type":"issue","backend":"pg-connector-issue-beads","query":"work-beads","consumers":{"pg-router":{"cursor":227,"last_seen":"2026-09-23T15:20:45.540411-04:00"}},"refreshed_at":null,"last_error":null},
	{"type":"issue","backend":"pg-connector-issue-beads","query":"work-beads","instance":"/Volumes/gitrepos/ziprecruiter/pristine","consumers":{"pg-router":{"cursor":1683,"last_seen":"2026-10-06T18:53:43.268215-04:00"}},"refreshed_at":"2026-10-06T18:53:43.267417-04:00","last_error":null},
	{"type":"issue","backend":"pg-connector-issue-beads","query":"work-beads","instance":"/Volumes/ziprecruiter/monorepo","consumers":{"pg-router":{"cursor":145,"last_seen":"2026-09-25T19:46:10.901807-04:00"}},"refreshed_at":null,"last_error":null},
	{"type":"issue","backend":"pg-connector-issue-jira","query":"mine","consumers":{"pg-router":{"cursor":2,"last_seen":"2026-09-23T15:16:55.178002-04:00"}},"refreshed_at":null,"last_error":null},
	{"type":"issue","backend":"pg-connector-issue-jira","query":"mine","instance":"/Volumes/gitrepos/ziprecruiter/pristine","consumers":{"pg-router":{"cursor":2,"last_seen":"2026-10-06T18:54:31.496072-04:00"}},"refreshed_at":"2026-10-06T18:54:31.479823-04:00","last_error":null},
	{"type":"issue","backend":"pg-connector-issue-jira","query":"mine","instance":"/Volumes/ziprecruiter/monorepo","consumers":{"pg-router":{"cursor":1,"last_seen":"2026-09-25T19:47:42.819754-04:00"}},"refreshed_at":null,"last_error":null},
	{"type":"pr","backend":"pg-connector-pr-github","query":"mine","consumers":{"pg-router":{"cursor":1240,"last_seen":"2026-10-06T18:56:33.936461-04:00"}},"refreshed_at":"2026-10-06T18:56:33.935982-04:00","last_error":null},
	{"type":"thread","backend":"pg-connector-thread-slack","query":"involving-me","consumers":{"pg-router":{"cursor":0,"last_seen":"2026-10-06T18:38:23.611902-04:00"}},"refreshed_at":null,"last_error":{"at":"2026-10-06T18:38:23.605758-04:00","code":"truncated"}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	ghosts := 0
	for _, r := range collapse(rows) {
		if r.abandoned(now) {
			ghosts++
		}
	}
	if ghosts != 6 {
		t.Errorf("classified %d ghost records, want the 6 known ghosts", ghosts)
	}
	st := store.OpenForTest(t)
	mustUpdate(t, st, rows...)
	got := mustSources(t, st, nil, now)
	for _, src := range []string{"pg-connector-issue-beads", "pg-connector-issue-jira"} {
		// Both heartbeat refreshed_at stamps are ~4 minutes old at "now".
		if r := onlySource(t, got, src); r.Stale || r.LastSuccessAt == nil || r.AgeSeconds == nil {
			t.Errorf("%s = %+v, want fresh with a success time", src, r)
		}
	}
	if r := onlySource(t, got, "pg-connector-thread-slack"); !r.Stale || r.AgeSeconds != nil {
		t.Errorf("thread-slack = %+v, want stale (fail closed)", r)
	}
	if r := onlySource(t, got, "pg-connector-pr-github"); r.Stale {
		t.Errorf("pr-github = %+v, want fresh", r)
	}
}
