package main

// The bounded work-item lookup of a pr run, through the real planFn and applyFn
// and the real rules: the view comes from the pg-desk double, the tracker from a
// connector double that answers `issue list --query work-items-all` with a
// listing the test controls and records every other call.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/metrics"
)

const lookupView = "views/pr_new_everything.json"

// realLookup puts the real reader back in place of the test-wide stub.
func realLookup(t *testing.T) {
	t.Helper()
	orig := lookupRead
	t.Cleanup(func() { lookupRead = orig })
	lookupRead = func(ctx context.Context, env apply.Env) (*apply.Lookup, error) { return apply.ReadLookup(ctx, env) }
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
		return apply.CallResult{Stdout: out + "\n", Code: code}
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
	LookupItems   *int `json:"lookup_items"`
	LookupCeiling *int `json:"lookup_ceiling"`
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
	lookupConfig(t, `"work_items_query":"work-items-all","work_items_ceiling":7000`)

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
	if p.LookupItems == nil || *p.LookupItems != 2 || p.LookupCeiling == nil || *p.LookupCeiling != 7000 {
		t.Fatalf("figures = %v %v, want 2 and 7000", p.LookupItems, p.LookupCeiling)
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

func TestPlanTextNamesTheListingSizeAndTheCeiling(t *testing.T) {
	realLookup(t)
	lookupConfig(t, `"work_items_query":"work-items-all","work_items_ceiling":50`)
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
	trackerDouble(t, listingOf(closedItem("ci-closed", "fix-ci: acme/widgets#42", keyFixCI)), 0)
	out, errOut, code := runCLI(t, "plan", "pr", "acme/widgets#42")
	if code != exitcode.OK {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if !strings.Contains(out, "\nlookup: 1 work items listed, ceiling 50\n") || !strings.Contains(out, "fix-ci (closed)") {
		t.Fatalf("plan text:\n%s", out)
	}
}

// plan and apply run on the same state and agree per kind and operation: every
// create the plan lists is a create the apply writes, and no other.
func TestPlanAndApplyAgreePerKindAndOperation(t *testing.T) {
	realLookup(t)
	lookupConfig(t, `"work_items_query":"work-items-all","work_items_ceiling":7000`)
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
	lookupConfig(t, `"work_items_query":"work-items-all","work_items_ceiling":7000`)
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
	lookupConfig(t, `"work_items_query":"work-items-all","work_items_ceiling":300`)
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(lookupView))
	trackerDouble(t, listingOf(closedItem("ci-closed", "fix-ci: acme/widgets#42", keyFixCI)), 0)
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42")
	if code != exitcode.OK {
		t.Fatalf("code %d err %q", code, errOut)
	}
	line := countersLine(t, errOut)
	if line.LookupItems == nil || *line.LookupItems != 1 || line.LookupCeiling == nil || *line.LookupCeiling != 300 {
		t.Fatalf("figures = %v %v in %s", line.LookupItems, line.LookupCeiling, errOut)
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
// still reports the figures known (the ceiling always, the count when read).
func TestAnUntrustedLookupFailsTheRunClosedAndStillReportsTheFigures(t *testing.T) {
	three := listingOf(closedItem("a", "t", "pr:acme/widgets#42:anchor"), closedItem("b", "t", keyFixCI), closedItem("c", "t", keyReview))
	cases := []struct {
		name       string
		members    string
		listing    string
		listExit   int
		reason     string
		wantItems  int // -1 = not reported
		wantCeil   int // -1 = not reported
		wantInText string
	}{
		{"above the ceiling", `"work_items_query":"work-items-all","work_items_ceiling":2`, three, 0, "lookup-above-ceiling", 3, 2, "above the ceiling"},
		{
			"truncated", `"work_items_query":"work-items-all","work_items_ceiling":50`,
			`{"entities":[` + closedItem("a", "t", keyFixCI) + `],"sources":[{"source":"beads","status":"succeeded","count":1}],"truncated":true}`, 0, "lookup-truncated", 1, 50, "truncated",
		},
		{
			"unavailable (the call fails)", `"work_items_query":"work-items-all","work_items_ceiling":50`,
			`{"error":{"code":"unavailable","message":"down"}}`, 1, "lookup-unavailable", -1, 50, "unavailable",
		},
		{
			"unavailable (a source degraded)", `"work_items_query":"work-items-all","work_items_ceiling":50`,
			`{"entities":[],"sources":[{"source":"beads","status":"degraded","count":0,"reason":"cache"}],"truncated":false}`, 0, "lookup-unavailable", -1, 50, "degraded",
		},
		{"ceiling key absent", `"work_items_query":"work-items-all"`, three, 0, "lookup-ceiling-absent", -1, -1, "work_items_ceiling is not configured"},
		{"query key absent", `"work_items_ceiling":50`, three, 0, "lookup-query-absent", -1, 50, "work_items_query is not configured"},
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
				check("lookup_ceiling", line.LookupCeiling, tc.wantCeil)
			})
		}
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
