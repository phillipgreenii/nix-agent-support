package pull_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn/fake"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pull"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/telemetry"

	_ "modernc.org/sqlite"
)

var (
	t0  = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	t1  = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	now = time.Date(2026, 3, 3, 8, 0, 0, 0, time.UTC)
	rng = rangespec.Range{Since: t0, Before: t1}
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type env struct {
	deps     pull.Deps
	stateDir string
	st       *store.Store
}

func newEnv(t *testing.T, cfg config.Config) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	state := filepath.Join(dir, "state")
	return &env{
		deps:     pull.Deps{Cfg: cfg, Store: st, Conn: pgconn.NewExec(), Log: telemetry.New(state)},
		stateDir: state,
		st:       st,
	}
}

func listRoute(stdout string, exit int) fake.Route {
	return fake.Route{Match: []string{"activity", "list"}, Stdout: stdout, Exit: exit}
}

func run(t *testing.T, e *env, o pull.Opts) pull.Result {
	t.Helper()
	if o.Now.IsZero() {
		o.Now = now
	}
	if o.Range == (rangespec.Range{}) {
		o.Range = rng
	}
	res, err := pull.Run(context.Background(), e.deps, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func rowsBySource(res pull.Result) map[string]pull.OutcomeRow {
	m := map[string]pull.OutcomeRow{}
	for _, r := range res.Rows {
		m[r.Source] = r
	}
	return m
}

func queryAll(t *testing.T, st *store.Store) []store.Entry {
	t.Helper()
	es, err := st.Query(context.Background(), store.Filter{OpenStart: true})
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestMixedOutcomes(t *testing.T) {
	cfg := config.Config{Sources: map[string]config.SourceCfg{
		"backend-ok": {Enable: true, Labels: []string{"team:example"}},
	}}
	e := newEnv(t, cfg)
	fake.Install(t, listRoute(fixture(t, "mixed.json"), 2))

	res := run(t, e, pull.Opts{})
	rows := rowsBySource(res)

	ok := rows["backend-ok"]
	if ok.Status != pull.StatusSucceeded || ok.Count != 2 || ok.Rejected != 1 || ok.Unchanged != 0 {
		t.Errorf("backend-ok = %+v; want succeeded, 2 stored, 1 rejected", ok)
	}
	deg := rows["backend-degraded"]
	if deg.Status != pull.StatusDegraded || deg.Reason != "upstream timed out" || deg.Count != 1 {
		t.Errorf("backend-degraded = %+v; want degraded with the source's reason and its item stored", deg)
	}
	tr := rows["backend-trunc"]
	if tr.Status != pull.StatusDegraded || tr.Reason != "truncated: re-pull a narrower range" || !tr.Truncated || tr.Count != 1 {
		t.Errorf("backend-trunc = %+v; want degraded/truncated with its item stored", tr)
	}
	na := rows["backend-na"]
	if na.Status != pull.StatusDisabled || na.Reason != "not applicable: no credential" || na.Count != 0 {
		t.Errorf("backend-na = %+v; want disabled, not applicable", na)
	}
	if res.CouldNotRun {
		t.Error("CouldNotRun must be false when pg-connector answered")
	}
	// the fake exits 2; the code comes from the rows (2 degraded of 3 attempted).
	if got := res.ExitCode(); got != 2 {
		t.Errorf("ExitCode = %d; want 2", got)
	}

	entries := queryAll(t, e.st)
	if len(entries) != 4 {
		t.Fatalf("stored %d entries; want 4 (the invalid item is rejected, the rest stored)", len(entries))
	}
	byID := map[string]store.Entry{}
	for _, en := range entries {
		byID[en.ID] = en
	}
	one, found := byID["backend-ok:item-1"]
	if !found {
		t.Fatalf("missing backend-ok:item-1 in %v", entries)
	}
	if one.ExternalID != "example/repo#1" || one.SourceID != "backend-ok" || one.Type != "pr.merged" ||
		one.Summary != "Merged example change one" || one.URL != "https://example.test/pr/1" {
		t.Errorf("mapped entry = %+v", one)
	}
	if want := time.Date(2026, 3, 2, 15, 30, 0, 0, time.UTC); !one.OccurredAt.Equal(want) || one.OccurredAt.Location() != time.UTC {
		t.Errorf("occurred_at = %v; want %v in UTC", one.OccurredAt, want)
	}
	wantLabels := []string{"branch:main", "kind:pr.merged", "repo:example/repo", "team:example"}
	if !reflect.DeepEqual(one.Labels, wantLabels) {
		t.Errorf("labels = %v; want %v", one.Labels, wantLabels)
	}
	var fields map[string]any
	if err := json.Unmarshal(one.Fields, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["title"] != "one" || fields["entity_type"] != "pr" || fields["approximate"] != false ||
		fields["as_of"] != "2026-03-02T20:00:00Z" || fields["stale"] != false {
		t.Errorf("fields = %v", fields)
	}
	two := byID["backend-ok:item-2"]
	var f2 map[string]any
	_ = json.Unmarshal(two.Fields, &f2)
	if f2["approximate"] != true {
		t.Errorf("approximate must be carried into fields: %v", f2)
	}
	if _, bad := byID["backend-ok:item-bad"]; bad {
		t.Error("the schema-invalid item must not be stored")
	}

	// one pull row per source persisted, one log line per source written.
	st, err := e.st.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Sources) != 4 {
		t.Errorf("status lists %d sources; want 4 (one pull row each)", len(st.Sources))
	}
	logBytes, err := os.ReadFile(filepath.Join(e.stateDir, "log", "pull.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(strings.TrimSpace(string(logBytes)), "\n")); n != 4 {
		t.Errorf("pull.jsonl has %d lines; want 4", n)
	}
}

func TestSecondPullAppendsNothingEvenWithNewerAsOfAndStale(t *testing.T) {
	e := newEnv(t, config.Config{})
	rec := fake.Install(t, listRoute(fixture(t, "mixed.json"), 0))
	first := run(t, e, pull.Opts{})
	before := len(queryAll(t, e.st))

	// the same items re-observed with a later as_of, flipped stale, a different
	// offset spelling of the same instant and reordered labels.
	fake.Install(t, listRoute(fixture(t, "mixed_reobserved.json"), 0))
	second := run(t, e, pull.Opts{Now: now.Add(time.Hour)})

	if after := len(queryAll(t, e.st)); after != before {
		t.Errorf("store grew from %d to %d on a re-observation", before, after)
	}
	for _, name := range []string{"backend-ok", "backend-degraded", "backend-trunc"} {
		r := rowsBySource(second)[name]
		f := rowsBySource(first)[name]
		if r.Count != 0 || r.Unchanged != f.Count {
			t.Errorf("%s second pull = %+v; want count 0 and %d unchanged", name, r, f.Count)
		}
	}
	if r := rowsBySource(second)["backend-ok"]; r.Rejected != 0 {
		t.Errorf("the second fixture has no invalid item, rejected = %d", r.Rejected)
	}
	_ = rec
}

func TestChangedContentIsAppendedAsNewVersion(t *testing.T) {
	e := newEnv(t, config.Config{})
	fake.Install(t, listRoute(fixture(t, "mixed.json"), 0))
	run(t, e, pull.Opts{})
	before := len(queryAll(t, e.st))

	changed := strings.Replace(fixture(t, "mixed.json"), "Merged example change one", "Merged example change ONE", 1)
	fake.Install(t, listRoute(changed, 0))
	res := run(t, e, pull.Opts{})
	if r := rowsBySource(res)["backend-ok"]; r.Count != 1 || r.Unchanged != 1 {
		t.Errorf("backend-ok = %+v; want 1 superseding entry and 1 unchanged", r)
	}
	if after := len(queryAll(t, e.st)); after != before {
		t.Errorf("latest-wins reads must still show one row per id: %d -> %d", before, after)
	}
}

func TestExitCodesComeFromOwnRowsNotConnectorExit(t *testing.T) {
	doc := func(statuses ...string) string {
		var rows []string
		for i, s := range statuses {
			rows = append(rows, `{"source":"b`+string(rune('0'+i))+`","status":"`+s+`","count":0,"reason":"r"}`)
		}
		return `{"sources":[` + strings.Join(rows, ",") + `],"items":[]}`
	}
	cases := []struct {
		name     string
		stdout   string
		fakeExit int
		want     int
	}{
		{"all succeeded, fake says 3", doc("succeeded", "succeeded"), 3, 0},
		{"one degraded, fake says 0", doc("succeeded", "degraded"), 0, 2},
		{"all degraded, fake says 0", doc("degraded", "degraded"), 0, 3},
		{"disabled is not attempted", doc("succeeded", "disabled"), 3, 0},
		{"only disabled", doc("disabled"), 3, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, config.Config{})
			fake.Install(t, listRoute(c.stdout, c.fakeExit))
			if got := run(t, e, pull.Opts{}).ExitCode(); got != c.want {
				t.Errorf("ExitCode = %d; want %d", got, c.want)
			}
		})
	}
}

func TestAbsentConnectorDegradesEverySourceAndWritesOnlyPullRows(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cfg := config.Config{Sources: map[string]config.SourceCfg{
		"backend-a": {Enable: true},
		"backend-b": {Enable: true},
		"backend-c": {Enable: false},
	}}
	e := newEnv(t, cfg)
	res := run(t, e, pull.Opts{})

	if !res.CouldNotRun {
		t.Error("CouldNotRun must be true")
	}
	rows := rowsBySource(res)
	for _, n := range []string{"backend-a", "backend-b"} {
		if r := rows[n]; r.Status != pull.StatusDegraded || !strings.Contains(r.Reason, "pg-connector could not be run") {
			t.Errorf("%s = %+v; want degraded with the exec reason", n, r)
		}
	}
	if rows["backend-c"].Status != pull.StatusDisabled {
		t.Errorf("a config-disabled source stays disabled: %+v", rows["backend-c"])
	}
	if got := res.ExitCode(); got != 3 {
		t.Errorf("ExitCode = %d; want 3", got)
	}
	if n := len(queryAll(t, e.st)); n != 0 {
		t.Errorf("%d entries written; want only pull rows", n)
	}
	st, _ := e.st.Status(context.Background())
	if len(st.Sources) != 3 {
		t.Errorf("want one pull row per source, status lists %d", len(st.Sources))
	}
}

func TestAbsentConnectorWithNoKnownSourceNamesPGConnector(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	e := newEnv(t, config.Config{})
	res := run(t, e, pull.Opts{})
	if len(res.Rows) != 1 || res.Rows[0].Source != "pg-connector" || res.Rows[0].Status != pull.StatusDegraded || !res.CouldNotRun {
		t.Errorf("res = %+v; want one degraded pg-connector row", res)
	}
	if res.ExitCode() != 3 {
		t.Errorf("ExitCode = %d; want 3", res.ExitCode())
	}
}

func TestAbsentConnectorWithPinnedSources(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	e := newEnv(t, config.Config{})
	res := run(t, e, pull.Opts{Sources: []string{"backend-a", "backend-b"}})
	if len(res.Rows) != 2 || !res.CouldNotRun || res.ExitCode() != 3 {
		t.Errorf("res = %+v exit=%d", res, res.ExitCode())
	}
}

func TestCrashWithNoJSONDegradesEverySource(t *testing.T) {
	for _, stdout := range []string{"", "panic: something went wrong\n", "{not json"} {
		cfg := config.Config{Sources: map[string]config.SourceCfg{"backend-a": {Enable: true}}}
		e := newEnv(t, cfg)
		fake.Install(t, listRoute(stdout, 1))
		res := run(t, e, pull.Opts{})
		if !res.CouldNotRun || res.ExitCode() != 3 {
			t.Errorf("stdout %q: res = %+v", stdout, res)
			continue
		}
		r := res.Rows[0]
		if r.Status != pull.StatusDegraded || !strings.Contains(r.Reason, "exited 1") || !strings.Contains(r.Reason, "simulated failure") {
			t.Errorf("stdout %q: reason %q lacks the exit code and stderr excerpt", stdout, r.Reason)
		}
		if n := len(queryAll(t, e.st)); n != 0 {
			t.Errorf("stdout %q: %d entries written", stdout, n)
		}
	}
}

func TestDisabledSourceStoresNothing(t *testing.T) {
	cfg := config.Config{Sources: map[string]config.SourceCfg{
		"backend-ok": {Enable: false},
	}}
	t.Run("fan-out discards its items", func(t *testing.T) {
		e := newEnv(t, cfg)
		fake.Install(t, listRoute(fixture(t, "mixed.json"), 0))
		res := run(t, e, pull.Opts{})
		r := rowsBySource(res)["backend-ok"]
		if r.Status != pull.StatusDisabled || r.Reason != "disabled by configuration" || r.Count != 0 {
			t.Errorf("backend-ok = %+v", r)
		}
		for _, en := range queryAll(t, e.st) {
			if en.SourceID == "backend-ok" {
				t.Errorf("stored an entry of a disabled source: %+v", en)
			}
		}
		st, _ := e.st.Status(context.Background())
		found := false
		for _, s := range st.Sources {
			if s.Source == "backend-ok" && s.LastStatus == pull.StatusDisabled {
				found = true
			}
		}
		if !found {
			t.Error("a pull row must be persisted for the disabled source")
		}
	})
	t.Run("pinned is never invoked", func(t *testing.T) {
		e := newEnv(t, cfg)
		rec := fake.Install(t, listRoute(fixture(t, "mixed.json"), 0))
		res := run(t, e, pull.Opts{Sources: []string{"backend-ok"}})
		if len(rec.Calls()) != 0 {
			t.Errorf("pg-connector was invoked for a disabled pinned source: %v", rec.Calls())
		}
		if len(res.Rows) != 1 || res.Rows[0].Status != pull.StatusDisabled || res.ExitCode() != 0 {
			t.Errorf("res = %+v", res)
		}
		if n := len(queryAll(t, e.st)); n != 0 {
			t.Errorf("%d entries stored", n)
		}
	})
}

func TestArgvShapes(t *testing.T) {
	since, before := "2026-03-02T00:00:00Z", "2026-03-03T00:00:00Z"
	empty := `{"sources":[],"items":[]}`

	t.Run("fan-out is one call", func(t *testing.T) {
		e := newEnv(t, config.Config{})
		rec := fake.Install(t, listRoute(empty, 0))
		run(t, e, pull.Opts{})
		want := [][]string{{"activity", "list", "--since", since, "--before", before, "--output", "json"}}
		if got := rec.Calls(); !reflect.DeepEqual(got, want) {
			t.Errorf("calls = %q; want %q", got, want)
		}
	})
	t.Run("one --backend call per pinned source", func(t *testing.T) {
		e := newEnv(t, config.Config{})
		rec := fake.Install(t, listRoute(`{"sources":[{"source":"backend-a","status":"succeeded"}],"items":[]}`, 0))
		res := run(t, e, pull.Opts{Sources: []string{"backend-a", "backend-b", "backend-a"}})
		want := [][]string{
			{"activity", "list", "--since", since, "--before", before, "--backend", "backend-a", "--output", "json"},
			{"activity", "list", "--since", since, "--before", before, "--backend", "backend-b", "--output", "json"},
		}
		if got := rec.Calls(); !reflect.DeepEqual(got, want) {
			t.Errorf("calls = %q; want %q", got, want)
		}
		// the fake answers backend-b with no row for it: that is degraded, not silent.
		if r := rowsBySource(res)["backend-b"]; r.Status != pull.StatusDegraded {
			t.Errorf("backend-b = %+v; want degraded", r)
		}
	})
	t.Run("open-ended range omits --since", func(t *testing.T) {
		e := newEnv(t, config.Config{})
		rec := fake.Install(t, listRoute(empty, 0))
		run(t, e, pull.Opts{Range: rangespec.Range{Before: t1, OpenStart: true}})
		want := [][]string{{"activity", "list", "--before", before, "--output", "json"}}
		if got := rec.Calls(); !reflect.DeepEqual(got, want) {
			t.Errorf("calls = %q; want %q", got, want)
		}
	})
	t.Run("--before is an instant with offset", func(t *testing.T) {
		e := newEnv(t, config.Config{})
		rec := fake.Install(t, listRoute(empty, 0))
		cst := time.FixedZone("CST", -6*3600)
		run(t, e, pull.Opts{Range: rangespec.Range{Since: t0.In(cst), Before: t1.In(cst)}})
		c := rec.Calls()[0]
		if c[3] != "2026-03-01T18:00:00-06:00" || c[5] != "2026-03-02T18:00:00-06:00" {
			t.Errorf("argv = %q; want RFC3339 instants with offset", c)
		}
	})
}

func TestBackfillTwiceAppendsNothingSecondTime(t *testing.T) {
	e := newEnv(t, config.Config{})
	fake.Install(t, listRoute(fixture(t, "wide.json"), 0))
	wide := rangespec.Range{Since: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Before: t1}

	first := run(t, e, pull.Opts{Range: wide})
	if r := first.Rows[0]; r.Status != pull.StatusSucceeded || r.Count != 84 || r.Unchanged != 0 {
		t.Errorf("first = %+v; want 84 stored", r)
	}
	second := run(t, e, pull.Opts{Range: wide})
	if r := second.Rows[0]; r.Count != 0 || r.Unchanged != 84 {
		t.Errorf("second = %+v; want nothing appended, 84 unchanged", r)
	}
	if n := len(queryAll(t, e.st)); n != 84 {
		t.Errorf("store holds %d entries; want 84 with no duplicates", n)
	}
}

func TestSchemaInvalidItems(t *testing.T) {
	item := func(mut string) string {
		base := map[string]any{
			"id": "x", "kind": "commit", "entity_type": "commit", "entity_id": "e",
			"occurred_at": "2026-03-02T10:00:00Z", "summary": "s", "fields": map[string]any{},
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(mut), &m)
		for k, v := range m {
			if v == nil {
				delete(base, k)
			} else {
				base[k] = v
			}
		}
		b, _ := json.Marshal(base)
		return string(b)
	}
	cases := map[string]string{
		"missing id":          `{"id":null}`,
		"empty kind":          `{"kind":""}`,
		"missing entity_id":   `{"entity_id":null}`,
		"missing occurred_at": `{"occurred_at":null}`,
		"unparseable time":    `{"occurred_at":"yesterday"}`,
		"missing summary":     `{"summary":null}`,
		"fields missing":      `{"fields":null}`,
		"fields is an array":  `{"fields":[1]}`,
		"fields is a string":  `{"fields":"x"}`,
		"wrong type":          `{"id":7}`,
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, config.Config{})
			doc := `{"sources":[{"source":"b","status":"succeeded","count":2}],"items":[` +
				`{"source":"b","item":` + item(mut) + `},` +
				`{"source":"b","item":` + item(`{"id":"good"}`) + `}]}`
			fake.Install(t, listRoute(doc, 0))
			res := run(t, e, pull.Opts{})
			r := res.Rows[0]
			if r.Status != pull.StatusSucceeded || r.Rejected != 1 || r.Count != 1 {
				t.Errorf("row = %+v; want succeeded, 1 rejected, 1 stored", r)
			}
		})
	}
	t.Run("an item that is not an object", func(t *testing.T) {
		e := newEnv(t, config.Config{})
		doc := `{"sources":[{"source":"b","status":"succeeded"}],"items":[{"source":"b","item":"nope"},{"source":"b","item":null}]}`
		fake.Install(t, listRoute(doc, 0))
		if r := run(t, e, pull.Opts{}).Rows[0]; r.Rejected != 2 || r.Status != pull.StatusSucceeded {
			t.Errorf("row = %+v", r)
		}
	})
}

// holdWriteLock keeps a write transaction open on path until the test ends.
func holdWriteLock(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("take write lock: %v", err)
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS lock_holder (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(); _ = db.Close() })
}

func TestLockedStoreDegradesTheWholePull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	holdWriteLock(t, path)

	deps := pull.Deps{Cfg: config.Config{}, Store: st, Conn: pgconn.NewExec(), Log: telemetry.New(filepath.Join(dir, "state"))}
	fake.Install(t, listRoute(fixture(t, "mixed.json"), 0))
	res, err := pull.Run(context.Background(), deps, pull.Opts{Range: rng, Now: now}) // waits out the store's busy timeout
	if err != nil {
		t.Fatal(err)
	}
	if !res.CouldNotRun {
		t.Error("CouldNotRun must be true on a store lock")
	}
	for _, r := range res.Rows {
		switch r.Status {
		case pull.StatusDisabled:
		case pull.StatusDegraded:
			if !strings.Contains(r.Reason, "store locked") {
				t.Errorf("row %+v: reason must name the lock", r)
			}
		default:
			t.Errorf("row %+v must be degraded by the lock", r)
		}
	}
	if got := res.ExitCode(); got != 3 {
		t.Errorf("ExitCode = %d; want 3", got)
	}
}

func TestUnavailableMirrorsAFailedRun(t *testing.T) {
	cfg := config.Config{Sources: map[string]config.SourceCfg{
		"backend-a": {Enable: true}, "backend-b": {Enable: false},
	}}
	state := t.TempDir()
	d := pull.Deps{Cfg: cfg, Log: telemetry.New(state)}
	res := pull.Unavailable(d, pull.Opts{Range: rng, Now: now}, "store locked: test")
	if !res.CouldNotRun || res.ExitCode() != 3 {
		t.Fatalf("res = %+v", res)
	}
	rows := rowsBySource(res)
	if rows["backend-a"].Status != pull.StatusDegraded || rows["backend-a"].Reason != "store locked: test" ||
		rows["backend-b"].Status != pull.StatusDisabled {
		t.Errorf("rows = %+v", rows)
	}
	if _, err := os.Stat(filepath.Join(state, "log", "pull.jsonl")); err != nil {
		t.Errorf("a log line per source is still written: %v", err)
	}

	res = pull.Unavailable(d, pull.Opts{Range: rng, Now: now, Sources: []string{"backend-a", "backend-b"}}, "r")
	if len(res.Rows) != 2 || res.Rows[1].Status != pull.StatusDisabled {
		t.Errorf("pinned = %+v", res)
	}
}

func TestTelemetryFailureDoesNotFailThePull(t *testing.T) {
	e := newEnv(t, config.Config{})
	// a regular file where the state dir should be makes every log write fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.deps.Log = telemetry.New(blocker)
	fake.Install(t, listRoute(fixture(t, "mixed.json"), 0))
	if res := run(t, e, pull.Opts{}); res.ExitCode() != 2 || len(queryAll(t, e.st)) != 4 {
		t.Errorf("pull must proceed despite log failures: %+v", res)
	}
}
