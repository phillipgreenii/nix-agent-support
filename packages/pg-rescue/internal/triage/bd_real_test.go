package triage

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realBD runs the real bd against a scratch tracker (embedded engine, no
// server) and returns the bead id it printed.
func realBD(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BEADS_DOLT_AUTO_START=0", "BEADS_DIR="+filepath.Join(dir, ".beads"), "BEADS_ACTOR=scratch")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func createReal(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out := realBD(t, dir, append([]string{"create", "--json"}, args...)...)
	var env struct {
		ID   string `json:"id"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if i := strings.Index(out, "{"); i >= 0 {
		_ = json.Unmarshal([]byte(out[i:]), &env)
	}
	if env.ID == "" {
		env.ID = env.Data.ID
	}
	if env.ID == "" {
		t.Fatalf("no id in %q", out)
	}
	return env.ID
}

// A pg-rescue PROJECT bead (the label, no item metadata) is left alone by the
// real triage path, while a genuine item (pg_rescue_* metadata) is still
// triaged. Runs against the real bd in a scratch tracker.
func TestRealBDProjectBeadIsNotAnItem(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	realBD(t, dir, "init", "--non-interactive", "--quiet", "-p", "scr")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DIR", filepath.Join(dir, ".beads"))
	t.Setenv("BEADS_ACTOR", "scratch")
	t.Chdir(dir)

	project := createReal(t, dir, "a project bug", "--type", "bug", "--labels", RescueLabel)
	item := createReal(t, dir, "a genuine item", "--type", "task", "--labels", RescueLabel,
		"--metadata", `{"pg_rescue_fingerprint":"f","pg_rescue_cwd":"/x"}`)

	tr := BD{Run: ExecRunner}.WithActor("triage-test")

	out, err := Run(tr, Options{ItemID: project, Actor: "triage-test", Host: "h"})
	if err != nil || out.Action != ActionSkipped {
		t.Fatalf("project bead: %+v %v", out, err)
	}
	got, err := tr.Show(project)
	if err != nil {
		t.Fatal(err)
	}
	if hasLabel(got.Labels, "human") || got.Status != "open" {
		t.Fatalf("project bead changed: %+v", got)
	}
	cs, err := tr.Comments(project)
	if err != nil || len(cs) != 0 {
		t.Fatalf("project bead commented on: %v %v", cs, err)
	}

	out, err = Run(tr, Options{ItemID: item, Actor: "triage-test", Host: "h"})
	if err != nil || out.Action != ActionHandover || out.Verdict != "unverifiable" {
		t.Fatalf("item: %+v %v", out, err)
	}
	got, _ = tr.Show(item)
	if !hasLabel(got.Labels, "human") {
		t.Fatalf("item not handed over: %+v", got)
	}
}
