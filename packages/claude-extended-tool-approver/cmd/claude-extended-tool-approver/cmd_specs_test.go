package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/embeddedspecs"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
)

// writeRenamedCatSpec writes a valid command spec named name into dir, by
// renaming the embedded `cat` spec (a real, validating spec, not a mock).
func writeRenamedCatSpec(t *testing.T, dir, name string) {
	t.Helper()
	data, err := embeddedspecs.FS.ReadFile("data/cat.json")
	if err != nil {
		t.Fatalf("reading embedded cat spec: %v", err)
	}
	var s specfmt.Spec
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("decoding cat spec: %v", err)
	}
	s.Name = name
	s.Command.Name = name
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("encoding spec: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runSpecsListJSON(t *testing.T, userDir, repoDir string) map[string][]string {
	t.Helper()
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"specs", "list", "--user-dir", userDir, "--repo-dir", repoDir, "--format=json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("specs list: %v", err)
	}
	var entries []specListEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("decoding output %q: %v", buf.String(), err)
	}
	got := map[string][]string{}
	for _, e := range entries {
		got[e.Name] = e.Layers
	}
	return got
}

func TestSpecsList_BuiltInsListedAndUnknownAbsent(t *testing.T) {
	empty := t.TempDir()
	got := runSpecsListJSON(t, filepath.Join(empty, "no-user"), filepath.Join(empty, "no-repo"))

	if want := []string{"registry", "embedded"}; !reflect.DeepEqual(got["cat"], want) {
		t.Errorf("cat layers = %v, want %v", got["cat"], want)
	}
	if _, ok := got["definitely-not-a-command-xyz"]; ok {
		t.Error("an unknown command must not be listed")
	}
}

func TestSpecsList_UserAndRepoLayersAreListed(t *testing.T) {
	base := t.TempDir()
	userDir := filepath.Join(base, "user")
	repoDir := filepath.Join(base, "repo")
	writeRenamedCatSpec(t, userDir, "fixture-user-cmd")
	writeRenamedCatSpec(t, repoDir, "fixture-repo-cmd")

	got := runSpecsListJSON(t, userDir, repoDir)

	if want := []string{"user"}; !reflect.DeepEqual(got["fixture-user-cmd"], want) {
		t.Errorf("fixture-user-cmd layers = %v, want %v", got["fixture-user-cmd"], want)
	}
	if want := []string{"repo"}; !reflect.DeepEqual(got["fixture-repo-cmd"], want) {
		t.Errorf("fixture-repo-cmd layers = %v, want %v", got["fixture-repo-cmd"], want)
	}
}

func TestSpecsList_RejectsUnknownFormat(t *testing.T) {
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"specs", "list", "--format=yaml"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected an error for --format=yaml")
	}
}

// skillMissingSpecsProgram pulls the Step 7 jq program out of the
// identify-hook-misses skill, so the test exercises the text the skill ships
// rather than a copy. It skips when the skill file is not reachable (a build
// that sees only this Go module) or jq is not installed.
func skillMissingSpecsProgram(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	skill := filepath.Join("..", "..", "..", "..", "claude-marketplace", "claude-extended-tool-approver",
		"skills", "identify-hook-misses", "SKILL.md")
	data, err := os.ReadFile(skill)
	if err != nil {
		t.Skipf("skill file not reachable from this module: %v", err)
	}
	const open = "jq -s --slurpfile specs /tmp/ceta-spec-names.json '"
	const closing = "' /tmp/ceta-bash-rows.jsonl"
	text := string(data)
	start := strings.Index(text, open)
	if start < 0 {
		t.Fatalf("SKILL.md no longer contains the Step 7 query opener %q", open)
	}
	start += len(open)
	end := strings.Index(text[start:], closing)
	if end < 0 {
		t.Fatalf("SKILL.md no longer contains the Step 7 query closer %q", closing)
	}
	return text[start : start+end]
}

// TestSkillMissingSpecsQuery runs the skill's real query (real jq) over fixture
// rows. It proves commands inside pipelines, $(...), bash -c and bgrun
// wrappers are counted, specced names and Gas City gc are not, and that a
// command appearing twice in one row counts once.
func TestSkillMissingSpecsQuery(t *testing.T) {
	program := skillMissingSpecsProgram(t)
	dir := t.TempDir()

	specs := filepath.Join(dir, "specs.json")
	if err := os.WriteFile(specs, []byte(`["cat","echo","bash","bgrun","cd","jq"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	user := `"outcome":"approved","approval_source":"user","agent_type":null`
	machine := `"outcome":"approved","approval_source":"auto","agent_type":null`
	rows := strings.Join([]string{
		`{"id":1,"command_class":"uuidgen && tr A-Z a-z","hook_decision":"abstain",` + user + `}`,
		`{"id":2,"command_class":"echo $(uuidgen)","hook_decision":"deny",` + machine + `}`,
		`{"id":3,"command_class":"cd /x && bash -c wibble --flag","hook_decision":"abstain",` + machine + `}`,
		`{"id":4,"command_class":"bgrun JOB -- nice -n 10 wobble go","hook_decision":"abstain",` + user + `}`,
		`{"id":5,"command_class":"gc status && cat f && jq .a","hook_decision":"abstain",` + user + `}`,
		`{"id":6,"command_class":"FOO=1 /usr/bin/frob && frob again","hook_decision":"abstain",` + machine + `}`,
	}, "\n")
	rowsFile := filepath.Join(dir, "rows.jsonl")
	if err := os.WriteFile(rowsFile, []byte(rows), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("jq", "-sc", "--slurpfile", "specs", specs, program, rowsFile).Output()
	if err != nil {
		t.Fatalf("jq failed: %v\n%s", err, out)
	}
	var got []struct {
		Command    string `json:"command"`
		Calls      int    `json:"calls"`
		HumanTier  int    `json:"human_tier"`
		WeakerTier int    `json:"weaker_tier"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	byName := map[string]int{}
	for _, g := range got {
		byName[g.Command] = g.Calls
		if g.HumanTier+g.WeakerTier != g.Calls {
			t.Errorf("%s: human %d + weaker %d != calls %d", g.Command, g.HumanTier, g.WeakerTier, g.Calls)
		}
	}
	want := map[string]int{
		"uuidgen": 2, // leading word (row 1) and inside $(...) (row 2)
		"tr":      1, // later pipeline stage
		"wibble":  1, // inside bash -c
		"wobble":  1, // behind bgrun NAME -- and nice -n N
		"frob":    1, // env assignment and absolute path, listed twice in one row
	}
	if !reflect.DeepEqual(byName, want) {
		t.Errorf("missing-spec calls = %v, want %v (cat/echo/bash/bgrun/cd/jq have specs, gc is excluded)", byName, want)
	}
}
