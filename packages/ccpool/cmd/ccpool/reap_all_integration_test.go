package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
)

// TestReapAll_gcAndSkip exercises reap-all's registry sweep without any live
// sessions: a dangling symlink and one whose target went foreign are GC'd (symlink
// removed, target data untouched), while a valid pool and an auto_reap=false pool
// stay registered.
func TestReapAll_gcAndSkip(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "ccpool")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	reg := filepath.Join(base, "reg")
	if err := os.MkdirAll(reg, 0o700); err != nil {
		t.Fatal(err)
	}

	valid := filepath.Join(base, "valid")
	_ = os.MkdirAll(valid, 0o700)

	noReap := filepath.Join(base, "noreap")
	_ = os.MkdirAll(noReap, 0o700)
	_ = os.WriteFile(filepath.Join(noReap, "config.toml"), []byte("[pool]\nauto_reap = false\n"), 0o600)

	invalid := filepath.Join(base, "invalid")
	_ = os.MkdirAll(invalid, 0o700)
	_ = os.WriteFile(filepath.Join(invalid, "README.md"), nil, 0o600)

	dangling := filepath.Join(base, "dangling") // never created

	link := func(name, target string) {
		if err := os.Symlink(target, filepath.Join(reg, name)); err != nil {
			t.Fatal(err)
		}
	}
	link("cc-valid", valid)
	link("cc-noreap", noReap)
	link("cc-invalid", invalid)
	link("cc-dangling", dangling)

	env := append(os.Environ(),
		"HOME="+base,
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(base, "run"),
		"CCPOOL_REGISTRY_DIR="+reg,
	)
	cmd := exec.Command(bin, "reap-all")
	cmd.Env = env
	cmd.Dir = base
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reap-all (GC + no-op reaps should exit 0): %v\n%s", err, out)
	}

	exists := func(name string) bool {
		_, err := os.Lstat(filepath.Join(reg, name))
		return err == nil
	}
	if exists("cc-dangling") {
		t.Error("dangling symlink should be GC'd")
	}
	if exists("cc-invalid") {
		t.Error("invalid-target symlink should be GC'd")
	}
	if !exists("cc-valid") {
		t.Error("valid pool symlink should remain")
	}
	if !exists("cc-noreap") {
		t.Error("auto_reap=false pool stays registered")
	}
	// GC removes the symlink ONLY — never the target or its data.
	if _, err := os.Stat(filepath.Join(invalid, "README.md")); err != nil {
		t.Errorf("GC must not touch the target dir/data: %v", err)
	}
}

// TestReapAll_governsRegisteredPools is the core behavioral guarantee: one
// `ccpool reap-all` run reaps the default pool AND every registered pool, skips an
// auto_reap=false pool, and a manual `ccpool reap` still reaps that skipped pool.
// Uses fake-claude + real tmux (token-free), mirroring TestReap_closesOverCap.
func TestReapAll_governsRegisteredPools(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	_ = os.MkdirAll(bin, 0o755)
	ccpool := filepath.Join(bin, "ccpool")
	if out, err := exec.Command("go", "build", "-o", ccpool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	src, _ := os.ReadFile("testdata/fake-claude")
	fake := filepath.Join(bin, "fake-claude")
	_ = os.WriteFile(fake, src, 0o755)

	reg := filepath.Join(base, "reg")
	// default pool config (XDG): max_sessions=0 → any session is over cap → reaped.
	defSocket := "ccpool-reapall-def"
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`
[pool]
max_sessions = 0
idle_ttl = "0s"
[tmux]
socket = "`+defSocket+`"
[claude]
bin = "`+fake+`"
plugin_dir = "/unused"
[wait]
timeout = "10s"
`), 0o600)

	// Two named pools: A reaps automatically; N opts out (auto_reap=false).
	poolA := filepath.Join(base, "A")
	poolN := filepath.Join(base, "N")
	namedCfg := func(autoReap bool) string {
		ar := ""
		if !autoReap {
			ar = "auto_reap = false\n"
		}
		return "[pool]\nmax_sessions = 0\nidle_ttl = \"0s\"\n" + ar +
			"[claude]\nbin = \"" + fake + "\"\nplugin_dir = \"/unused\"\n[wait]\ntimeout = \"10s\"\n"
	}

	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(base, "run"),
		"CCPOOL_REGISTRY_DIR="+reg,
		"HOME="+base, "CCPOOL_BIN="+ccpool, "PATH="+bin+":"+os.Getenv("PATH"),
	)
	var socketA, socketN string
	t.Cleanup(func() {
		for _, s := range []string{defSocket, socketA, socketN} {
			if s != "" {
				_ = exec.Command("tmux", "-L", s, "kill-server").Run()
			}
		}
	})
	run := func(args ...string) (string, int) {
		cmd := exec.Command(ccpool, args...)
		cmd.Env = env
		cmd.Dir = base
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run %v: %v\n%s", args, err, out)
		}
		return string(out), code
	}
	hasSession := func(socket, name string) bool {
		return exec.Command("tmux", "-L", socket, "has-session", "-t", "cc-"+name).Run() == nil
	}

	// Register each named pool by letting ccpool create its dir (a read-only `list`
	// hits the create-on-first-resolve path), THEN drop its config.toml. A pre-mkdir'd
	// dir would take the validate branch and never register — the documented
	// pre-existing-pool limitation.
	if _, code := run("--pool", poolA, "list"); code != 0 {
		t.Fatal("registering pool A failed")
	}
	if _, code := run("--pool", poolN, "list"); code != 0 {
		t.Fatal("registering pool N failed")
	}
	_ = os.WriteFile(filepath.Join(poolA, "config.toml"), []byte(namedCfg(true)), 0o600)
	_ = os.WriteFile(filepath.Join(poolN, "config.toml"), []byte(namedCfg(false)), 0o600)
	socketA = config.SocketFor(mustEval(t, poolA))
	socketN = config.SocketFor(mustEval(t, poolN))

	// Exactly the two named pools are registered; the default pool never self-registers.
	if links, _ := os.ReadDir(reg); len(links) != 2 {
		t.Fatalf("registry should hold exactly 2 named pools (default never registers), got %d", len(links))
	}

	run("new", "delta") // default pool
	run("--pool", poolA, "new", "alpha")
	run("--pool", poolN, "new", "november")
	time.Sleep(50 * time.Millisecond)

	if _, code := run("reap-all"); code != 0 {
		t.Fatal("reap-all failed")
	}
	time.Sleep(400 * time.Millisecond)

	if hasSession(defSocket, "delta") {
		t.Error("reap-all should reap the default pool's over-cap session")
	}
	if hasSession(socketA, "alpha") {
		t.Error("reap-all should reap registered pool A's over-cap session")
	}
	if !hasSession(socketN, "november") {
		t.Error("reap-all must SKIP an auto_reap=false pool (november should survive)")
	}

	// Manual reap still reaps the no-reap pool.
	if _, code := run("--pool", poolN, "reap"); code != 0 {
		t.Fatal("manual reap on no-reap pool failed")
	}
	time.Sleep(400 * time.Millisecond)
	if hasSession(socketN, "november") {
		t.Error("manual `ccpool reap` must still reap an auto_reap=false pool")
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("eval %s: %v", p, err)
	}
	return r
}
