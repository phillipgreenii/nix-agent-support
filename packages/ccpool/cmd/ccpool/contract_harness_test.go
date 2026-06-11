//go:build contract

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// builtBin is the binary under test, built once. CCPOOL_BIN overrides it with an
// already-built (e.g. installed) binary so the suite can test the shipped ccpool.
var (
	builtBin  string
	buildOnce sync.Once
	buildErr  error
)

func TestMain(m *testing.M) {
	// Hard requirements; without them every scenario is meaningless.
	for _, tool := range []string{"tmux", "claude", "sqlite3"} {
		if _, err := exec.LookPath(tool); err != nil {
			fmt.Fprintf(os.Stderr, "contract suite skipped: %q not on PATH\n", tool)
			os.Exit(0) // skip cleanly, not a failure
		}
	}
	os.Exit(m.Run())
}

// ccpoolBin returns the path to the binary under test, building it once.
func ccpoolBin(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("CCPOOL_BIN"); env != "" {
		return env
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ccpool-contract-bin-")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "ccpool")
		if out, e := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); e != nil {
			buildErr = fmt.Errorf("build ccpool: %v\n%s", e, out)
			return
		}
		builtBin = bin
	})
	if buildErr != nil {
		t.Fatalf("%v", buildErr)
	}
	return builtBin
}

func TestContract_GuardAndBuild(t *testing.T) {
	bin := ccpoolBin(t)
	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("ccpool version: %v\n%s", err, out)
	}
	t.Logf("binary under test: %s (version %s)", bin, out)
}

// sandbox is one isolated ccpool world: temp XDG dirs, a unique tmux socket, the
// repo's source plugin (hooks call bare `ccpool`), the binary-under-test on PATH.
// Real HOME is preserved (claude needs it for OAuth); the price is a folder-trust
// write to the real ~/.claude.json for cwd — accepted and documented.
type sandbox struct {
	t      *testing.T
	bin    string
	socket string
	prefix string
	cwd    string
	env    []string
}

const contractModel = "claude-opus-4-8" // pinned: exposes high (xhigh) reasoning effort

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	bin := ccpoolBin(t)

	base := t.TempDir()
	mustMkdir := func(p string) string { _ = os.MkdirAll(p, 0o755); return p }
	cfgHome := mustMkdir(filepath.Join(base, "cfg"))
	dataHome := mustMkdir(filepath.Join(base, "data"))
	stateHome := mustMkdir(filepath.Join(base, "state"))
	runDir := mustMkdir(filepath.Join(base, "run"))
	cwd := mustMkdir(filepath.Join(base, "work"))
	binDir := mustMkdir(filepath.Join(base, "bin"))

	if err := os.Symlink(bin, filepath.Join(binDir, "ccpool")); err != nil {
		t.Fatalf("symlink ccpool onto PATH: %v", err)
	}

	socket := "cc-contract-" + filepath.Base(base)
	prefix := "cct-"

	repoRoot, err := filepath.Abs("../..") // cmd/ccpool -> packages/ccpool
	if err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(repoRoot, "ccpool-plugin")

	cfg := "" +
		"[pool]\nmax_sessions = 6\nidle_ttl = \"30m\"\n\n" +
		"[tmux]\nsocket = \"" + socket + "\"\nprefix = \"" + prefix + "\"\n\n" +
		"[claude]\nplugin_dir = \"" + pluginDir + "\"\ndefault_cwd = \"" + cwd + "\"\n" +
		"default_model = \"" + contractModel + "\"\nbin = \"claude\"\n\n" +
		"[wait]\ntimeout = \"5m\"\n\n" +
		"[notify]\nadapter = \"none\"\non = []\n"
	cfgDir := mustMkdir(filepath.Join(cfgHome, "ccpool"))
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+cfgHome,
		"XDG_DATA_HOME="+dataHome,
		"XDG_STATE_HOME="+stateHome,
		"XDG_RUNTIME_DIR="+runDir,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)

	sb := &sandbox{t: t, bin: bin, socket: socket, prefix: prefix, cwd: cwd, env: env}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	return sb
}

// withFakeClaude rewrites the sandbox config to launch the fake-claude stub.
func (sb *sandbox) withFakeClaude() {
	sb.t.Helper()
	src, err := os.ReadFile("testdata/fake-claude")
	if err != nil {
		sb.t.Fatal(err)
	}
	binDir := filepath.Dir(strings.SplitN(sb.envGet("PATH"), string(os.PathListSeparator), 2)[0])
	fake := filepath.Join(binDir, "fake-claude")
	if err := os.WriteFile(fake, src, 0o755); err != nil {
		sb.t.Fatal(err)
	}
	cfgPath := filepath.Join(sb.envGet("XDG_CONFIG_HOME"), "ccpool", "config.toml")
	b, _ := os.ReadFile(cfgPath)
	out := strings.Replace(string(b), `bin = "claude"`, `bin = "`+fake+`"`, 1)
	_ = os.WriteFile(cfgPath, []byte(out), 0o600)
	sb.env = append(sb.env, "CCPOOL_BIN="+sb.bin)
}

func (sb *sandbox) envGet(key string) string {
	for i := len(sb.env) - 1; i >= 0; i-- {
		if strings.HasPrefix(sb.env[i], key+"=") {
			return strings.TrimPrefix(sb.env[i], key+"=")
		}
	}
	return ""
}

// ccp runs the binary-under-test with the sandbox env; returns combined output + exit code.
func (sb *sandbox) ccp(args ...string) (string, int) {
	sb.t.Helper()
	cmd := exec.Command(sb.bin, args...)
	cmd.Env = sb.env
	cmd.Dir = sb.cwd
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		sb.t.Fatalf("ccp %v: %v\n%s", args, err, out)
	}
	return string(out), code
}

// ccpTimed runs ccp but fails the test if it does not return within budget.
func (sb *sandbox) ccpTimed(budget time.Duration, args ...string) (string, int, time.Duration) {
	sb.t.Helper()
	type res struct {
		out  string
		code int
	}
	ch := make(chan res, 1)
	start := time.Now()
	go func() { o, c := sb.ccp(args...); ch <- res{o, c} }()
	select {
	case r := <-ch:
		return r.out, r.code, time.Since(start)
	case <-time.After(budget):
		sb.t.Fatalf("ccp %v did not return within %s (hang)", args, budget)
		return "", 0, 0
	}
}

// cap captures the tmux pane for session <prefix><name>.
func (sb *sandbox) cap(name string) string {
	sb.t.Helper()
	out, _ := exec.Command("tmux", "-L", sb.socket, "capture-pane", "-t", sb.prefix+name, "-p").Output()
	return string(out)
}

// setState writes a store-row state directly (fixture).
func (sb *sandbox) setState(name, state string) {
	sb.t.Helper()
	db := filepath.Join(sb.envGet("XDG_DATA_HOME"), "ccpool", "store.db")
	q := fmt.Sprintf("update sessions set state='%s' where name='%s';", state, name)
	if out, err := exec.Command("sqlite3", db, q).CombinedOutput(); err != nil {
		sb.t.Fatalf("setState: %v\n%s", err, out)
	}
}

func TestContract_Sandbox_FakeClaudeReachesReady(t *testing.T) {
	sb := newSandbox(t)
	sb.withFakeClaude()
	out, code := sb.ccp("new", "alpha")
	if code != 0 {
		t.Fatalf("new exit=%d: %s", code, out)
	}
	out, _ = sb.ccp("list")
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "ready") {
		t.Fatalf("list missing alpha/ready: %s", out)
	}
}
