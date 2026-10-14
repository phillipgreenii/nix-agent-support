package serve_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The assembled daemon is tested as the REAL binary: built with go build, run as
// a child process on a short temp socket path with an explicit environment, its
// gh replaced by the counting fake (testdata/fake-gh.sh), its state in a temp
// directory (internal/servetest).

func TestMain(m *testing.M) {
	code := m.Run()
	servetest.RemoveBuilds()
	os.Exit(code)
}

// The fixture PRs: three, in two repositories, generic names only.
var (
	prA = servetest.PR{NodeID: "PR_node_a", Repo: "example-org/widget", Number: 3}
	prB = servetest.PR{NodeID: "PR_node_b", Repo: "example-org/gadget", Number: 7}
	prC = servetest.PR{NodeID: "PR_node_c", Repo: "example-org/widget", Number: 11}
)

// seed gives the shim the three fixture PRs as the answer of the mine query,
// prA and prB as the team query's.
func seed(d *servetest.Daemon) {
	d.Shim.SetPRs(servetest.MineExpr, prA, prB, prC)
	d.Shim.SetPRs(servetest.TeamExpr, prA, prB)
}

func listReq(query string, extra map[string]any) scriptout.Request {
	args := map[string]any{"query": query}
	for k, v := range extra {
		args[k] = v
	}
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return scriptout.Request{Op: scriptout.OpList, Args: raw}
}

// list asks the daemon for a configured query and decodes the result; the
// response is returned so a test can inspect its error.
func list(t *testing.T, d *servetest.Daemon, query string, deadline time.Duration, extra map[string]any) (scriptout.Response, schema.PRListResult) {
	t.Helper()
	resp := d.MustCall(t, "pr", listReq(query, extra), deadline)
	var res schema.PRListResult
	if resp.Error == nil {
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("decode the list result %s: %v", resp.Result, err)
		}
	}
	return resp, res
}

// mustList is list that requires a successful answer.
func mustList(t *testing.T, d *servetest.Daemon, query string, extra map[string]any) schema.PRListResult {
	t.Helper()
	resp, res := list(t, d, query, 20*time.Second, extra)
	if resp.Error != nil {
		t.Fatalf("list %s: %s: %s", query, resp.Error.Code, resp.Error.Message)
	}
	return res
}

// until polls cond every 25 ms until it holds or the timeout passes.
func until(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// firstSightingGroups are the entity groups the daemon fetches for every pull
// request it sees: the summary (from the query's members), then the detail, the
// conversation and the runs.
var firstSightingGroups = []string{"summary", "detail", "conversation", "runs"}

// awaitFirstSightingFetches waits until the daemon has stored every
// first-sighting group of prs pull requests. The silence of the call log
// (Shim.WaitQuiet) cannot say that: a host under load can leave a fetch in
// flight, or the next one unstarted, for longer than any quiet period, and a
// test that then stops the daemon, restarts it or flips the origin's mode acts
// on a daemon that is still filling. The store is the daemon's own account of
// what it has fetched.
func awaitFirstSightingFetches(t *testing.T, d *servetest.Daemon, prs int) {
	t.Helper()
	db := d.OpenStore(t)
	until(t, 60*time.Second, "every first-sighting group of the fixture pull requests to be stored", func() bool {
		var n int
		err := db.QueryRow(`SELECT COUNT(*) FROM entity_group WHERE kind = 'pr' AND fetched_at IS NOT NULL AND group_name IN (?, ?, ?, ?)`,
			firstSightingGroups[0], firstSightingGroups[1], firstSightingGroups[2], firstSightingGroups[3]).Scan(&n)
		return err == nil && n >= prs*len(firstSightingGroups)
	})
}
