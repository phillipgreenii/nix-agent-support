package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/config"
)

func TestDoctorHeader_poolContext(t *testing.T) {
	got := doctorPoolHeader(config.Config{PoolRoot: "/pools/alpha", DBPath: "/pools/alpha/store.db", StateDir: "/pools/alpha", Tmux: config.Tmux{Socket: "cc-abc123"}})
	for _, want := range []string{"/pools/alpha", "store.db", "cc-abc123", "diagnostics.jsonl", "events.jsonl"} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor header missing %q:\n%s", want, got)
		}
	}
	def := doctorPoolHeader(config.Config{PoolRoot: "", DBPath: "/xdg/store.db", Tmux: config.Tmux{Socket: "ccpool"}})
	if !strings.Contains(def, "default") {
		t.Errorf("default mode header should say 'default':\n%s", def)
	}
}

// TestPools_isolated is the core isolation guarantee: two --pool dirs each get their
// own store + tmux server, so sessions in one are invisible to the other; and a dir
// containing foreign content is refused with exit 2. Uses fake-claude + real tmux
// (token-free), mirroring TestReap_closesOverCap.
func TestPools_isolated(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "ccpool")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	src, err := os.ReadFile("testdata/fake-claude")
	if err != nil {
		t.Fatalf("read fake-claude: %v", err)
	}
	fake := filepath.Join(base, "fake-claude")
	if err := os.WriteFile(fake, src, 0o755); err != nil {
		t.Fatal(err)
	}
	poolA := filepath.Join(base, "A")
	poolB := filepath.Join(base, "B")
	if err := os.MkdirAll(poolA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(poolB, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[claude]\nbin = \"" + fake + "\"\nplugin_dir = \"/unused\"\n[wait]\ntimeout = \"10s\"\n"
	for _, p := range []string{poolA, poolB} {
		if err := os.WriteFile(filepath.Join(p, "config.toml"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "HOME="+base, "CCPOOL_BIN="+bin, "PATH="+base+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		for _, p := range []string{poolA, poolB} {
			if abs, e := filepath.EvalSymlinks(p); e == nil {
				_ = exec.Command("tmux", "-L", config.SocketFor(abs), "kill-server").Run()
			}
		}
	})
	run := func(pool string, args ...string) (string, int) {
		full := append([]string{"--pool", pool}, args...)
		cmd := exec.Command(bin, full...)
		cmd.Env = env
		cmd.Dir = base
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run %v: %v\n%s", full, err, out)
		}
		return string(out), code
	}

	run(poolA, "new", "alpha")
	run(poolB, "new", "beta")

	outA, _ := run(poolA, "list", "--all")
	outB, _ := run(poolB, "list", "--all")
	if !strings.Contains(outA, "alpha") || strings.Contains(outA, "beta") {
		t.Errorf("pool A list leaked across pools:\n%s", outA)
	}
	if !strings.Contains(outB, "beta") || strings.Contains(outB, "alpha") {
		t.Errorf("pool B list leaked across pools:\n%s", outB)
	}

	// Distinct tmux servers (independent sockets).
	absA, _ := filepath.EvalSymlinks(poolA)
	absB, _ := filepath.EvalSymlinks(poolB)
	if config.SocketFor(absA) == config.SocketFor(absB) {
		t.Errorf("two pools must use distinct tmux sockets")
	}

	// A dir with foreign content is refused with exit 2.
	foreign := filepath.Join(base, "C")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "README.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := run(foreign, "list"); code != 2 {
		t.Errorf("foreign pool dir should exit 2, got %d", code)
	}
}
