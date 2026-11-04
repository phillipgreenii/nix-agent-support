package shapereplay

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func fixtureTree(t *testing.T) (root, proj, home string) {
	t.Helper()
	root = t.TempDir()
	proj = filepath.Join(root, "proj")
	home = filepath.Join(root, "home")
	for _, d := range []string{proj, home, filepath.Join(root, "outside")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	return root, proj, home
}

func TestReplayStateIndependentVerdictConsultsNothing(t *testing.T) {
	_, proj, _ := fixtureTree(t)
	res := New().Replay(Input{ToolName: "Bash", ToolInputJSON: `{"command":"echo hi"}`, CWD: proj})
	if res.Decision != "approve" || len(res.Consulted) != 0 || res.Consulted == nil {
		t.Fatalf("got %+v, want approve with empty non-nil Consulted", res)
	}
}

func TestReplayFilesystemSymlinkFlipsAndIsTagged(t *testing.T) {
	root, proj, _ := fixtureTree(t)
	file := `{"file_path":"` + proj + `/link/a.txt","content":"x"}`
	in := Input{ToolName: "Write", ToolInputJSON: file, CWD: proj}

	// No symlink: inside the project, approve, not state-dependent.
	if res := New().Replay(in); res.Decision != "approve" || len(res.Consulted) != 0 {
		t.Fatalf("without the link: %+v", res)
	}
	// The link escapes the project: the live verdict is not approve and
	// depends on the filesystem.
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(proj, "link")); err != nil {
		t.Fatal(err)
	}
	res := New().Replay(in)
	if res.Decision == "approve" || !has(res.Consulted, StateFilesystem) {
		t.Fatalf("with the link: %+v, want non-approve consulting filesystem", res)
	}
}

func TestReplayEnvDependentVerdictIsTagged(t *testing.T) {
	_, proj, _ := fixtureTree(t)
	in := Input{ToolName: "Write", ToolInputJSON: `{"file_path":"` + proj + `/denied/a.txt","content":"x"}`, CWD: proj}
	if res := New().Replay(in); res.Decision != "approve" {
		t.Fatalf("baseline: %+v", res)
	}
	t.Setenv("CETA_DENIED_ROOTS", proj+"/denied")
	res := New().Replay(in)
	if res.Decision != "reject" || !has(res.Consulted, StateEnv) {
		t.Fatalf("got %+v, want reject consulting env", res)
	}
	if os.Getenv("CETA_DENIED_ROOTS") != proj+"/denied" {
		t.Fatal("the env twin did not restore the environment")
	}
}

func TestReplayConfigDependentVerdictIsTagged(t *testing.T) {
	_, proj, home := fixtureTree(t)
	dir := filepath.Join(home, ".config", "claude-extended-tool-approver-engine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"agentWritableConfig":["~/.config/claude-extended-tool-approver-engine/notes.json"]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	in := Input{ToolName: "Write", ToolInputJSON: `{"file_path":"` + dir + `/notes.json","content":"x"}`, CWD: proj}
	res := New().Replay(in)
	if res.Decision != "approve" || !reflect.DeepEqual(res.Consulted, []string{StateConfig}) {
		t.Fatalf("got %+v, want approve consulting exactly config", res)
	}
	if os.Getenv("HOME") != home {
		t.Fatal("the config twin did not restore HOME")
	}
}

func TestReplayReadGlobGrepGoThroughTheAdapter(t *testing.T) {
	_, proj, _ := fixtureTree(t)
	r := New()
	for tool, input := range map[string]string{
		"Read": `{"file_path":"` + proj + `/a.txt"}`,
		"Glob": `{"pattern":"*.go"}`,
		"Grep": `{"pattern":"x"}`,
	} {
		res := r.Replay(Input{ToolName: tool, ToolInputJSON: input, CWD: proj})
		if res.Unmapped || res.Decision != "approve" {
			t.Errorf("%s: %+v, want a mapped approve", tool, res)
		}
	}
}

func TestReplayUnmappedToolAbstains(t *testing.T) {
	_, proj, _ := fixtureTree(t)
	res := New().Replay(Input{ToolName: "Skill", ToolInputJSON: `{"skill":"x"}`, CWD: proj})
	if res.Decision != "abstain" || !res.Unmapped || !strings.Contains(res.Reason, "unsupported tool Skill") {
		t.Fatalf("got %+v", res)
	}
}

func TestReplayRewritePrefixHonoursPathBoundaries(t *testing.T) {
	got := rewritePrefix(`cat /ws/repo/a /ws/repo2/b "/ws/repo" /ws/repo.git`, "/ws/repo", "/T")
	want := `cat /T/a /ws/repo2/b "/T" /ws/repo.git`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
