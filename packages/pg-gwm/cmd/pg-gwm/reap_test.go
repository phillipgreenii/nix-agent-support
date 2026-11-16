package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

func TestReapCommand(t *testing.T) {
	c := newCLI(t)
	if code, _, e := c.run("lease", "--name", "demo", "-b", "demo", "--format=json"); code != 0 {
		t.Fatalf("lease: %d %s", code, e)
	}
	if code, _, e := c.run("release", "--name", "demo", "--format=json"); code != 0 {
		t.Fatalf("release: %d %s", code, e)
	}

	code, out, _ := c.run("reap", "--dry-run", "--format=json")
	got := decode(t, out)
	if code != 0 || got["schema_version"] != float64(1) {
		t.Fatalf("dry run: %d %s", code, out)
	}
	if _, err := os.Stat(state.ReaperPath()); err == nil {
		t.Error("a dry run wrote reaper.json")
	}

	code, out, _ = c.run("reap", "--format=json")
	got = decode(t, out)
	res := got["results"].([]any)
	if code != 0 || len(res) != 1 {
		t.Fatalf("reap: %d %s", code, out)
	}
	r := res[0].(map[string]any)
	if r["outcome"] != "completed" || len(r["removed"].([]any)) != 1 || r["kept"] == nil {
		t.Errorf("result = %v", r)
	}
	if _, err := os.Stat(state.ReaperPath()); err != nil {
		t.Errorf("no reaper.json: %v", err)
	}

	c.tty = true
	if code, out, _ = c.run("reap"); code != 0 || !strings.Contains(out, "completed") {
		t.Errorf("text reap: %d %q", code, out)
	}
}

func TestReapAllReposCommand(t *testing.T) {
	c := newCLI(t)
	// Nothing configured and nothing registered: completed, empty, exit 0.
	var out, errb strings.Builder
	_ = out
	_ = errb
	c.tty = false
	if code := c.a.run([]string{"reap", "--all-repos", "--format=json"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(c.out.Name())
	if !strings.Contains(string(b), `"results":[]`) {
		t.Errorf("output = %s", b)
	}
	rec, _ := state.ReadReaperRecord(state.ReaperPath())
	if rec.Outcome != "completed" || rec.LastSuccess == nil {
		t.Errorf("reaper.json = %+v", rec)
	}
	if code := c.a.run([]string{"reap", "--all-repos", "--repo", c.repo.Dir}); code != 2 {
		t.Errorf("--all-repos with --repo exit %d", code)
	}
	if code, _, _ := c.run("reap", "--deadline", "soon"); code != 2 {
		t.Errorf("bad deadline exit %d", code)
	}
}

func TestReapBusyExitsNine(t *testing.T) {
	c := newCLI(t)
	if code, _, e := c.run("lease", "--name", "x", "--detach", "--format=json"); code != 0 {
		t.Fatalf("lease: %d %s", code, e)
	}
	conf := config.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(c.repo.Dir)
	if err := os.WriteFile(conf, []byte("[repos.\""+real+"\"]\nlock-wait = \"1s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _, err := c.a.openManager(t.Context(), c.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	lk, err := m.Store.Lock(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Unlock()
	if code, _, _ := c.run("reap", "--format=json"); code != 9 {
		t.Errorf("exit %d, want 9", code)
	}
}
