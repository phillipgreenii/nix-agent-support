//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	env := append(
		os.Environ(),
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

	env := append(
		os.Environ(),
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

// TestReapAll_closureNarrationResolvesEachPoolsOwnLabels: two registered pools,
// one labelled session each with a DIFFERENT role. One reap-all process closes
// both, and each "reap closed session" record carries its OWN pool's label —
// the labeler is re-set to each pool's store per iteration, never left on the
// first pool's (or a closed) store.
func TestReapAll_closureNarrationResolvesEachPoolsOwnLabels(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ccpool := filepath.Join(bin, "ccpool")
	if out, err := exec.Command("go", "build", "-o", ccpool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	src, err := os.ReadFile("testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(bin, "fake-claude")
	if err := os.WriteFile(fake, src, 0o755); err != nil {
		t.Fatal(err)
	}

	// Every pool: max_sessions=0 so a ready session is over cap and reaped.
	poolCfg := "[pool]\nmax_sessions = 0\nidle_ttl = \"0s\"\n" +
		"[claude]\nbin = \"" + fake + "\"\nplugin_dir = \"/unused\"\n" +
		"[wait]\ntimeout = \"10s\"\n[notify]\nadapter = \"none\"\n"
	defSocket := config.SocketFor(base) // default pool: unique, never the live "ccpool" socket
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("[tmux]\nsocket = \""+defSocket+"\"\n"+poolCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(
		envWithoutOTel(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(base, "run"),
		"CCPOOL_REGISTRY_DIR="+filepath.Join(base, "reg"),
		"HOME="+base, "CCPOOL_BIN="+ccpool, "PATH="+bin+":"+os.Getenv("PATH"),
	)
	run := func(extraEnv []string, args ...string) string {
		t.Helper()
		cmd := exec.Command(ccpool, args...)
		cmd.Env = append(append([]string{}, env...), extraEnv...)
		cmd.Dir = base
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ccpool %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	poolA := filepath.Join(base, "A")
	poolB := filepath.Join(base, "B")
	var sockets []string
	t.Cleanup(func() {
		for _, s := range append(sockets, defSocket) {
			_ = exec.Command("tmux", "-L", s, "kill-server").Run()
		}
	})
	// Register each pool via ccpool's own create-on-first-resolve, then drop in
	// its config (see TestReapAll_governsRegisteredPools for why this order).
	for _, p := range []string{poolA, poolB} {
		run(nil, "--pool", p, "list")
		if err := os.WriteFile(filepath.Join(p, "config.toml"), []byte(poolCfg), 0o600); err != nil {
			t.Fatal(err)
		}
		sockets = append(sockets, config.SocketFor(mustEval(t, p)))
	}

	// Each pool's first `new` starts that pool's tmux server, so its transcript
	// env is per pool.
	run([]string{"FAKE_CLAUDE_TRANSCRIPT=" + filepath.Join(base, "alpha.jsonl")},
		"--pool", poolA, "new", "alpha", "--meta", "pgrouter.role=review", "--label", "pgrouter.role")
	run([]string{"FAKE_CLAUDE_TRANSCRIPT=" + filepath.Join(base, "bravo.jsonl")},
		"--pool", poolB, "new", "bravo", "--meta", "pgrouter.role=worker", "--label", "pgrouter.role")
	// Cap eviction closes only a session whose turn has ended (ADR 0072), so
	// run one blocking turn on each: fake-claude's Stop leaves it idle.
	run(nil, "--pool", poolA, "reply", "alpha", "ping")
	run(nil, "--pool", poolB, "reply", "bravo", "ping")

	out := run(nil, "reap-all")

	lineA := narrationLine(out, "ccpool: reap closed session", "alpha")
	lineB := narrationLine(out, "ccpool: reap closed session", "bravo")
	if !strings.Contains(lineA, " pgrouter.role=review") || strings.Contains(lineA, "worker") {
		t.Errorf("pool A closure must carry ONLY its own pgrouter.role=review; got %q\n%s", lineA, out)
	}
	if !strings.Contains(lineB, " pgrouter.role=worker") || strings.Contains(lineB, "review") {
		t.Errorf("pool B closure must carry ONLY its own pgrouter.role=worker; got %q\n%s", lineB, out)
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
