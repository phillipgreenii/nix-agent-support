package backfill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// connectorDouble answers the two listings Enumerate asks pg-connector for.
type connectorDouble struct {
	issues string
	prs    map[string]string // query -> answer
	calls  []string
	env    [][]string
}

func (d *connectorDouble) run(_ context.Context, c Call) Result {
	d.calls = append(d.calls, strings.Join(c.Args, " "))
	d.env = append(d.env, c.Env)
	switch {
	case len(c.Args) >= 2 && c.Args[0] == "issue" && c.Args[1] == "list":
		return Result{Stdout: d.issues}
	case len(c.Args) >= 2 && c.Args[0] == "pr" && c.Args[1] == "list":
		for i, a := range c.Args {
			if a == "--query" {
				if ans, ok := d.prs[c.Args[i+1]]; ok {
					return Result{Stdout: ans}
				}
			}
		}
	}
	return Result{Code: 2, Stderr: "no answer for " + strings.Join(c.Args, " ")}
}

const okSources = `"sources":[{"source":"s","status":"succeeded","count":1}],"truncated":false`

func queriesFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "queries")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func enumOpts(t *testing.T, d *connectorDouble) EnumerateOptions {
	return EnumerateOptions{QueriesFile: queriesFile(t, "# the deployment's PR queries\nmine\nteam\n"), AnchorsQuery: "work-beads", Backend: "beads", BeadsDir: "/tmp/tracker", Runner: d.run}
}

func TestEnumerateListsAnchorsAndQueryPRsIncludingAnAnchorWhosePRIsMergedOrAbsent(t *testing.T) {
	d := &connectorDouble{
		issues: `{"entities":[
		  {"id":"bd-1","title":"acme/api#10: open one","state":"open","issue_type":"merge-request","metadata":{"state":"open"}},
		  {"id":"bd-2","title":"acme/api#11: merged one","state":"in_progress","issue_type":"merge-request","metadata":{"state":"merged"}},
		  {"id":"bd-3","title":"acme/api#12: gone","state":"open","issue_type":"merge-request"},
		  {"id":"bd-4","title":"review-pr: acme/api#10","state":"open","issue_type":"task"},
		  {"id":"bd-5","title":"acme/api#13: closed anchor","state":"closed","issue_type":"merge-request"},
		  {"id":"bd-6","title":"a merge request with a strange title","state":"open","issue_type":"merge-request"}
		],` + okSources + `}`,
		prs: map[string]string{
			"mine": `{"present_ids":["acme/api#10","acme/api#20"],` + okSources + `}`,
			"team": `{"present_ids":["acme/api#20","acme/api#21","not a pr id"],` + okSources + `}`,
		},
	}
	s, err := Enumerate(context.Background(), enumOpts(t, d))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range s.Entries {
		got[e.PR] = e
	}
	if len(got) != 5 {
		t.Fatalf("entries %v", s.IDs())
	}
	// The anchor whose PR is merged is reached only through the tracker.
	if e := got["acme/api#11"]; !e.Has(FromAnchor) || e.Has(FromQuery) || e.State != StateMerged {
		t.Errorf("merged-PR anchor: %+v", e)
	}
	// An anchor with no recorded PR state is listed with the state unknown.
	if e := got["acme/api#12"]; !e.Has(FromAnchor) || e.State != StateUnknown {
		t.Errorf("absent-PR anchor: %+v", e)
	}
	// A PR in both a query and an anchor is both, and open.
	if e := got["acme/api#10"]; !e.Has(FromAnchor) || !e.Has(FromQuery) || e.State != StateOpen || len(e.AnchorIDs) != 1 {
		t.Errorf("open PR in both: %+v", e)
	}
	if e := got["acme/api#20"]; e.Has(FromAnchor) || len(e.Queries) != 2 {
		t.Errorf("query-only PR: %+v", e)
	}
	// A closed anchor is not an open anchor, a child is not an anchor.
	if _, ok := got["acme/api#13"]; ok {
		t.Error("a closed anchor was enumerated")
	}
	if s.OpenAnchorCount != 4 || s.OpenAnchorPRCount != 3 || s.QueryPRCount != 3 {
		t.Errorf("counts: open anchors %d, anchor PRs %d, query PRs %d", s.OpenAnchorCount, s.OpenAnchorPRCount, s.QueryPRCount)
	}
	// What maps to no PR is reported, not dropped.
	if len(s.Unparseable) != 2 {
		t.Fatalf("unparseable %+v", s.Unparseable)
	}
	froms := s.Unparseable[0].From + "," + s.Unparseable[1].From
	if !strings.Contains(froms, FromAnchor) || !strings.Contains(froms, FromQuery) {
		t.Errorf("unparseable %+v", s.Unparseable)
	}
	// The tracker listing is pinned to the configured backend and directory.
	if !strings.Contains(d.calls[0], "issue list --query work-beads --backend beads") || len(d.env[0]) == 0 || d.env[0][len(d.env[0])-1] != "PG_CONNECTOR_ISSUE_BEADS_DIR=/tmp/tracker" {
		t.Errorf("tracker call %q env %v", d.calls[0], d.env[0])
	}
	if !strings.Contains(d.calls[1], "pr list --query mine --ids-only") {
		t.Errorf("pr call %q", d.calls[1])
	}
}

func TestEnumerateFailsClosedOnAnAbsentOrEmptyQueryNameFile(t *testing.T) {
	d := &connectorDouble{issues: `{"entities":[{"id":"bd-1","title":"acme/api#10: x","state":"open","issue_type":"merge-request"}],` + okSources + `}`}
	for name, path := range map[string]string{
		"absent":         filepath.Join(t.TempDir(), "missing"),
		"empty":          queriesFile(t, ""),
		"only comments":  queriesFile(t, "# nothing\n\n  \n"),
		"no path at all": "",
	} {
		o := enumOpts(t, d)
		o.QueriesFile = path
		if _, err := Enumerate(context.Background(), o); err == nil || !strings.Contains(err.Error(), "query") {
			t.Errorf("%s: err %v", name, err)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("an enumeration with no queries asked the connector: %v", d.calls)
	}
}

func TestEnumerateAssertsANonZeroCount(t *testing.T) {
	d := &connectorDouble{
		issues: `{"entities":[],` + okSources + `}`,
		prs:    map[string]string{"mine": `{"present_ids":[],` + okSources + `}`, "team": `{"present_ids":[],` + okSources + `}`},
	}
	if _, err := Enumerate(context.Background(), enumOpts(t, d)); !errors.Is(err, ErrEmptySet) {
		t.Fatalf("err %v", err)
	}
}

func TestEnumerateRefusesAnIncompleteListing(t *testing.T) {
	good := `{"present_ids":["acme/api#20"],` + okSources + `}`
	issuesOK := `{"entities":[],` + okSources + `}`
	for name, d := range map[string]*connectorDouble{
		"a degraded issue source":       {issues: `{"entities":[],"sources":[{"source":"s","status":"degraded","reason":"bd down"}],"truncated":false}`, prs: map[string]string{"mine": good, "team": good}},
		"a truncated pr listing":        {issues: issuesOK, prs: map[string]string{"mine": `{"present_ids":["acme/api#20"],"sources":[{"source":"s","status":"succeeded"}],"truncated":true}`, "team": good}},
		"a degraded pr source":          {issues: issuesOK, prs: map[string]string{"mine": good, "team": `{"present_ids":[],"sources":[{"source":"s","status":"degraded","reason":"rate limited"}],"truncated":false}`}},
		"no source answered":            {issues: `{"entities":[],"sources":[],"truncated":false}`, prs: map[string]string{"mine": good, "team": good}},
		"an answer that is no JSON":     {issues: `oops`, prs: map[string]string{"mine": good, "team": good}},
		"a query the connector refuses": {issues: issuesOK, prs: map[string]string{"mine": good}},
	} {
		if _, err := Enumerate(context.Background(), enumOpts(t, d)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestTheSetRoundTripsAndAnEmptyOrForeignOneIsRefused(t *testing.T) {
	s := testSet("a/b#1", "a/b#2")
	p := filepath.Join(t.TempDir(), "sub", "set.json")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSet(p)
	if err != nil || len(got.Entries) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	empty := &Set{Contract: SetContract}
	if err := empty.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSet(p); err == nil || !strings.Contains(err.Error(), "lists no PR") {
		t.Errorf("an empty set: %v", err)
	}
	if err := os.WriteFile(p, []byte(`{"contract":"other","entries":[{"pr":"a/b#1"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSet(p); err == nil || !strings.Contains(err.Error(), "contract") {
		t.Errorf("a foreign contract: %v", err)
	}
	if _, err := LoadSet(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing set")
	}
}

func TestAnchorTitleMapsToItsPR(t *testing.T) {
	for title, want := range map[string]string{"acme/api#10: Add it": "acme/api#10", "a.b/c-d#1: x": "a.b/c-d#1"} {
		if got, ok := PRFromAnchorTitle(title); !ok || got != want {
			t.Errorf("%q -> %q %v", title, got, ok)
		}
	}
	for _, title := range []string{"review-pr: acme/api#10", "acme/api#10", "#10: x", "no hash", "acme/api#x: y"} {
		if _, ok := PRFromAnchorTitle(title); ok {
			t.Errorf("%q mapped to a PR", title)
		}
	}
}
