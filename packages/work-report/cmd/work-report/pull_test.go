package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn/fake"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

const pullDoc = `{
  "sources": [
    {"source": "backend-ok", "status": "succeeded", "count": 2},
    {"source": "backend-slow", "status": "degraded", "count": 0, "reason": "upstream timed out"}
  ],
  "items": [
    {"source": "backend-ok", "item": {"id": "i1", "kind": "commit", "entity_type": "commit", "entity_id": "c1",
      "occurred_at": "2026-03-02T10:00:00Z", "summary": "one", "fields": {}, "as_of": "2026-03-02T11:00:00Z", "stale": false}},
    {"source": "backend-ok", "item": {"id": "i2", "kind": "commit", "entity_type": "commit", "entity_id": "c2",
      "occurred_at": "oops", "summary": "two", "fields": {}, "as_of": "2026-03-02T11:00:00Z", "stale": false}}
  ]
}`

func fixedNow(t *testing.T) {
	t.Helper()
	old := pullNow
	pullNow = func() time.Time { return time.Date(2026, 3, 2, 15, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { pullNow = old })
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("error %v is not an exit-status error", err)
	}
	return ee.code
}

func storeEntries(t *testing.T, path string) (entries []store.Entry, sources int) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	es, err := st.Query(t.Context(), store.Filter{OpenStart: true})
	if err != nil {
		t.Fatal(err)
	}
	data, err := st.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return es, len(data.Sources)
}

func TestPullJSONOutputAndExitCode(t *testing.T) {
	isolate(t)
	fixedNow(t)
	fake.Install(t, fake.Route{Match: []string{"activity", "list"}, Stdout: pullDoc, Exit: 0})
	db := filepath.Join(t.TempDir(), "s.db")

	out, err := runCLI(t, "--store", db, "--output", "json", "pull", "--range", "yesterday")
	if code := exitCode(t, err); code != 2 {
		t.Errorf("exit code = %d; want 2 (one degraded source), computed from our rows", code)
	}
	var got struct {
		Sources []struct {
			Source    string `json:"source"`
			Status    string `json:"status"`
			Count     int    `json:"count"`
			Unchanged int    `json:"unchanged"`
			Rejected  int    `json:"rejected"`
			Truncated bool   `json:"truncated"`
			Reason    string `json:"reason"`
		} `json:"sources"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("shape: %v\n%s", err, out)
	}
	if len(got.Sources) != 2 {
		t.Fatalf("sources = %+v", got.Sources)
	}
	if s := got.Sources[0]; s.Source != "backend-ok" || s.Status != "succeeded" || s.Count != 1 || s.Rejected != 1 {
		t.Errorf("backend-ok = %+v", s)
	}
	if s := got.Sources[1]; s.Status != "degraded" || s.Reason != "upstream timed out" {
		t.Errorf("backend-slow = %+v", s)
	}
	es, sources := storeEntries(t, db)
	if len(es) != 1 || sources != 2 {
		t.Errorf("stored %d entries, %d sources; want 1 entry and a pull row per source", len(es), sources)
	}
}

func TestPullHumanOutputIsDefault(t *testing.T) {
	isolate(t)
	fixedNow(t)
	fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: pullDoc})
	out, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "pull")
	if exitCode(t, err) != 2 {
		t.Errorf("err = %v", err)
	}
	for _, want := range []string{"backend-ok: succeeded (1 stored, 0 unchanged, 1 rejected)", "backend-slow: degraded", "upstream timed out"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output lacks %q:\n%s", want, out)
		}
	}
}

func TestPullExitZeroWhenAllSucceed(t *testing.T) {
	isolate(t)
	fixedNow(t)
	fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: `{"sources":[{"source":"b","status":"succeeded"}],"items":[]}`, Exit: 3})
	_, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "json", "pull")
	if err != nil {
		t.Errorf("exit must not follow pg-connector's code: %v", err)
	}
}

func TestPullDefaultRangeIsTodayInConfiguredZone(t *testing.T) {
	isolate(t)
	fixedNow(t)
	rec := fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: `{"sources":[],"items":[]}`})
	cfg := writeCfg(t, "timezone: America/Chicago\n")
	if _, err := runCLI(t, "--config", cfg, "--store", filepath.Join(t.TempDir(), "s.db"), "pull"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"activity", "list", "--since", "2026-03-02T00:00:00-06:00", "--before", "2026-03-03T00:00:00-06:00", "--output", "json"}}
	if got := rec.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q; want %q", got, want)
	}
}

func TestPullSourceFlagRepeatsAndAllOmitsSince(t *testing.T) {
	isolate(t)
	fixedNow(t)
	rec := fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: `{"sources":[],"items":[]}`})
	_, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "json",
		"pull", "--range", "all", "--source", "backend-a", "--source", "backend-b")
	if exitCode(t, err) != 3 { // neither pinned source got a row: both degraded
		t.Errorf("err = %v", err)
	}
	var backends []string
	for _, c := range rec.Calls() {
		for _, a := range c {
			if a == "--since" {
				t.Errorf("--range all must omit --since: %q", c)
			}
		}
		backends = append(backends, c[len(c)-3])
	}
	if !reflect.DeepEqual(backends, []string{"backend-a", "backend-b"}) {
		t.Errorf("backends = %v; want one --backend call per --source", backends)
	}
}

func TestPullDisabledSourceViaConfig(t *testing.T) {
	isolate(t)
	fixedNow(t)
	rec := fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: pullDoc})
	cfg := writeCfg(t, "sources:\n  backend-ok:\n    enable: false\n  backend-slow:\n    enable: false\n")
	db := filepath.Join(t.TempDir(), "s.db")

	out, err := runCLI(t, "--config", cfg, "--store", db, "--output", "json", "pull", "--source", "backend-ok")
	if err != nil {
		t.Errorf("only disabled sources requested, nothing attempted: %v", err)
	}
	if !strings.Contains(out, `"status": "disabled"`) || !strings.Contains(out, "disabled by configuration") {
		t.Errorf("out = %s", out)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("pg-connector invoked: %v", rec.Calls())
	}

	if _, err := runCLI(t, "--config", cfg, "--store", db, "--output", "json", "pull"); err != nil {
		t.Errorf("fan-out with every returned source disabled: %v", err)
	}
	if es, _ := storeEntries(t, db); len(es) != 0 {
		t.Errorf("stored %d entries of disabled sources", len(es))
	}
}

func TestPullAbsentConnector(t *testing.T) {
	isolate(t)
	fixedNow(t)
	t.Setenv("PATH", t.TempDir())
	db := filepath.Join(t.TempDir(), "s.db")
	out, err := runCLI(t, "--store", db, "--output", "json", "pull", "--range", "yesterday")
	if exitCode(t, err) != 3 {
		t.Errorf("err = %v; want exit 3", err)
	}
	if !strings.Contains(out, "could not be run") || !strings.Contains(out, `"source": "pg-connector"`) {
		t.Errorf("out = %s", out)
	}
	if es, sources := storeEntries(t, db); len(es) != 0 || sources != 1 {
		t.Errorf("entries=%d sources=%d; want only the pull row", len(es), sources)
	}
}

func TestPullCrashedConnector(t *testing.T) {
	isolate(t)
	fixedNow(t)
	fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: "", Exit: 1})
	out, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "json", "pull")
	if exitCode(t, err) != 3 || !strings.Contains(out, "exited 1") {
		t.Errorf("err=%v out=%s", err, out)
	}
}

func TestPullStoreLockedAtOpen(t *testing.T) {
	isolate(t)
	fixedNow(t)
	rec := fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: pullDoc})
	db := filepath.Join(t.TempDir(), "s.db")
	lock, err := sql.Open("sqlite", "file:"+db+"?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	lock.SetMaxOpenConns(1)
	tx, err := lock.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`CREATE TABLE lock_holder (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(); _ = lock.Close() })

	out, err := runCLI(t, "--store", db, "--output", "json", "pull") // waits out the busy timeout
	if exitCode(t, err) != 3 || !strings.Contains(out, "store locked") {
		t.Errorf("err=%v out=%s", err, out)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("pg-connector must not run when the store is locked: %v", rec.Calls())
	}
}

func TestPullRejectsBadFlags(t *testing.T) {
	isolate(t)
	fixedNow(t)
	db := filepath.Join(t.TempDir(), "s.db")
	if _, err := runCLI(t, "--store", db, "--output", "yaml", "pull"); err == nil {
		t.Error("an unsupported --output must fail")
	}
	if _, err := runCLI(t, "--store", db, "pull", "--range", "next-fortnight"); err == nil {
		t.Error("an unrecognized --range must fail")
	}
	if _, err := os.Stat(db); err == nil {
		t.Error("a rejected invocation must not create the store")
	}
}
