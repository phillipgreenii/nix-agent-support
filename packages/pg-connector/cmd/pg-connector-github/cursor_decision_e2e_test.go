package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
)

// The cursor decision of the PR lane cutover, against the real stack: the new
// unscoped consumer is reset with a replay AFTER the marking step, and the
// replay delivers every watched PR as `added`, so the decision loses no row.
// The same real daemon child, client binary, umbrella and adapter as
// adapter_e2e_test.go, each a separate process; nothing is faked but the origin
// (the counting fake gh).
//
// The old backend's cursor is a position in its own ledger and cannot carry
// into the daemon's sequence numbers, so the cutover creates a fresh consumer
// and replays (docs/runbooks/pg-connector-github-cutover.md in the repository
// root, "The cursor decision"). Two properties make that safe, each shown here
// by a plain negative case:
//
//   - the replay lists only WATCHED PRs, so a PR that was never marked is
//     absent from it (the replay BEFORE the marking step lacks the directly
//     marked PR; the replay AFTER it carries it);
//   - an unscoped consumer's first poll before every watched query has its
//     baseline answers empty and creates no cursor, so a poll that races the
//     first complete run neither starts at a premature tail nor skips rows.

const cursorDecisionConsumer = "pr-changes"

// cursorDecisionPRW is a PR no configured query lists: the open-anchor PRs the
// marking step reaches that no query would otherwise watch.
var cursorDecisionPRW = servetest.PR{NodeID: "PR_cd_w", Repo: "example-org/widget", Number: 21}

// cursorDecisionRig is the environment of the umbrella, the client and the
// adapter, wired to one daemon as a deployment wires them.
type cursorDecisionRig struct {
	rig      adapterE2ERig
	umbrella string
	adapter  string
}

func newCursorDecisionRig(t *testing.T, d *servetest.Daemon) cursorDecisionRig {
	t.Helper()
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	adapterDir := adapterBinary(t, moduleRoot)
	umbrella := adapterE2EBuild(t, moduleRoot, "./cmd/pg-connector", "pg-connector")
	client := servetest.Binary(t, "", "")
	home := t.TempDir()
	registry := filepath.Join(home, "registry.yaml")
	manifest := fmt.Sprintf("connector:\n  pr:\n    - name: %s\n      command:\n        - %s\n        - --config\n        - %s\n        - pr\n",
		adapterE2EBackend, filepath.Base(client), d.ConfigPath)
	if err := os.WriteFile(registry, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return cursorDecisionRig{
		rig: adapterE2ERig{env: []string{
			"PATH=" + filepath.Dir(umbrella) + string(os.PathListSeparator) + filepath.Dir(client) + string(os.PathListSeparator) + adapterDir + string(os.PathListSeparator) + servetest.DaemonPath(),
			"HOME=" + home,
			"XDG_STATE_HOME=" + filepath.Join(home, "state"),
			"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
			"PG_PR_CONFIG=" + registry,
		}},
		umbrella: umbrella,
		adapter:  filepath.Join(adapterDir, adapterBinaryName),
	}
}

// poll is one poll of the new source: no query argument, so the consumer is
// unscoped, as the registry's pr-changes source runs.
func (r cursorDecisionRig) poll(t *testing.T) adapterRun {
	t.Helper()
	return r.rig.run(t, r.adapter, "changes", "pr", "--consumer", cursorDecisionConsumer)
}

// mark is one marking read: `pr show <ref> --watch`.
func (r cursorDecisionRig) mark(t *testing.T, ref string) {
	t.Helper()
	res := r.rig.run(t, r.umbrella, "pr", "show", ref, "--watch", "--output", "json")
	if res.exit != 0 {
		t.Fatalf("pr show %s --watch: exit=%d stdout=%q stderr=%q", ref, res.exit, res.stdout, res.stderr)
	}
}

// reset is the umbrella's reset of the consumer; it returns the replay as
// "<change> <ref>" strings, sorted.
func (r cursorDecisionRig) reset(t *testing.T) []string {
	t.Helper()
	res := r.rig.run(t, r.umbrella, "pr", "changes", "--consumer", cursorDecisionConsumer, "--reset", "--output", "json")
	if res.exit != 0 {
		t.Fatalf("the umbrella --reset: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	var wire struct {
		Changes []struct {
			Change string `json:"change"`
			Entity struct {
				ID string `json:"id"`
			} `json:"entity"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &wire); err != nil {
		t.Fatalf("decode the reset wire: %v (stdout=%s)", err, res.stdout)
	}
	got := []string{}
	for _, c := range wire.Changes {
		got = append(got, c.Change+" "+c.Entity.ID)
	}
	slices.Sort(got)
	return got
}

func (r cursorDecisionRig) wantNoItems(t *testing.T, what string) {
	t.Helper()
	res := r.poll(t)
	if res.exit != 0 || strings.TrimSpace(res.stdout) != "[]" || res.stderr != "" {
		t.Fatalf("%s: exit=%d stdout=%q stderr=%q, want exit 0, no items and no stderr", what, res.exit, res.stdout, res.stderr)
	}
}

func consumerRows(t *testing.T, d *servetest.Daemon, consumer string) int {
	t.Helper()
	var n int
	if err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM consumer WHERE consumer = ?`, consumer).Scan(&n); err != nil {
		t.Fatalf("count consumer rows: %v", err)
	}
	return n
}

func TestCursorDecision_MarkingThenReplayDeliversEveryWatchedPRAsAdded(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{
		Config: map[string]any{"pr": confWatchedQueries()},
		Before: func(d *servetest.Daemon) {
			d.Shim.SetPRs(servetest.MineExpr, confPRA, confPRB)
			d.Shim.SetPRs(servetest.TeamExpr, confPRA)
			// The daemon can answer for W but no configured query lists it, so only
			// the marking read ever shows it to the daemon (it resolves a reference
			// it has never seen with the identity query).
			d.Shim.SetPRs("author:nobody", cursorDecisionPRW)
			d.Shim.Set("resolve.example_org__widget__21.json", `{"nameWithOwner":"example-org/widget","pullRequest":{"id":"`+cursorDecisionPRW.NodeID+`","number":21}}`)
		},
	})
	confAwaitBaseline(t, d, servetest.QueryMine, servetest.QueryTeam)
	rig := newCursorDecisionRig(t, d)

	// The new consumer's first poll registers it at the tail: it answers nothing
	// and sees none of the history. This is why the cutover replays at all.
	rig.wantNoItems(t, "the first poll")

	// Negative case: a replay BEFORE the marking step delivers the PRs the
	// queries watch and lacks the PR only the marking step reaches. A cutover
	// that reset first would lose W.
	members := []string{"added " + confPRA.Ref(), "added " + confPRB.Ref()}
	slices.Sort(members)
	if got := rig.reset(t); !slices.Equal(got, members) {
		t.Fatalf("the replay before the marking step = %v, want only the watched query members %v", got, members)
	}

	// The marking step, then the replay: every watched PR is delivered as
	// `added`, the directly marked one included. A PR a query already watches is
	// marked again harmlessly.
	rig.mark(t, cursorDecisionPRW.Ref())
	rig.mark(t, confPRA.Ref())
	want := append([]string{"added " + cursorDecisionPRW.Ref()}, members...)
	slices.Sort(want)
	got := rig.reset(t)
	if !slices.Equal(got, want) {
		t.Fatalf("the replay after the marking step = %v, want every watched PR as added %v", got, want)
	}

	// The replay is complete and not repeated: the consumer is at the tail, the
	// next poll answers nothing and the replay of an unchanged view writes
	// nothing new.
	rig.wantNoItems(t, "the poll after the replay")
	if again := rig.reset(t); !slices.Equal(again, want) {
		t.Fatalf("a second replay of an unchanged view = %v, want the same set %v", again, want)
	}
	rig.wantNoItems(t, "the poll after the second replay")
}

func TestCursorDecision_AFirstUnscopedPollBeforeEveryBaselineAnswersEmptyAndCreatesNoCursor(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{
		Config: map[string]any{"pr": confWatchedQueries()},
		Before: func(d *servetest.Daemon) {
			d.Shim.SetPRs(servetest.MineExpr, confPRA, confPRB)
			d.Shim.SetPRs(servetest.TeamExpr, confPRA)
			// Every query run fails, so no watched query can complete its first run
			// and none has a baseline: the daemon is up and the feed is not ready.
			d.Shim.Fail("search", 502, 0)
		},
	})
	rig := newCursorDecisionRig(t, d)

	var baselines int
	if err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM query WHERE baseline_at IS NOT NULL`).Scan(&baselines); err != nil || baselines != 0 {
		t.Fatalf("control: %d queries have a baseline (err %v), want none before the origin answers", baselines, err)
	}

	// The poll answers empty, and it creates nothing: a cursor created now would
	// start at a tail taken before the baselines and lose the rows between.
	res := rig.poll(t)
	if strings.TrimSpace(res.stdout) != "[]" && res.stdout != "" {
		t.Fatalf("a first poll before the baselines: exit=%d stdout=%q stderr=%q, want no items", res.exit, res.stdout, res.stderr)
	}
	if n := consumerRows(t, d, cursorDecisionConsumer); n != 0 {
		t.Fatalf("a first poll before the baselines created %d consumer rows, want none", n)
	}

	// The origin recovers, the baselines appear and the next poll creates the
	// cursor at the tail: it answers nothing and the consumer now exists.
	d.Shim.Remove("fail.search")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if n := consumerRows(t, d, cursorDecisionConsumer); n == 1 {
			break
		}
		res = rig.poll(t)
		if time.Now().After(deadline) {
			t.Fatalf("the cursor was never created after the baselines: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	rig.wantNoItems(t, "the poll after the cursor exists")
}
