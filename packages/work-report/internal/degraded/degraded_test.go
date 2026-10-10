package degraded_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/degraded"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn/fake"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pull"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
)

const repull = "work-report pull --range last-48h --source backend-a"

var (
	chicago = mustZone("America/Chicago")
	// 2026-03-02 23:30 in Chicago, so the local day ends at local midnight and
	// differs from the UTC day.
	now = time.Date(2026, 3, 2, 23, 30, 0, 0, chicago)
	rng = rangespec.Range{
		Since:  time.Date(2026, 2, 28, 23, 30, 0, 0, chicago),
		Before: now,
	}
)

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func degradedRow(source string) pull.OutcomeRow {
	return pull.OutcomeRow{Source: source, Status: pull.StatusDegraded, Reason: "upstream timed out", Rejected: 1}
}

func okRow(source string) pull.OutcomeRow {
	return pull.OutcomeRow{Source: source, Status: pull.StatusSucceeded, Count: 3}
}

func reconcile(t *testing.T, rows ...pull.OutcomeRow) ([]degraded.Item, error) {
	t.Helper()
	return degraded.Reconcile(context.Background(), pgconn.NewExec(), "", rows, rng, now, repull)
}

// verbs returns "<noun> <verb>" for each recorded call.
func verbs(calls [][]string) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c[0]+" "+c[1])
	}
	return out
}

func flagValue(call []string, flag string) string {
	for i, a := range call {
		if a == flag && i+1 < len(call) {
			return call[i+1]
		}
	}
	return ""
}

func flagValues(call []string, flag string) []string {
	var out []string
	for i, a := range call {
		if a == flag && i+1 < len(call) {
			out = append(out, call[i+1])
		}
	}
	return out
}

func find(calls [][]string, noun, verb string) [][]string {
	var out [][]string
	for _, c := range calls {
		if c[0] == noun && c[1] == verb {
			out = append(out, c)
		}
	}
	return out
}

func TestFirstDegradedPullCreatesOneBead(t *testing.T) {
	t.Setenv("PG_CONNECTOR_ISSUE_BEADS_DIR", t.TempDir())
	rec := fake.Install(
		t,
		fake.IssueListRoute(),
		fake.ConfigValidateRoute(fake.ConfigRow{Source: "backend-a", Status: "degraded", Count: 1, Reason: "auth_status: token expired"}),
		fake.IssueCreateRoute("bd-1"),
	)
	items, err := reconcile(t, degradedRow("backend-a"), okRow("backend-b"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "bd-1" || items[0].Title != "work-report: backend-a degraded" || items[0].Type != "issue" {
		t.Fatalf("items = %+v", items)
	}
	if !reflect.DeepEqual(items[0].Metadata, degradedRow("backend-a")) {
		t.Errorf("metadata = %+v; want the outcome row", items[0].Metadata)
	}

	calls := rec.Calls()
	list := find(calls, "issue", "list")
	if len(list) != 1 {
		t.Fatalf("issue list calls = %v", list)
	}
	if flagValue(list[0], "--query") != "escalated-all" || flagValue(list[0], "--backend") != "" {
		t.Errorf("dedup lookup argv = %q; want --query escalated-all and no --backend pin (the tracker is discovered from the fan-out)", list[0])
	}
	for _, c := range calls {
		for _, a := range c {
			if a == "escalated-work" {
				t.Errorf("the ready-only escalated-work query MUST NOT be used: %q", c)
			}
		}
	}

	create := find(calls, "issue", "create")
	if len(create) != 1 {
		t.Fatalf("issue create calls = %v", create)
	}
	c := create[0]
	if got := flagValue(c, "--title"); got != "work-report: backend-a degraded" {
		t.Errorf("title = %q", got)
	}
	labels := flagValues(c, "--labels")
	sort.Strings(labels)
	if !reflect.DeepEqual(labels, []string{"escalated", "work-report"}) {
		t.Errorf("labels = %v", labels)
	}
	if flagValue(c, "--backend") != fake.TrackerBackend {
		t.Errorf("create is not pinned to the discovered tracker %s: %q", fake.TrackerBackend, c)
	}
	body := flagValue(c, "--description")
	for _, want := range []string{
		"upstream timed out", // the reason
		"2026-02-28T23:30:00-06:00 .. 2026-03-02T23:30:00-06:00", // the range, in the configured zone
		repull,                                  // the exact re-pull command
		`"reason":"auth_status: token expired"`, // the config validate row
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bead body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "backend-b") {
		t.Errorf("a succeeded source leaked into another source's bead:\n%s", body)
	}
	if got := len(find(calls, "config", "validate")); got != 1 {
		t.Errorf("config validate ran %d times; want once", got)
	}
}

func TestSecondDegradedPullAppendsAndCreatesNothing(t *testing.T) {
	rec := fake.Install(
		t,
		fake.IssueListRoute(
			fake.IssueEntity{ID: "bd-other", Title: "work-report: backend-z degraded"},
			fake.IssueEntity{ID: "bd-1", Title: "work-report: backend-a degraded", Labels: []string{"escalated", "work-report"}},
		),
		fake.IssueCommentRoute(),
	)
	items, err := reconcile(t, degradedRow("backend-a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "bd-1" {
		t.Fatalf("items = %+v; want the existing bead", items)
	}
	calls := rec.Calls()
	if got := verbs(calls); !reflect.DeepEqual(got, []string{"issue list", "issue comment"}) {
		t.Fatalf("calls = %v; want only list then comment", got)
	}
	cm := calls[1]
	if cm[2] != "bd-1" || !strings.Contains(flagValue(cm, "--body"), "upstream timed out") {
		t.Errorf("comment argv = %q", cm)
	}
}

func TestBeadHeldByHumanOrClaimedIsStillFound(t *testing.T) {
	// the dedup query is non-ready: a human-labeled, claimed or deferred bead is
	// returned by it, and must be appended to, never duplicated.
	for _, state := range []string{"in_progress", "deferred", "blocked"} {
		t.Run(state, func(t *testing.T) {
			rec := fake.Install(
				t,
				fake.IssueListRoute(fake.IssueEntity{
					ID: "bd-9", Title: "work-report: backend-a degraded",
					Labels: []string{"escalated", "human", "work-report"}, State: state,
				}),
				fake.IssueCommentRoute(),
			)
			items, err := reconcile(t, degradedRow("backend-a"))
			if err != nil || len(items) != 1 || items[0].ID != "bd-9" {
				t.Fatalf("items=%+v err=%v", items, err)
			}
			if got := len(find(rec.Calls(), "issue", "create")); got != 0 {
				t.Errorf("created %d beads for a source whose bead already exists", got)
			}
		})
	}
}

func TestSucceedingPullClosesTheBeadWithAReason(t *testing.T) {
	rec := fake.Install(
		t,
		fake.IssueListRoute(fake.IssueEntity{ID: "bd-1", Title: "work-report: backend-a degraded"}),
		fake.IssueCloseRoute(),
	)
	items, err := reconcile(t, okRow("backend-a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("items = %+v; a healthy pull emits nothing", items)
	}
	closes := find(rec.Calls(), "issue", "close")
	if len(closes) != 1 || closes[0][2] != "bd-1" {
		t.Fatalf("close calls = %v", closes)
	}
	if reason := flagValue(closes[0], "--reason"); !strings.Contains(reason, "backend-a") || !strings.Contains(reason, "succeeded") {
		t.Errorf("reason = %q", reason)
	}
}

func TestHealthyPullWithNoBeadMakesNoWrites(t *testing.T) {
	rec := fake.Install(t, fake.IssueListRoute())
	items, err := reconcile(t, okRow("backend-a"))
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if got := verbs(rec.Calls()); !reflect.DeepEqual(got, []string{"issue list"}) {
		t.Errorf("calls = %v", got)
	}
}

func TestDisabledRowsNeverTouchTheTracker(t *testing.T) {
	rec := fake.Install(t) // any call exits 64
	items, err := reconcile(t, pull.OutcomeRow{Source: "backend-a", Status: pull.StatusDisabled, Reason: pull.ReasonDisabledByConfig})
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("calls = %v", rec.Calls())
	}
}

func TestExpiresAtIsEndOfLocalDayPlusSixHours(t *testing.T) {
	fake.Install(t, fake.IssueListRoute(), fake.ConfigValidateRoute(), fake.IssueCreateRoute("bd-1"))
	items, err := reconcile(t, degradedRow("backend-a"))
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	want := time.Date(2026, 3, 3, 6, 0, 0, 0, chicago)
	if !items[0].ExpiresAt.Equal(want) {
		t.Errorf("expiresAt = %v; want %v (local midnight + 6h, not UTC midnight)", items[0].ExpiresAt, want)
	}
}

func TestItemJSONKeys(t *testing.T) {
	fake.Install(t, fake.IssueListRoute(), fake.ConfigValidateRoute(), fake.IssueCreateRoute("bd-1"))
	items, err := reconcile(t, degradedRow("backend-a"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"expiresAt", "id", "metadata", "title", "type"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v; want %v", keys, want)
	}
	var meta map[string]any
	if err := json.Unmarshal(m["metadata"], &meta); err != nil || meta["source"] != "backend-a" || meta["status"] != "degraded" {
		t.Errorf("metadata = %s (%v)", m["metadata"], err)
	}
}

func TestTrackerUnreachableReturnsErrorAndNoItem(t *testing.T) {
	fake.Install(t, fake.Route{Match: []string{"issue", "list"}, Stdout: "", Exit: 1})
	items, err := reconcile(t, degradedRow("backend-a"))
	if err == nil {
		t.Error("want an error when the dedup lookup fails")
	}
	if items == nil || len(items) != 0 {
		t.Errorf("items = %#v; want an empty non-nil slice", items)
	}
}

func TestPartialFailureKeepsTheItemsThatWereBuilt(t *testing.T) {
	fake.Install(
		t,
		fake.IssueListRoute(fake.IssueEntity{ID: "bd-2", Title: "work-report: backend-b degraded"}),
		fake.ConfigValidateRoute(),
		fake.Route{Match: []string{"issue", "create"}, Stdout: "", Exit: 1}, // creating backend-a's bead fails
		fake.IssueCommentRoute(),
	)
	items, err := reconcile(t, degradedRow("backend-a"), degradedRow("backend-b"))
	if err == nil || !strings.Contains(err.Error(), "backend-a") {
		t.Errorf("err = %v; want it to name backend-a", err)
	}
	if len(items) != 1 || items[0].ID != "bd-2" {
		t.Errorf("items = %+v; want the backend-b item only", items)
	}
}

func TestConfigValidateFailureDoesNotBlockCreation(t *testing.T) {
	rec := fake.Install(
		t,
		fake.IssueListRoute(),
		fake.Route{Match: []string{"config", "validate"}, Stdout: "", Exit: 1},
		fake.IssueCreateRoute("bd-1"),
	)
	items, err := reconcile(t, degradedRow("backend-a"))
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	body := flagValue(find(rec.Calls(), "issue", "create")[0], "--description")
	if !strings.Contains(body, "unavailable") {
		t.Errorf("body should say the validate row is unavailable:\n%s", body)
	}
}

func TestAbsentConnectorIsAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	items, err := reconcile(t, degradedRow("backend-a"))
	if err == nil || len(items) != 0 {
		t.Errorf("items=%+v err=%v", items, err)
	}
}

// registeredIssueBackends are the issue backend names the live pg-connector
// registry exposes today (jira, and one beads instance per tracker). The stale
// literal pg-connector-issue-beads is deliberately NOT among them: it was
// retired by the instance split, and pinning --backend to it makes pg-connector
// fail with "not registered" (pg2-sqc5v).
var registeredIssueBackends = []string{"pg-connector-issue-jira", "pg-connector-issue-beads-zr", "pg-connector-issue-beads-pg2"}

// registeredBackendsRoute is the fan-out answer where only the last registered
// backend defines the dedup query; the others report disabled.
func registeredBackendsRoute(entities ...fake.IssueEntity) fake.Route {
	var rows []fake.SourceRow
	for i, name := range registeredIssueBackends {
		if i == len(registeredIssueBackends)-1 {
			rows = append(rows, fake.SourceRow{Source: name, Status: "succeeded", Count: len(entities)})
		} else {
			rows = append(rows, fake.SourceRow{Source: name, Status: "disabled", Reason: "not applicable: query not recognized"})
		}
	}
	return fake.IssueListRouteSources(rows, entities...)
}

// TestDiscoveredBackendIsARegisteredName pins the backend lookup against the
// registered backend names: with no override, every issue call after the
// unpinned lookup passes --backend equal to a name the registry reports, never
// a literal the code carries.
func TestDiscoveredBackendIsARegisteredName(t *testing.T) {
	rec := fake.Install(
		t,
		registeredBackendsRoute(fake.IssueEntity{ID: "bd-1", Title: "work-report: backend-a degraded"}, fake.IssueEntity{ID: "bd-2", Title: "work-report: backend-c degraded"}),
		fake.ConfigValidateRoute(),
		fake.IssueCreateRoute("bd-3"),
		fake.IssueCommentRoute(),
		fake.IssueCloseRoute(),
	)
	if _, err := reconcile(t, degradedRow("backend-a"), degradedRow("backend-b"), okRow("backend-c")); err != nil {
		t.Fatal(err)
	}
	want := registeredIssueBackends[len(registeredIssueBackends)-1]
	var seen []string
	for _, c := range rec.Calls() {
		if c[0] != "issue" {
			continue
		}
		seen = append(seen, c[1])
		if c[1] == "list" {
			if got := flagValue(c, "--backend"); got != "" {
				t.Errorf("the discovery lookup must be unpinned, got --backend %q", got)
			}
			continue
		}
		got := flagValue(c, "--backend")
		if got != want {
			t.Errorf("issue %s used --backend %q; want the registered tracker %q", c[1], got, want)
		}
		if !contains(registeredIssueBackends, got) {
			t.Errorf("issue %s used --backend %q, which is not a registered backend %v", c[1], got, registeredIssueBackends)
		}
	}
	sort.Strings(seen)
	if !reflect.DeepEqual(seen, []string{"close", "comment", "create", "list"}) {
		t.Errorf("issue verbs exercised = %v; want list, comment, create and close", seen)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestAmbiguousOrAbsentTrackerIsAnErrorNamingTheRemedy(t *testing.T) {
	cases := map[string]struct {
		sources []fake.SourceRow
		want    []string
	}{
		"several backends define the query": {
			sources: []fake.SourceRow{
				{Source: "pg-connector-issue-beads-zr", Status: "succeeded"},
				{Source: "pg-connector-issue-beads-pg2", Status: "succeeded"},
			},
			want: []string{"several", "pg-connector-issue-beads-zr", "pg-connector-issue-beads-pg2", degraded.EnvBackend},
		},
		"no backend defines the query": {
			sources: []fake.SourceRow{{Source: "pg-connector-issue-jira", Status: "disabled", Reason: "query not recognized"}},
			want:    []string{"no registered issue backend", "pg-connector-issue-jira", degraded.EnvBackend},
		},
		"the only candidate is degraded": {
			sources: []fake.SourceRow{
				{Source: "pg-connector-issue-jira", Status: "disabled"},
				{Source: "pg-connector-issue-beads-pg2", Status: "degraded", Reason: "dolt unreachable"},
			},
			want: []string{"pg-connector-issue-beads-pg2", "degraded", "dolt unreachable"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := fake.Install(t, fake.IssueListRouteSources(tc.sources), fake.ConfigValidateRoute(), fake.IssueCreateRoute("bd-1"))
			items, err := reconcile(t, degradedRow("backend-a"))
			if err == nil || len(items) != 0 {
				t.Fatalf("items=%+v err=%v; want an error and no items", items, err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			if got := find(rec.Calls(), "issue", "create"); len(got) != 0 {
				t.Errorf("no bead may be created against a guessed tracker: %v", got)
			}
		})
	}
}

func TestOverriddenBackendIsPassedOnEveryCall(t *testing.T) {
	const override = "pg-connector-issue-beads-zr"
	rec := fake.Install(
		t,
		fake.IssueListRoute(fake.IssueEntity{ID: "bd-1", Title: "work-report: backend-a degraded"}, fake.IssueEntity{ID: "bd-2", Title: "work-report: backend-c degraded"}),
		fake.ConfigValidateRoute(),
		fake.IssueCreateRoute("bd-3"),
		fake.IssueCommentRoute(),
		fake.IssueCloseRoute(),
	)
	_, err := degraded.Reconcile(context.Background(), pgconn.NewExec(), override,
		[]pull.OutcomeRow{degradedRow("backend-a"), degradedRow("backend-b"), okRow("backend-c")}, rng, now, repull)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, c := range rec.Calls() {
		if c[0] != "issue" {
			continue
		}
		seen = append(seen, c[1])
		if got := flagValue(c, "--backend"); got != override {
			t.Errorf("issue %s used --backend %q; want %q", c[1], got, override)
		}
	}
	sort.Strings(seen)
	if !reflect.DeepEqual(seen, []string{"close", "comment", "create", "list"}) {
		t.Errorf("issue verbs exercised = %v; want list, comment, create and close", seen)
	}
}
