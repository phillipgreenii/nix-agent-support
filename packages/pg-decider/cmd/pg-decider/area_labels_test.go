package main

// plan and apply honour the area_labels configuration key (bead pg2-fsrzf):
// a configured vocabulary labels the anchor and its children on create, and an
// unconfigured run is untouched (the golden plans cover that).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
)

func newEverythingView() string {
	return filepath.Join("..", "..", "testdata", "views", "pr_new_everything.json")
}

func writeAreaConfig(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "decider.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_DECIDER_CONFIG", p)
}

const areaConfigBody = `{"area_labels":[
	{"pattern":"^Add retry","labels":["retry-area","client"]},
	{"pattern":"^feature/","field":"branch","labels":["feature-branch"]}]}`

func TestPlanShowsConfiguredAreaLabelsOnTheCreates(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+newEverythingView())
	writeAreaConfig(t, areaConfigBody)
	out, errOut, code := runCLI(t, "plan", "pr", "acme/widgets#42", "--json")
	if code != exitcode.OK || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var plan action.PlanResult
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range plan.Actions {
		if a.Op != action.OpCreate || (a.Kind != "anchor" && a.Kind != "review-pr" && a.Kind != "process-feedback") {
			continue
		}
		seen[a.Kind] = true
		got := strings.Join(a.Fields.Labels, ",")
		for _, l := range []string{"client", "feature-branch", "retry-area"} {
			if !strings.Contains(got, l) {
				t.Errorf("%s create lacks %q: %s", a.Kind, l, got)
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("expected anchor, review-pr and process-feedback creates, saw %v", seen)
	}
}

func TestPlanWithoutAreaLabelsConfigAddsNone(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+newEverythingView())
	t.Setenv("PG_DECIDER_CONFIG", "")
	out, _, code := runCLI(t, "plan", "pr", "acme/widgets#42")
	if code != exitcode.OK || strings.Contains(out, "retry-area") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestPlanWithAnInvalidAreaLabelsConfigExits1(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+newEverythingView())
	writeAreaConfig(t, `{"area_labels":[{"pattern":"(","labels":["a"]}]}`)
	out, errOut, code := runCLI(t, "plan", "pr", "acme/widgets#42")
	if code != exitcode.Failure || out != "" || !strings.Contains(errOut, "area_labels[0]") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestApplyWritesConfiguredAreaLabelsOnTheAnchorCreate(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+newEverythingView())
	writeAreaConfig(t, areaConfigBody)
	recs := recordWrites(t, []action.Action{{
		Op: action.OpCreate, Kind: "anchor", Rule: "anchor.lazy",
		Fields: action.Fields{Title: "acme/widgets#42: Add retry to client", IssueType: "merge-request", Labels: []string{"co-owned"}},
	}}, `{"result":{"id":"wb-9"}}`, "0")
	if _, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42"); code != exitcode.OK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var create string
	for _, r := range withoutComments(*recs) {
		if strings.HasPrefix(r.args, "issue create") {
			create = r.args
		}
	}
	if !strings.Contains(create, "--labels client,co-owned,feature-branch,retry-area") {
		t.Fatalf("create exec = %q", create)
	}
}
