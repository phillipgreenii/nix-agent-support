package main

// The bounded work-item lookup of a pr run, through the real planFn and applyFn
// and the real rules: the view comes from the pg-desk double, the tracker from a
// connector double that answers `issue list --query work-items-all` with a
// listing the test controls and records every other call.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/metrics"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

const lookupView = "views/pr_new_everything.json"

// realLookup puts the real reader back in place of the test-wide stub.
func realLookup(t *testing.T) {
	t.Helper()
	orig := lookupRead
	t.Cleanup(func() { lookupRead = orig })
	lookupRead = func(ctx context.Context, env apply.Env, v *view.View) (*apply.Lookup, error) {
		return apply.ReadLookup(ctx, env, v)
	}
}

// lookupConfig writes the decider configuration file with the lookup keys and
// points PG_DECIDER_CONFIG at it. extra is more JSON members (with a leading
// comma) or "".
func lookupConfig(t *testing.T, members string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "decider.json")
	writeFile(t, p, "{"+members+"}")
	t.Setenv("PG_DECIDER_CONFIG", p)
}

type connectorDouble struct{ calls []execRec }

// trackerDouble swaps applyRunner for an in-process connector double: `issue
// list` answers listJSON and exits listExit; every other call answers a
// created id and succeeds. It records every call.
func trackerDouble(t *testing.T, listJSON string, listExit int) *connectorDouble {
	t.Helper()
	d := &connectorDouble{}
	orig := applyRunner
	t.Cleanup(func() { applyRunner = orig })
	n := 0
	applyRunner = func(_ context.Context, c apply.Call) apply.CallResult {
		name := c.Name
		joined := strings.Join(c.Args, " ")
		d.calls = append(d.calls, execRec{name, joined})
		out, code := `{"result":{}}`, 0
		switch {
		case name == "pg-connector" && strings.HasPrefix(joined, "issue list"):
			out, code = listJSON, listExit
		case name == "pg-connector" && strings.HasPrefix(joined, "issue create"):
			n++
			out = `{"result":{"id":"wb-new-` + strconv.Itoa(n) + `"}}`
		}
		return apply.CallResult{Stdout: []byte(out + "\n"), Code: code}
	}
	return d
}

// writes are the calls that change the tracker or pg-desk (everything except
// the read-only list and the refresh that follows a write).
func (d *connectorDouble) writes() []execRec {
	var out []execRec
	for _, c := range d.calls {
		if strings.HasPrefix(c.args, "issue list") || strings.HasPrefix(c.args, "issue refresh") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (d *connectorDouble) listCalls() int {
	n := 0
	for _, c := range d.calls {
		if strings.HasPrefix(c.args, "issue list") {
			n++
		}
	}
	return n
}

// createdKinds are the kinds of the issues the run created, read from each
// create's dedup_key (the connector argv carries it as --metadata).
func (d *connectorDouble) createdKinds() []string {
	var out []string
	for _, c := range d.calls {
		if !strings.HasPrefix(c.args, "issue create") {
			continue
		}
		i := strings.Index(c.args, "dedup_key=")
		if i < 0 {
			out = append(out, "?")
			continue
		}
		key := c.args[i+len("dedup_key="):]
		if j := strings.IndexAny(key, " \""); j >= 0 {
			key = key[:j]
		}
		parts := strings.SplitN(key, ":", 4)
		out = append(out, parts[2])
	}
	sort.Strings(out)
	return out
}

func listingOf(items ...string) string {
	return `{"entities":[` + strings.Join(items, ",") + `],"present_ids":[],"sources":[{"source":"beads","status":"succeeded","count":` + strconv.Itoa(len(items)) + `}],"truncated":false}`
}

func closedItem(id, title, key string) string {
	return `{"id":"` + id + `","title":"` + title + `","state":"closed","metadata":{"repo":"acme/widgets","pr_number":"42","dedup_key":"` + key + `"}}`
}

// closedReviewItem is a closed review item reviewed at the view's head: the rule
// reads its head_sha, never its key.
func closedReviewItem(id string) string {
	return `{"id":"` + id + `","title":"review-pr: acme/widgets#42 @ 9f3c1e2","state":"closed","metadata":{"repo":"acme/widgets","pr_number":"42","head_sha":"9f3c1e2","dedup_key":"` + keyReview + `"}}`
}

const (
	keyFixCI  = "pr:acme/widgets#42:fix-ci:9f3c1e2"
	keyReview = "pr:acme/widgets#42:review-pr:9f3c1e2"
)

type planDoc struct {
	Actions []struct {
		Op, Kind, Rule string
		Fields         struct {
			Metadata map[string]string `json:"metadata"`
		} `json:"fields"`
	} `json:"actions"`
	Skipped []struct {
		Rule, Reason string
		Facts        map[string]any
	} `json:"skipped"`
	LookupItems *int `json:"lookup_items"`
}

func planKinds(p planDoc) []string {
	var out []string
	for _, a := range p.Actions {
		if a.Op == "create" {
			out = append(out, a.Kind)
		}
	}
	sort.Strings(out)
	return out
}

func runPlanJSON(t *testing.T) (planDoc, string, int) {
	t.Helper()
	out, errOut, code := runCLI(t, "plan", "pr", "acme/widgets#42", "--json")
	var p planDoc
	if code == exitcode.OK {
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
	}
	return p, errOut, code
}

// A closed fix-ci item the view does not link is exactly what the gather's
// work-beads query hides: with the listing the plan does not mint it again;
// without it (the negative control) the plan does.
func TestPlanSeesAClosedItemTheViewDoesNotLinkAndReportsTheFigures(t *testing.T) {
	realLookup(t)
	lookupConfig(t, `"work_items_query":"work-items-all"`)

	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
	trackerDouble(t, listingOf(closedItem("ci-closed", "fix-ci: acme/widgets#42", keyFixCI), closedReviewItem("rv-closed")), 0)
	p, errOut, code := runPlanJSON(t)
	if code != exitcode.OK {
		t.Fatalf("code %d err %q", code, errOut)
	}
	got := strings.Join(planKinds(p), ",")
	if strings.Contains(got, "fix-ci") || strings.Contains(got, "review-pr") {
		t.Fatalf("the plan creates a kind the listing already holds: %s", got)
	}
	for _, want := range []string{"anchor", "process-feedback", "resolve-conflict"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan lost the create of %s: %s", want, got)
		}
	}
	if p.LookupItems == nil || *p.LookupItems != 2 {
		t.Fatalf("lookup_items = %v, want 2", p.LookupItems)
	}

	// Negative control: an empty listing mints the fix-ci item again.
	trackerDouble(t, listingOf(), 0)
	p, errOut, code = runPlanJSON(t)
	if code != exitcode.OK {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if got := strings.Join(planKinds(p), ","); !strings.Contains(got, "fix-ci") || !strings.Contains(got, "review-pr") {
		t.Fatalf("with an empty listing the plan must create fix-ci and review-pr: %s", got)
	}
	if p.LookupItems == nil || *p.LookupItems != 0 {
		t.Errorf("an empty listing must report lookup_items 0, got %v", p.LookupItems)
	}
}

func TestPlanTextNamesTheListingSize(t *testing.T) {
	realLookup(t)
	lookupConfig(t, `"work_items_query":"work-items-all"`)
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
	trackerDouble(t, listingOf(closedItem("ci-closed", "fix-ci: acme/widgets#42", keyFixCI)), 0)
	out, errOut, code := runCLI(t, "plan", "pr", "acme/widgets#42")
	if code != exitcode.OK {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if !strings.Contains(out, "\nlookup: 1 work items listed\n") || !strings.Contains(out, "fix-ci (closed)") {
		t.Fatalf("plan text:\n%s", out)
	}
}

// plan and apply run on the same state and agree per kind and operation: every
// create the plan lists is a create the apply writes, and no other.
func TestPlanAndApplyAgreePerKindAndOperation(t *testing.T) {
	realLookup(t)
	lookupConfig(t, `"work_items_query":"work-items-all"`)
	for name, listing := range map[string]string{
		"empty listing": listingOf(),
		"one held":      listingOf(closedItem("ci-closed", "fix-ci: acme/widgets#42", keyFixCI)),
		"two held":      listingOf(closedItem("ci-closed", "fix-ci: acme/widgets#42", keyFixCI), closedReviewItem("rv-closed")),
	} {
		t.Run(name, func(t *testing.T) {
			withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
			planDouble := trackerDouble(t, listing, 0)
			p, errOut, code := runPlanJSON(t)
			if code != exitcode.OK {
				t.Fatalf("plan: code %d err %q", code, errOut)
			}
			if len(planDouble.writes()) != 0 {
				t.Fatalf("plan wrote: %+v", planDouble.writes())
			}
			ops := map[string]int{}
			for _, a := range p.Actions {
				ops[a.Kind+"/"+a.Op]++
			}

			applyDouble := trackerDouble(t, listing, 0)
			if _, errOut, code = runCLI(t, "apply", "pr", "acme/widgets#42"); code != exitcode.OK {
				t.Fatalf("apply: code %d err %q", code, errOut)
			}
			if got, want := strings.Join(applyDouble.createdKinds(), ","), strings.Join(planKinds(p), ","); got != want {
				t.Fatalf("apply created %q, the plan lists creates of %q", got, want)
			}
			// The same listing, read once, by each command.
			if planDouble.listCalls() != 1 || applyDouble.listCalls() != 1 {
				t.Errorf("list calls: plan %d, apply %d, want 1 each", planDouble.listCalls(), applyDouble.listCalls())
			}
		})
	}
}

// With every kind already in the listing (what a second run sees in
// production), apply writes nothing.
func TestApplyAgainstAListingThatHoldsEveryItemWritesNothing(t *testing.T) {
	realLookup(t)
	lookupConfig(t, `"work_items_query":"work-items-all"`)
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
	all := listingOf(
		closedItem("an-1", "acme/widgets#42: Retry", "pr:acme/widgets#42:anchor"),
		closedReviewItem("rv-1"),
		closedItem("pf-1", "process-feedback: acme/widgets#42", "pr:acme/widgets#42:process-feedback:5ff7c41ebe23"),
		closedItem("ci-1", "fix-ci: acme/widgets#42", keyFixCI),
		closedItem("rc-1", "resolve-conflict: acme/widgets#42", "pr:acme/widgets#42:resolve-conflict:feature/retry:9f3c1e2:main:b45e000"),
	)
	d := trackerDouble(t, all, 0)
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42")
	if code != exitcode.OK {
		t.Fatalf("code %d err %q", code, errOut)
	}
	for _, c := range d.writes() {
		if strings.HasPrefix(c.args, "issue create") {
			t.Errorf("a create was written against a listing that holds the item: %+v", c)
		}
	}
	if got := d.createdKinds(); len(got) != 0 {
		t.Fatalf("created %v", got)
	}
}

func TestApplyCountersLineCarriesTheLookupFigures(t *testing.T) {
	realLookup(t)
	lookupConfig(t, `"work_items_query":"work-items-all"`)
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
	trackerDouble(t, listingOf(closedItem("ci-closed", "fix-ci: acme/widgets#42", keyFixCI)), 0)
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42")
	if code != exitcode.OK {
		t.Fatalf("code %d err %q", code, errOut)
	}
	line := countersLine(t, errOut)
	if line.LookupItems == nil || *line.LookupItems != 1 {
		t.Fatalf("lookup_items = %v in %s", line.LookupItems, errOut)
	}
	if strings.Contains(errOut, "lookup_ceiling") {
		t.Fatalf("the retired lookup_ceiling is still reported: %s", errOut)
	}
}

func countersLine(t *testing.T, stderr string) metrics.Line {
	t.Helper()
	for _, l := range strings.Split(stderr, "\n") {
		if strings.Contains(l, metrics.Contract) {
			var line metrics.Line
			if err := json.Unmarshal([]byte(l), &line); err != nil {
				t.Fatalf("counters line: %v: %s", err, l)
			}
			return line
		}
	}
	t.Fatalf("no counters line in %q", stderr)
	return metrics.Line{}
}

// A lookup that cannot be trusted fails the run closed, plan and apply alike:
// exit 4, nothing printed as a plan, nothing written, and the failure line
// still reports the figures known (the count, when a listing was read).
func TestAnUntrustedLookupFailsTheRunClosedAndStillReportsTheFigures(t *testing.T) {
	three := listingOf(closedItem("a", "t", "pr:acme/widgets#42:anchor"), closedItem("b", "t", keyFixCI), closedItem("c", "t", keyReview))
	cases := []struct {
		name       string
		members    string
		listing    string
		listExit   int
		reason     string
		wantItems  int // -1 = not reported
		wantInText string
	}{
		{
			"truncated", `"work_items_query":"work-items-all"`,
			`{"entities":[` + closedItem("a", "t", keyFixCI) + `],"sources":[{"source":"beads","status":"succeeded","count":1}],"truncated":true}`, 0, "lookup-truncated", 1, "truncated",
		},
		{
			"unavailable (the call fails)", `"work_items_query":"work-items-all"`,
			`{"error":{"code":"unavailable","message":"down"}}`, 1, "lookup-unavailable", -1, "unavailable",
		},
		{
			"unavailable (a source degraded)", `"work_items_query":"work-items-all"`,
			`{"entities":[],"sources":[{"source":"beads","status":"degraded","count":0,"reason":"cache"}],"truncated":false}`, 0, "lookup-unavailable", -1, "degraded",
		},
		{"query key absent", ``, three, 0, "lookup-query-absent", -1, "work_items_query is not configured"},
	}
	for _, tc := range cases {
		for _, verb := range []string{"plan", "apply"} {
			t.Run(tc.name+"/"+verb, func(t *testing.T) {
				realLookup(t)
				lookupConfig(t, tc.members)
				withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
				d := trackerDouble(t, tc.listing, tc.listExit)
				args := []string{verb, "pr", "acme/widgets#42"}
				if verb == "plan" {
					args = append(args, "--json")
				}
				out, errOut, code := runCLI(t, args...)
				if code != exitcode.LookupUntrusted {
					t.Fatalf("exit %d, want %d; err %q", code, exitcode.LookupUntrusted, errOut)
				}
				if out != "" {
					t.Errorf("stdout is not empty: %q", out)
				}
				if !strings.Contains(errOut, tc.wantInText) {
					t.Errorf("stderr lacks %q:\n%s", tc.wantInText, errOut)
				}
				if w := d.writes(); len(w) != 0 {
					t.Errorf("a failed-closed run wrote: %+v", w)
				}
				line := countersLine(t, errOut)
				if line.Failures[tc.reason] != 1 || len(line.Rules) != 0 {
					t.Errorf("failure line = %+v, want failures[%s]=1 and no rules", line, tc.reason)
				}
				check := func(what string, got *int, want int) {
					switch {
					case want < 0 && got != nil:
						t.Errorf("%s = %d reported, want it absent", what, *got)
					case want >= 0 && (got == nil || *got != want):
						t.Errorf("%s = %v, want %d", what, got, want)
					}
				}
				check("lookup_items", line.LookupItems, tc.wantItems)
				if line.LookupMs == nil || *line.LookupMs < 0 {
					t.Errorf("lookup_ms = %v, want a measured duration on the failed-closed line", line.LookupMs)
				}
			})
		}
	}
}

// There is no size ceiling (retired, pg2-eclw3j.9): a config that still carries
// the old work_items_ceiling key is not checked against it. A run over a
// listing larger than that stale value succeeds, reports lookup_items on the
// counters line, and reports no lookup_ceiling.
func TestNoCeilingIsCheckedAndLookupItemsIsReportedPerRun(t *testing.T) {
	three := listingOf(closedItem("a", "t", "pr:acme/widgets#42:anchor"), closedItem("b", "t", keyFixCI), closedItem("c", "t", keyReview))
	for _, verb := range []string{"plan", "apply"} {
		t.Run(verb, func(t *testing.T) {
			realLookup(t)
			lookupConfig(t, `"work_items_query":"work-items-all","work_items_ceiling":2`)
			withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
			trackerDouble(t, three, 0)
			args := []string{verb, "pr", "acme/widgets#42"}
			if verb == "plan" {
				args = append(args, "--json")
			}
			out, errOut, code := runCLI(t, args...)
			if code != exitcode.OK {
				t.Fatalf("exit %d, want OK: a stale ceiling of 2 must not fail a listing of 3; err %q", code, errOut)
			}
			if strings.Contains(out+errOut, "lookup_ceiling") {
				t.Errorf("the retired lookup_ceiling is reported:\n%s\n%s", out, errOut)
			}
			if verb == "apply" {
				if line := countersLine(t, errOut); line.LookupItems == nil || *line.LookupItems != 3 {
					t.Errorf("lookup_items = %v, want 3 in %s", line.LookupItems, errOut)
				}
			}
		})
	}
}

// An issue run makes no lookup: its dedup lookups are unchanged and its config
// needs none of the keys.
func TestAnIssueRunMakesNoWorkItemLookup(t *testing.T) {
	realLookup(t)
	lookupConfig(t, ``)
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("views/issue_focus_selected.json"))
	d := trackerDouble(t, listingOf(), 0)
	if _, errOut, code := runCLI(t, "plan", "issue", "ACME-7", "--json"); code != exitcode.OK {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if d.listCalls() != 0 {
		t.Errorf("an issue plan made %d list call(s)", d.listCalls())
	}
}

// steppingClock returns a clock that advances by step on every call.
func steppingClock(step time.Duration) func() time.Time {
	t := time.Unix(1_700_000_000, 0)
	return func() time.Time { t = t.Add(step); return t }
}

// The lookup is timed once: the success path stamps the Lookup with its
// elapsed time (the counters hook prints it as lookup_ms), the failure path
// prints it on the failed-closed line.
func TestReadRunLookupTimesTheLookup(t *testing.T) {
	origRead, origNow := lookupRead, lookupNow
	t.Cleanup(func() { lookupRead, lookupNow = origRead, origNow })
	lookupNow = steppingClock(1500 * time.Millisecond)

	lookupRead = func(context.Context, apply.Env, *view.View) (*apply.Lookup, error) {
		return apply.NewLookup(nil), nil
	}
	var errOut bytes.Buffer
	lk, ok := readRunLookup(context.Background(), &errOut, &config.Config{}, nil, "apply", "pr", "acme/widgets#42")
	if !ok || lk.Elapsed() != 1500*time.Millisecond {
		t.Fatalf("ok=%v elapsed=%v", ok, lk.Elapsed())
	}

	lookupRead = func(context.Context, apply.Env, *view.View) (*apply.Lookup, error) {
		return nil, &apply.LookupError{Reason: apply.LookupReasonUnavailable, Err: errors.New("down")}
	}
	errOut.Reset()
	if _, ok := readRunLookup(context.Background(), &errOut, &config.Config{}, nil, "apply", "pr", "acme/widgets#42"); ok {
		t.Fatal("a failed lookup must not be ok")
	}
	if line := countersLine(t, errOut.String()); line.LookupMs == nil || *line.LookupMs != 1500 {
		t.Fatalf("lookup_ms = %v, want 1500", line.LookupMs)
	}
}

// With work_items_query_params the run reads the per-PR query once, passing
// the viewed PR's repository and number (the number normalised) as --param,
// and plan and apply see the answer exactly as they see the whole listing.
func TestPlanAndApplyReadThePerPRQueryWithThePRIdentity(t *testing.T) {
	for _, verb := range []string{"plan", "apply"} {
		t.Run(verb, func(t *testing.T) {
			realLookup(t)
			lookupConfig(t, `"work_items_query":"work-items-for-pr","work_items_query_params":["repo","pr_number"]`)
			withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
			d := trackerDouble(t, listingOf(closedItem("ci-closed", "fix-ci: acme/widgets#42", keyFixCI)), 0)
			args := []string{verb, "pr", "acme/widgets#42"}
			if verb == "plan" {
				args = append(args, "--json")
			}
			if _, errOut, code := runCLI(t, args...); code == exitcode.LookupUntrusted {
				t.Fatalf("code %d err %q", code, errOut)
			}
			if n := d.listCalls(); n != 1 {
				t.Fatalf("%d list calls, want 1", n)
			}
			var line string
			for _, c := range d.calls {
				if strings.HasPrefix(c.args, "issue list") {
					line = c.args
				}
			}
			if !strings.Contains(line, "--query work-items-for-pr --param repo=acme/widgets --param pr_number=42") {
				t.Errorf("list call = %q", line)
			}
		})
	}
}
