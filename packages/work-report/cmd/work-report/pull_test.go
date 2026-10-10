package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/degraded"
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
		if c[0] != "activity" { // the degraded-source bead lookup is not under test here
			continue
		}
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

// ---- degraded-source bead and --output pg-router ---------------------------

const (
	degradedDoc = `{"sources":[{"source":"backend-slow","status":"degraded","count":0,"reason":"upstream timed out"}],"items":[]}`
	healthyDoc  = `{"sources":[{"source":"backend-ok","status":"succeeded","count":0}],"items":[]}`
)

func issueCalls(calls [][]string, verb string) [][]string {
	var out [][]string
	for _, c := range calls {
		if len(c) > 1 && c[0] == "issue" && c[1] == verb {
			out = append(out, c)
		}
	}
	return out
}

func TestPullPGRouterEmitsOneItemPerDegradedSource(t *testing.T) {
	isolate(t)
	fixedNow(t)
	rec := fake.Install(
		t,
		fake.Route{Match: []string{"activity", "list"}, Stdout: degradedDoc},
		fake.IssueListRoute(), fake.ConfigValidateRoute(fake.ConfigRow{Source: "backend-slow", Status: "degraded"}),
		fake.IssueCreateRoute("bd-7"),
	)
	cfg := writeCfg(t, "timezone: America/Chicago\n")
	out, err := runCLI(t, "--config", cfg, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "pg-router",
		"pull", "--range", "yesterday")
	if err != nil {
		t.Fatalf("per-source degradation is data, not a failure in pg-router mode: %v", err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out)
	}
	if len(items) != 1 {
		t.Fatalf("items = %s", out)
	}
	var keys []string
	for k := range items[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"expiresAt", "id", "metadata", "title", "type"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("item keys = %v; want %v", keys, want)
	}
	var id, typ, title, expires string
	for k, dst := range map[string]*string{"id": &id, "type": &typ, "title": &title, "expiresAt": &expires} {
		if err := json.Unmarshal(items[0][k], dst); err != nil {
			t.Fatal(err)
		}
	}
	if id != "bd-7" || typ != "issue" || title != "work-report: backend-slow degraded" {
		t.Errorf("item = %s", out)
	}
	// fixedNow is 2026-03-02T15:00Z = 09:00 Chicago; the local day ends 2026-03-03T00:00-06:00.
	if want := "2026-03-03T06:00:00-06:00"; expires != want {
		t.Errorf("expiresAt = %q; want %q", expires, want)
	}

	creates := issueCalls(rec.Calls(), "create")
	if len(creates) != 1 {
		t.Fatalf("create calls = %v", creates)
	}
	var body string
	for i, a := range creates[0] {
		if a == "--description" {
			body = creates[0][i+1]
		}
	}
	if !strings.Contains(body, "work-report pull --range yesterday") {
		t.Errorf("bead body lacks the exact re-pull command:\n%s", body)
	}
}

func TestPullPGRouterHealthyPullPrintsEmptyArray(t *testing.T) {
	isolate(t)
	fixedNow(t)
	rec := fake.Install(
		t,
		fake.Route{Match: []string{"activity", "list"}, Stdout: healthyDoc},
		fake.IssueListRoute(),
	)
	out, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "pg-router", "pull")
	if err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(out), &items); err != nil || items == nil || len(items) != 0 {
		t.Errorf("out = %q (err %v); want []", out, err)
	}
	if got := len(issueCalls(rec.Calls(), "create")); got != 0 {
		t.Errorf("a healthy pull created %d beads", got)
	}
}

func TestPullPGRouterSucceedingPullClosesTheBead(t *testing.T) {
	isolate(t)
	fixedNow(t)
	rec := fake.Install(
		t,
		fake.Route{Match: []string{"activity", "list"}, Stdout: `{"sources":[{"source":"backend-slow","status":"succeeded"}],"items":[]}`},
		fake.IssueListRoute(fake.IssueEntity{ID: "bd-7", Title: "work-report: backend-slow degraded"}),
		fake.IssueCloseRoute(),
	)
	out, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "pg-router", "pull")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if got := len(issueCalls(rec.Calls(), "close")); got != 1 {
		t.Errorf("close calls = %d; want 1", got)
	}
}

func TestPullPGRouterConfigLoadErrorExitsNonZero(t *testing.T) {
	isolate(t)
	fixedNow(t)
	cfg := writeCfg(t, "no_such_key: true\n")
	if _, err := runCLI(t, "--config", cfg, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "pg-router", "pull"); err == nil {
		t.Error("an unreadable configuration must make pg-router mode exit non-zero")
	}
}

func TestPullPGRouterCrashedConnectorExitsNonZeroAndTouchesNoTracker(t *testing.T) {
	isolate(t)
	fixedNow(t)
	rec := fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: "", Exit: 1})
	out, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "pg-router", "pull")
	if exitCode(t, err) == 0 {
		t.Error("a pg-connector that crashes with no JSON must make pg-router mode exit non-zero")
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("out = %q; want an empty array", out)
	}
	for _, c := range rec.Calls() {
		if c[0] == "issue" {
			t.Errorf("an issue route was called although the pull could not run: %q", c)
		}
	}
}

func TestPullPGRouterAbsentConnectorExitsNonZero(t *testing.T) {
	isolate(t)
	fixedNow(t)
	t.Setenv("PATH", t.TempDir())
	_, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "pg-router", "pull")
	if exitCode(t, err) == 0 {
		t.Error("an absent pg-connector must make pg-router mode exit non-zero")
	}
}

func TestPullPGRouterStoreLockedExitsNonZero(t *testing.T) {
	isolate(t)
	fixedNow(t)
	rec := fake.Install(t, fake.Route{Match: []string{"activity"}, Stdout: degradedDoc})
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

	if _, err := runCLI(t, "--store", db, "--output", "pg-router", "pull"); exitCode(t, err) == 0 {
		t.Error("a store that stays locked must make pg-router mode exit non-zero")
	}
	for _, c := range rec.Calls() {
		if c[0] == "issue" {
			t.Errorf("an issue route was called although the store was locked: %q", c)
		}
	}
}

func TestPullPGRouterTrackerFailureIsNotAPullFailure(t *testing.T) {
	isolate(t)
	fixedNow(t)
	fake.Install(
		t,
		fake.Route{Match: []string{"activity", "list"}, Stdout: degradedDoc},
		fake.Route{Match: []string{"issue", "list"}, Stdout: "", Exit: 1},
	)
	out, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "pg-router", "pull")
	if err != nil {
		t.Errorf("an unreachable tracker must not make the process exit non-zero: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("out = %q; the source's item is omitted", out)
	}
}

func TestPullReconcilesInEveryOutputMode(t *testing.T) {
	for _, mode := range []string{"json", "human"} {
		t.Run(mode, func(t *testing.T) {
			isolate(t)
			fixedNow(t)
			rec := fake.Install(
				t,
				fake.Route{Match: []string{"activity", "list"}, Stdout: degradedDoc},
				fake.IssueListRoute(), fake.ConfigValidateRoute(), fake.IssueCreateRoute("bd-7"),
			)
			out, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", mode,
				"pull", "--range", "last-48h", "--source", "backend-slow")
			if got := exitCode(t, err); got != 3 {
				t.Errorf("exit = %d; the degraded exit scheme must be unchanged outside pg-router mode", got)
			}
			if strings.Contains(out, "bd-7") {
				t.Errorf("only pg-router mode prints items:\n%s", out)
			}
			creates := issueCalls(rec.Calls(), "create")
			if len(creates) != 1 {
				t.Fatalf("create calls = %v", creates)
			}
			if !strings.Contains(strings.Join(creates[0], "\x00"), "work-report pull --range last-48h --source backend-slow") {
				t.Errorf("create argv lacks the exact re-pull command: %q", creates[0])
			}
		})
	}
}

func TestPullPassesTheEnvBackendToEveryIssueCall(t *testing.T) {
	for name, tc := range map[string]struct{ env, want string }{
		"default":  {"", "pg-connector-issue-beads"},
		"override": {"pg-connector-issue-beads-pg2", "pg-connector-issue-beads-pg2"},
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			fixedNow(t)
			t.Setenv(degraded.EnvBackend, tc.env)
			rec := fake.Install(
				t,
				fake.Route{Match: []string{"activity", "list"}, Stdout: degradedDoc},
				fake.IssueListRoute(), fake.ConfigValidateRoute(), fake.IssueCreateRoute("bd-7"),
			)
			_, _ = runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "json",
				"pull", "--range", "last-48h", "--source", "backend-slow")
			issue := 0
			for _, c := range rec.Calls() {
				if c[0] != "issue" {
					continue
				}
				issue++
				for i, a := range c {
					if a == "--backend" && c[i+1] != tc.want {
						t.Errorf("issue %s used --backend %q; want %q", c[1], c[i+1], tc.want)
					}
				}
			}
			if issue == 0 {
				t.Fatal("no issue calls were made")
			}
		})
	}
}

func TestPullOutputFormatRejectsUnknownAndOtherVerbsRejectPGRouter(t *testing.T) {
	isolate(t)
	if _, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "pg-routerr", "pull"); err == nil {
		t.Error("an unknown --output value must be rejected")
	}
	if _, err := runCLI(t, "--store", filepath.Join(t.TempDir(), "s.db"), "--output", "pg-router", "status"); err == nil {
		t.Error("--output pg-router is a pull-only mode")
	}
}
