package interpret

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

func TestComputeCIRollupHeadSHA(t *testing.T) {
	type run = map[string]any
	mk := func(name, conclusion, sha, id string, attempt int) run {
		r := run{"name": name, "status": "completed", "conclusion": conclusion, "id": id, "attempt": attempt}
		if sha != "" {
			r["head_sha"] = sha
		}
		return r
	}
	cases := []struct {
		name    string
		runs    []run
		head    string
		interps []config.CheckInterpreterConfig
		want    string
	}{
		{"old-SHA cancelled runs ignored", []run{
			mk("A", "success", "new", "10", 1), mk("B", "skipped", "new", "11", 1),
			mk("A", "cancelled", "old", "5", 1), mk("B", "cancelled", "old", "6", 1),
		}, "new", nil, "success"},
		{"same-name duplicate newest success", []run{
			mk("PR Checks", "cancelled", "new", "20", 1), mk("PR Checks", "success", "new", "21", 1),
		}, "new", nil, "success"},
		{"higher attempt beats higher id", []run{
			mk("X", "success", "new", "5", 2), mk("X", "failure", "new", "9", 1),
		}, "new", nil, "success"},
		{"newest cancelled still fails", []run{
			mk("PR Checks", "success", "new", "20", 1), mk("PR Checks", "cancelled", "new", "21", 1),
		}, "new", nil, "failure"},
		{"unknown head SHA falls back to all runs", []run{
			mk("A", "cancelled", "old", "5", 1), mk("A", "success", "new", "10", 1),
		}, "", nil, "failure"},
		{"run without head_sha is kept", []run{
			mk("A", "failure", "", "5", 0), mk("B", "success", "new", "6", 1),
		}, "new", nil, "failure"},
		{"numeric id ordering 9 vs 10", []run{
			mk("X", "failure", "new", "9", 1), mk("X", "success", "new", "10", 1),
		}, "new", nil, "success"},
		{"excluder still applied", []run{
			mk("policy-bot: x", "failure", "new", "1", 1), mk("A", "success", "new", "2", 1),
		}, "new", []config.CheckInterpreterConfig{{Patterns: []string{"^policy-bot"}}}, "success"},
		{"pending newest on head", []run{
			{"name": "A", "status": "in_progress", "id": "3", "attempt": 1, "head_sha": "new"},
			mk("A", "success", "new", "2", 1),
		}, "new", nil, "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"runs": tc.runs})
			got := computeCIRollup(raw, tc.interps, tc.head)
			if got.State != tc.want {
				t.Fatalf("got %q, want %q", got.State, tc.want)
			}
		})
	}
}
