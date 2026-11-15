package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// useBackfillFakeConnector answers `issue list --query Q --backend B` from
// <dir>/list-Q.json (exit code from list-Q.exit, default 0) and `issue show
// <id> --fresh --backend jira-a` from a held-<id> file (exit 4 otherwise). A
// file named down makes every call answer the wire error unavailable.
func useBackfillFakeConnector(t *testing.T, dir string) {
	t.Helper()
	log := &connectorCallLog{path: filepath.Join(dir, "calls.log")}
	useFakeConnector(t, func(args []string) (string, int) {
		log.record(t, args)
		if fileExists(filepath.Join(dir, "down")) {
			return daemonDownJSON, 1
		}
		if argAt(args, 1) == "list" {
			q := argAt(args, 3)
			code := 0
			if fileExists(filepath.Join(dir, "list-"+q+".exit")) {
				code, _ = strconv.Atoi(strings.TrimSpace(readFileOr(filepath.Join(dir, "list-"+q+".exit"), "0")))
			}
			if fileExists(filepath.Join(dir, "list-"+q+".json")) {
				return readFileOr(filepath.Join(dir, "list-"+q+".json"), ""), code
			}
			return noListFixture, 1
		}
		id := argAt(args, 2)
		if flagValue(args, "--backend") == "jira-a" && fileExists(filepath.Join(dir, "held-"+id)) {
			return `{"id":"` + id + `"}`, 0
		}
		return notFoundJSON, 4
	})
}

// runBackfillCmd runs `pg-desk issue backfill-backend` through the real cobra
// tree, in-process.
func runBackfillCmd(t *testing.T, extra ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetArgs(append([]string{"issue", "backfill-backend"}, extra...))
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
	err = rootCmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

// newBackfillFixture seeds PROJ-1 (a member of the owning watched query),
// PROJ-2 (named only in text, held by the daemon) and PROJ-3 (held by
// nobody), all with NULL backend, plus bd-5, a beads issue that already
// carries its backend and a watched mark.
func newBackfillFixture(t *testing.T) *backendFixture {
	t.Helper()
	f := newBackendFixture(t, "PROJ-1", "PROJ-2", "PROJ-3", "bd-5")
	useBackfillFakeConnector(t, f.dir) // shadows the fixture's own fake
	f.setBackend("bd-5", "beads-a")
	yes := true
	if err := f.seed.SetEntityColumns("o/r", "issue", "bd-5", nil, &yes); err != nil {
		t.Fatal(err)
	}
	f.write("held-PROJ-2", "")
	f.write("list-jira-q.json", `{"entities":[{"id":"PROJ-1","entity_meta":{"watched":true}},{"id":"PROJ-9","entity_meta":{"watched":true}}],"present_ids":["PROJ-1","PROJ-9"],"truncated":false}`)
	return f
}

type backfillSnapshot struct {
	version int64
	history int
}

func (f *backendFixture) snapshot(id string) backfillSnapshot {
	f.t.Helper()
	return backfillSnapshot{version: f.entity("issue", id).Version, history: len(f.history(id))}
}

func TestBackfillBackendSetsEveryHeldIDWithoutAChangeRow(t *testing.T) {
	f := newBackfillFixture(t)
	ids := []string{"PROJ-1", "PROJ-2", "PROJ-3", "bd-5"}
	before := map[string]backfillSnapshot{}
	for _, id := range ids {
		before[id] = f.snapshot(id)
	}

	out, _, err := runBackfillCmd(t)
	if err != nil {
		t.Fatalf("backfill-backend: %v", err)
	}
	for _, want := range []string{"query jira-q (jira-a): 1 held member(s) set", "issue PROJ-2: backend jira-a", "issue PROJ-3: backend beads-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q lacks %q", out, want)
		}
	}

	want := map[string]string{"PROJ-1": "jira-a", "PROJ-2": "jira-a", "PROJ-3": "beads-a", "bd-5": "beads-a"}
	for id, backend := range want {
		e := f.entity("issue", id)
		if e.Backend != backend {
			t.Errorf("%s backend = %q, want %q", id, e.Backend, backend)
		}
		if got := f.snapshot(id); got != before[id] {
			t.Errorf("%s version/history = %+v, want unchanged %+v", id, got, before[id])
		}
	}
	if w := f.entity("issue", "PROJ-1").Watched; w == nil || !*w {
		t.Errorf("PROJ-1 watched = %v, want true", w)
	}
	for _, id := range []string{"PROJ-2", "PROJ-3"} {
		if w := f.entity("issue", id).Watched; w != nil {
			t.Errorf("%s watched = %v, want untouched (probe-resolved ids never get a watched mark)", id, *w)
		}
	}
	if w := f.entity("issue", "bd-5").Watched; w == nil || !*w {
		t.Errorf("beads entity watched = %v, want unchanged true", w)
	}
	// PROJ-9 is listed but not held: it is not created.
	if _, found, err := f.seed.GetEntity("o/r", "issue", "PROJ-9"); err != nil || found {
		t.Errorf("PROJ-9 created: found=%v err=%v", found, err)
	}

	// A second run changes nothing.
	snap := map[string]store.Entity{}
	for _, id := range ids {
		snap[id] = f.entity("issue", id)
	}
	if _, _, err := runBackfillCmd(t); err != nil {
		t.Fatalf("second run: %v", err)
	}
	for _, id := range ids {
		a, b := snap[id], f.entity("issue", id)
		sameWatched := (a.Watched == nil) == (b.Watched == nil) && (a.Watched == nil || *a.Watched == *b.Watched)
		if a.Backend != b.Backend || a.Version != b.Version || !sameWatched {
			t.Errorf("%s changed on re-run: %+v -> %+v", id, a, b)
		}
	}
}

func TestBackfillBackendUnstatedWatchedLeavesTheColumnUntouched(t *testing.T) {
	f := newBackfillFixture(t)
	f.write("list-jira-q.json", `{"entities":[{"id":"PROJ-1"}],"present_ids":["PROJ-1"],"truncated":false}`)
	if _, _, err := runBackfillCmd(t); err != nil {
		t.Fatal(err)
	}
	e := f.entity("issue", "PROJ-1")
	if e.Backend != "jira-a" || e.Watched != nil {
		t.Errorf("PROJ-1 = backend %q watched %v, want jira-a and NULL", e.Backend, e.Watched)
	}
}

func TestBackfillBackendFailedListReadExitsOneNamingTheQueryAndRerunCompletes(t *testing.T) {
	f := newBackfillFixture(t)
	f.write("list-jira-q.exit", "1")
	f.write("list-jira-q.json", `{"error":{"code":"unavailable","message":"daemon down"}}`)
	f.write("held-PROJ-1", "")

	_, errOut, err := runBackfillCmd(t)
	if err == nil || exitCodeFor(err) != 1 {
		t.Fatalf("err = %v (exit %d), want exit 1", err, exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "query jira-q") || !strings.Contains(errOut+err.Error(), "unavailable") {
		t.Errorf("err %q, stderr %q: want the query named and the unavailable cause", err, errOut)
	}
	// The other held ids were still tried.
	if b := f.entity("issue", "PROJ-3").Backend; b != "beads-a" {
		t.Errorf("PROJ-3 backend = %q, want beads-a despite the failed list", b)
	}

	// The daemon recovers: a re-run completes with exit 0 and the member's mark.
	if err := os.Remove(filepath.Join(f.dir, "list-jira-q.exit")); err != nil {
		t.Fatal(err)
	}
	f.write("list-jira-q.json", `{"entities":[{"id":"PROJ-1","entity_meta":{"watched":true}}],"present_ids":["PROJ-1"],"truncated":false}`)
	if _, _, err := runBackfillCmd(t); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if e := f.entity("issue", "PROJ-1"); e.Backend != "jira-a" || e.Watched == nil || !*e.Watched {
		t.Errorf("PROJ-1 = %+v, want jira-a and watched", e)
	}
	if got := f.snapshot("PROJ-1"); got.history != 1 {
		t.Errorf("PROJ-1 history = %d, want only the seed row", got.history)
	}
}

func TestBackfillBackendTruncatedListIsAFailedRead(t *testing.T) {
	f := newBackfillFixture(t)
	f.write("list-jira-q.json", `{"entities":[{"id":"PROJ-1"}],"present_ids":["PROJ-1"],"truncated":true}`)
	f.write("held-PROJ-1", "")
	_, _, err := runBackfillCmd(t)
	if err == nil || exitCodeFor(err) != 1 || !strings.Contains(err.Error(), "query jira-q") || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("err = %v (exit %d), want exit 1 naming query jira-q as truncated", err, exitCodeFor(err))
	}
}

func TestBackfillBackendFailedProbeNamesTheIDAndLeavesItNull(t *testing.T) {
	f := newBackfillFixture(t)
	// The list answers, then the daemon goes away for the probes.
	f.write("held-PROJ-2", "")
	dir := f.dir
	// Make `issue show` fail by exiting 1 for PROJ-2 only.
	useFakeConnector(t, func(args []string) (string, int) {
		switch {
		case argAt(args, 1) == "list":
			return readFileOr(filepath.Join(dir, "list-jira-q.json"), ""), 0
		case argAt(args, 2) == "PROJ-2":
			return connectorUnavail, 1
		default:
			return notFoundJSON, 4
		}
	})

	out, errOut, err := runBackfillCmd(t, "--json")
	if err == nil || exitCodeFor(err) != 1 || !strings.Contains(err.Error(), "issue PROJ-2") {
		t.Fatalf("err = %v (exit %d), want exit 1 naming issue PROJ-2", err, exitCodeFor(err))
	}
	if !strings.Contains(errOut, "issue PROJ-2") {
		t.Errorf("stderr %q does not name PROJ-2", errOut)
	}
	var got struct {
		Updated int      `json:"updated"`
		Failed  []string `json:"failed"`
	}
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("stdout %q is not the JSON object: %v", out, jerr)
	}
	// PROJ-1 (list member) and PROJ-3 (probe, held by none) were written.
	if got.Updated != 2 || len(got.Failed) != 1 || !strings.Contains(got.Failed[0], "PROJ-2") {
		t.Errorf("json = %+v, want updated 2 and one failure naming PROJ-2", got)
	}
	if b := f.entity("issue", "PROJ-2").Backend; b != "" {
		t.Errorf("PROJ-2 backend = %q, want NULL after a failed probe", b)
	}
}

func TestBackfillBackendJSONOutputOnSuccess(t *testing.T) {
	newBackfillFixture(t)
	out, _, err := runBackfillCmd(t, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != `{"updated":3,"failed":[]}` {
		t.Errorf("stdout = %q, want updated 3 and an empty failed list", out)
	}
}

func TestBackfillBackendIsRegisteredUnderIssueOnly(t *testing.T) {
	if c, _, err := rootCmd.Find([]string{"issue", "backfill-backend"}); err != nil || c.Name() != "backfill-backend" {
		t.Errorf("issue backfill-backend not registered: %v, %v", c, err)
	}
	for _, typ := range []string{"pr", "thread"} {
		if c, _, _ := rootCmd.Find([]string{typ, "backfill-backend"}); c != nil && c.Name() == "backfill-backend" {
			t.Errorf("%s has a backfill-backend", typ)
		}
	}
}
