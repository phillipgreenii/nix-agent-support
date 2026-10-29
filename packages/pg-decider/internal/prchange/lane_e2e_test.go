//go:build laneE2E

package prchange

import (
	"fmt"
	"os/exec"
	"strconv"
	"testing"
)

// The real-tracker soak of the PR lane (docket pg2-6mo45.3, packet P4c-1; bead
// pg2-s4xeju put it behind this file's build tag). TestLaneE2E runs every lane
// scenario (lane_world_test.go describes them and the other run) over a REAL bd
// in a throwaway embedded store, one after another, plus the one scenario that
// tests bd's own query semantics and so means nothing against a fake.
//
// It is not a unit test and not a gate (operator standard V-4; operator ruling
// 2026-10-28, bead pg2-s4xeju: "a test must not run for 1.5 hours"): every bd
// call is a cold start of bd's embedded dolt engine, so the run takes tens of
// minutes and ran 84 on a loaded host. A plain `go test ./...` does not compile
// it. Run it by hand, when a change to bd, to the beads backend or to the lane's
// tracker calls warrants it:
//
//	bash scripts/run-pg-decider-lane-e2e.sh      (builds the binaries from source)
//	nix run .#pg-decider-lane-e2e-soak           (the same script, with its tools)
//
// bd and git are found on PATH. Without the binaries (see lane_world_test.go)
// the test skips unless PG_PR_CHANGE_REQUIRE_BINARIES is set, which the script
// sets.

// laneE2EScenariosFile lists, one name per line, every scenario TestLaneE2E
// runs. The test fails when its own scenarios and the file differ, and the
// script asserts a PASS line for each name in it.
const laneE2EScenariosFile = "testdata/lane-e2e-scenarios.txt"

// minLaneE2EScenarios is the number of scenarios the soak must run; keep it
// equal to MIN_SCENARIOS of the script.
const minLaneE2EScenarios = 25

// laneE2ETools is the lane's tools over the real bd (and the git its init needs).
func laneE2ETools(t *testing.T) e2eTools {
	t.Helper()
	tools := laneTools(t)
	var err error
	tools.bd, err = exec.LookPath("bd")
	e2eNeed(t, "bd (the real tracker)", err)
	tools.git, err = exec.LookPath("git")
	e2eNeed(t, "git (bd init needs it)", err)
	return tools
}

func TestLaneE2E(t *testing.T) {
	l := &laneRun{t: t, tools: laneE2ETools(t)}
	adoptionScenarios(l)
	unhydratedScenarios(l)
	failClosedScenarios(l)
	bdListQueryScenarios(l)
	driftScenarios(l)
	reviewScenarios(l)
	failureScenarios(l)
	rollbackScenarios(l)
	l.scenarioList(laneE2EScenariosFile, minLaneE2EScenarios)
}

// bdListQueryScenarios test the real bd's own answer to the query that lists a
// PR's review items, so they run only over a real bd.
func bdListQueryScenarios(l *laneRun) {
	l.run("the-bd-list-query-of-a-prs-review-items-returns-every-state", func(t *testing.T) {
		tr := newTracker(t, l.tools)
		md := func(repo string, n int, sha string) map[string]string {
			return map[string]string{"repo": repo, "pr_number": strconv.Itoa(n), "head_sha": sha}
		}
		title := func(repo string, n int, sha string) string {
			return fmt.Sprintf("review-pr: %s#%d @ %s", repo, n, sha[:7])
		}
		mine := map[string]string{}
		for _, st := range []string{"open", "in_progress", "blocked", "deferred", "closed"} {
			sha := fmt.Sprintf("%s0000000000", st[:2])
			id := tr.create(title("acme/api", 7, sha), "task", "", md("acme/api", 7, sha))
			switch st {
			case "closed":
				tr.run("close", id, "--reason", "reviewed")
			case "open":
			default:
				tr.run("update", id, "--status", st)
			}
			mine[id] = st
		}
		// Neighbors the query must leave out: another PR of the repo whose number
		// starts with 7, the same number in another repository, and the PR's own
		// anchor and feedback item (no review-pr: prefix).
		tr.create(title("acme/api", 70, "abcdef0"), "task", "", md("acme/api", 70, "abcdef0"))
		tr.create(title("other/api", 7, "abcdef1"), "task", "", md("other/api", 7, "abcdef1"))
		tr.create("acme/api#7: Add the export command", "merge-request", "", md("acme/api", 7, "abcdef2"))
		tr.create("process-feedback: acme/api#7", "task", "", md("acme/api", 7, "abcdef3"))

		query := func(repo string, n int) []map[string]any {
			return decodeItems(t, tr.run("list", "--all", "--limit", "0", "--metadata-field", "repo="+repo,
				"--metadata-field", "pr_number="+strconv.Itoa(n), "--title-contains", "review-pr:", "--sort", "created", "--json"))
		}
		got := query("acme/api", 7)
		seen := map[string]string{}
		for _, it := range got {
			seen[it["id"].(string)], _ = it["status"].(string)
		}
		if len(seen) != len(mine) {
			t.Fatalf("the query returned %d items, want the %d review items of the PR in every state: %v", len(seen), len(mine), seen)
		}
		for id, st := range mine {
			if seen[id] != st {
				t.Errorf("review item %s: returned in state %q, want %q", id, seen[id], st)
			}
		}
		if n := len(query("acme/api", 1)); n != 0 {
			t.Errorf("a PR with no review item got %d (the filters do not discriminate)", n)
		}
		// bd list's own 50-row default would hide older rows; the query names --limit 0.
		if len(tr.items()) != len(mine)+4 {
			t.Errorf("setup: the tracker holds %d beads", len(tr.items()))
		}
	})
}
