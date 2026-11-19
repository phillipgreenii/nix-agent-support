//go:build integration

package executor

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunbookRecipes runs the jq recipes documented in
// docs/runbooks/dispatched-session-failure-signatures.md against the fixture.
func TestRunbookRecipes(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH")
	}
	fixture := filepath.Join("testdata", "dispatch-results.jsonl")
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), jq, append(args, fixture)...).CombinedOutput()
		if err != nil {
			t.Fatalf("jq %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	first := run("-s", `map(select(.kind=="dispatch_result" and .failure_signature=="git-auth"))
       | min_by(.time) | {time, role, pool, bead}`)
	if !strings.Contains(first, "2026-09-23T14:01:00") || !strings.Contains(first, `"zr-1"`) {
		t.Errorf("first-occurrence recipe = %s", first)
	}
	perDay := run("-r", `select(.kind=="dispatch_result") | "\(.time[0:10]) \(.failure_signature)"`)
	if strings.Count(perDay, "2026-09-23 git-auth") != 2 || strings.Count(perDay, "\n") != 4 {
		t.Errorf("per-day recipe = %q (hard_stop/needs_input rows must be excluded)", perDay)
	}
	byRole := run("-r", `select(.kind=="dispatch_result") | "\(.failure_signature) \(.role) \(.pool)"`)
	if !strings.Contains(byRole, "git-auth review default") || !strings.Contains(byRole, "budget worker p2") {
		t.Errorf("per-role/pool recipe = %q", byRole)
	}
	// The unclaimed_end rows (pg2-e0imq) are not dispatch results: the recipes above
	// still ignore them, and the exempt-ends recipe lists only the limit and outage ones.
	if strings.Contains(byRole, "api-rate-limit") {
		t.Errorf("the per-role recipe must not count unclaimed_end rows: %q", byRole)
	}
	exempt := run("-r", `select(.kind=="unclaimed_end" and (.failure_signature=="api-rate-limit"
         or .failure_signature=="api-transient"))
         | "\(.time) \(.role) \(.bead) \(.failure_signature)"`)
	if strings.TrimSpace(exempt) != "2026-10-14T08:02:40.000000000Z drain zr-6 api-rate-limit" {
		t.Errorf("exempt-ends recipe = %q", exempt)
	}
}
