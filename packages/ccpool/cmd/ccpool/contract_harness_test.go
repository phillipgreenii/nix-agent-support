//go:build contract

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

	// base = t.TempDir() = /tmp/<TestName><rand>/NNN, where NNN is "001" for the
	// FIRST TempDir of every test. Using only filepath.Base(base) collides across
	// tests; incorporate the unique per-test parent dir for a distinct socket.
	socket := "cc-contract-" + filepath.Base(filepath.Dir(base)) + "-" + filepath.Base(base)
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
	// Force a non-TTY stdin. With Stdin unset, exec wires the child to /dev/null,
	// which os.Stdin.Stat() reports as a char device -> stdinIsTerminal() is true,
	// so multi-candidate `attend` would enter the interactive picker (fzf on
	// /dev/tty) and hang forever under `go test`. An empty reader gives the child
	// a pipe (not a char device), so attend correctly takes the scriptable branch.
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		sb.t.Fatalf("ccp %v: %v\n%s", args, err, out)
	}
	return string(out), code
}

// ccpTimed runs the binary-under-test (mirroring ccp) but fails the test if it
// does not return within budget. It owns its *exec.Cmd so that, on timeout, it
// can Kill the spawned child (CombinedOutput would otherwise block forever) and
// report the failure on the MAIN goroutine — calling t.Fatal* off the test
// goroutine is a Go testing violation.
func (sb *sandbox) ccpTimed(budget time.Duration, args ...string) (string, int, time.Duration) {
	sb.t.Helper()
	type res struct {
		out  string
		code int
		err  error
	}
	cmd := exec.Command(sb.bin, args...)
	cmd.Env = sb.env
	cmd.Dir = sb.cwd
	cmd.Stdin = strings.NewReader("") // non-TTY stdin; see ccp for rationale
	ch := make(chan res, 1)
	start := time.Now()
	go func() {
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code, err = ee.ExitCode(), nil
		}
		ch <- res{string(out), code, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			sb.t.Fatalf("ccp %v: %v\n%s", args, r.err, r.out)
		}
		return r.out, r.code, time.Since(start)
	case <-time.After(budget):
		_ = cmd.Process.Kill()
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

// --- Task 4: machine-distinguishable OUTCOME helpers -----------------------
// go test only has PASS/FAIL/SKIP, so we emit OUTCOME= log lines a classifier
// can bucket into live / baseline-drift / pending / scaffold.

// liveAssert is an objective check that MUST hold; failing it is a real regression.
func liveAssert(t *testing.T, desc string, got, want any) {
	t.Helper()
	if got != want {
		t.Errorf("OUTCOME=live-fail test=%q desc=%q got=%v want=%v", t.Name(), desc, got, want)
		return
	}
	t.Logf("OUTCOME=live test=%q desc=%q ok=%v", t.Name(), desc, got)
}

// baseline pins the CURRENTLY OBSERVED value (not the desired one). Any drift —
// including a future fix — fails loudly. The baseline calls in code ARE the
// expected-deferred manifest.
func baseline(t *testing.T, bead, desc string, got, wantObserved any) {
	t.Helper()
	if got != wantObserved {
		t.Errorf("OUTCOME=baseline-drift bead=%s test=%q desc=%q got=%v wasObserved=%v (re-triage: behaviour changed)",
			bead, t.Name(), desc, got, wantObserved)
		return
	}
	t.Logf("OUTCOME=baseline bead=%s test=%q desc=%q observed=%v", bead, t.Name(), desc, got)
}

// pending records a check we cannot make until observability exists, then SKIPS.
// MUST be the last call in a test so it never short-circuits a live assert.
func pending(t *testing.T, desc, obsNeeded string) {
	t.Helper()
	t.Skipf("OUTCOME=pending test=%q desc=%q needs=%q", t.Name(), desc, obsNeeded)
}

// scaffoldFail marks the harness's own driving as broken (e.g. pane-rendering
// drift), NOT a verdict on the command under test.
func scaffoldFail(t *testing.T, format string, a ...any) {
	t.Helper()
	t.Fatalf("OUTCOME=scaffold test=%q msg=%q", t.Name(), fmt.Sprintf(format, a...))
}

// --- Task 5: phase gates ---------------------------------------------------
// CONTRACT-SENSITIVE patterns. When a Claude Code upgrade changes pane rendering,
// these stop matching and the gates SCAFFOLD-FAIL — never used for correctness
// assertions, only to DRIVE to a phase.
var (
	reThinking    = regexp.MustCompile(`thinking with|· ↓ \d+ tokens|\) +· +thinking`)
	reStreaming   = regexp.MustCompile(`\b\w+ for \d+s\b|⏺`)
	reInterrupted = regexp.MustCompile(`Interrupted`)
)

// thinkingPrompt reliably produces a multi-second high-effort thinking phase.
const thinkingPrompt = "Think step by step in extensive detail, then write a thorough 1500-word essay on the internal architecture of Unix pipes."

func (sb *sandbox) waitForThinking(name string, budget time.Duration) {
	sb.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if reThinking.MatchString(sb.cap(name)) {
			return
		}
		time.Sleep(400 * time.Millisecond)
	}
	scaffoldFail(sb.t, "thinking phase never observed for %q within %s", name, budget)
}

func (sb *sandbox) waitForStreaming(name string, budget time.Duration) {
	sb.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		p := sb.cap(name)
		if reStreaming.MatchString(p) && !reThinking.MatchString(p) {
			return
		}
		time.Sleep(400 * time.Millisecond)
	}
	scaffoldFail(sb.t, "streaming phase never observed for %q within %s", name, budget)
}

// mustNew ensures a session exists, retrying once on a transient launch failure
// (real claude can hiccup — rate/usage limits, slow start — under load). A
// persistent failure is an ENV/HARNESS problem, not a contract verdict, so it
// scaffoldFails (classified) rather than failing opaquely.
func (sb *sandbox) mustNew(name string) {
	sb.t.Helper()
	out, code := sb.ccp("new", name)
	if code == 0 {
		return
	}
	time.Sleep(2 * time.Second)
	out, code = sb.ccp("new", name)
	if code != 0 {
		scaffoldFail(sb.t, "new %q failed twice (transient real-claude/env issue, e.g. rate/usage limit): exit=%d %s", name, code, out)
	}
}

func TestContract_PhaseGate_ThinkingObserved(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("p")
	sb.ccp("reply", "p", thinkingPrompt, "--no-wait")
	sb.waitForThinking("p", 30*time.Second) // scaffoldFails if not seen
	liveAssert(t, "thinking observed", true, true)
}
