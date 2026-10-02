package runlog

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

// readmeRecipes returns the jq recipes of the README's "Measuring from the run
// log": each fenced bash block that follows a `<!-- recipe: NAME -->` marker.
func readmeRecipes(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile("(?s)<!-- recipe: ([a-z-]+) -->\\s*```bash\n(.*?)\n```")
	out := map[string]string{}
	for _, m := range re.FindAllSubmatch(b, -1) {
		out[string(m[1])] = string(m[2])
	}
	return out
}

// TestReadmeJqRecipesRunAgainstAFixtureLog runs every recipe exactly as the
// README prints it, with XDG_STATE_HOME pointing at a copy of the fixture, and
// compares what jq prints with the numbers worked out by hand from
// testdata/runs.fixture.jsonl:
//
//	sync (8 runs): 3 success, 1 resolved by a deterministic handler, 1 resolved
//	  by an agent (cost 0.25), 1 deferred, 2 unhandled (one agent attempt cost
//	  0.125); so common 4/8, agent 1/8, escape 3/8, cost 0.375.
//	other (2 runs): 1 success, 1 unhandled.
//	(handlers) (1 run): resolved by an agent (cost 0.5).
func TestReadmeJqRecipesRunAgainstAFixtureLog(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not on PATH; the nix check provides it")
	}
	recipes := readmeRecipes(t)

	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "pg-rescue"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "runs.fixture.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "pg-rescue", "runs.jsonl"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"common-case-rate": `[{"chain":"(handlers)","runs":1,"common_case_rate":0},{"chain":"other","runs":2,"common_case_rate":0.5},{"chain":"sync","runs":8,"common_case_rate":0.5}]`,
		"agent-rate":       `[{"chain":"(handlers)","runs":1,"agent_rate":1},{"chain":"other","runs":2,"agent_rate":0},{"chain":"sync","runs":8,"agent_rate":0.125}]`,
		"agent-cost":       `[{"chain":"(handlers)","agent_cost_usd":0.5},{"chain":"other","agent_cost_usd":0},{"chain":"sync","agent_cost_usd":0.375}]`,
		"escape-rate":      `[{"chain":"(handlers)","runs":1,"escape_rate":0},{"chain":"other","runs":2,"escape_rate":0.5},{"chain":"sync","runs":8,"escape_rate":0.375}]`,
		"failing-handlers": `[{"handler":"ag","reason":"timed out after 8m","count":2},{"handler":"ag","reason":"exit 1","count":1}]`,
	}
	for name := range recipes {
		if _, ok := want[name]; !ok {
			t.Errorf("the README has a recipe %q this test has no expectation for", name)
		}
	}
	for name, expected := range want {
		t.Run(name, func(t *testing.T) {
			script, ok := recipes[name]
			if !ok {
				t.Fatalf("the README has no recipe %q", name)
			}
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("recipe failed: %v\n%s\n%s", err, stderr.String(), script)
			}
			var got, wantV any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("recipe output is not JSON: %v\n%s", err, out)
			}
			if err := json.Unmarshal([]byte(expected), &wantV); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, wantV) {
				t.Errorf("output:\n%s\nwant:\n%s", out, expected)
			}
		})
	}
}
