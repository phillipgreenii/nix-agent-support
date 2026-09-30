//go:build !contract

package main

// Package-level hermetic environment for this package's tests (pg2-7jaxd).
//
// Several tests here drive production entry points (runNew, runClose, ...)
// IN-PROCESS and rely on an early validation return to stay away from real
// config, store and tmux. That is only as safe as the validation: when a
// pg-go-mutate mutant disabled runNew's --permission-mode guard,
// TestRunNew_rejectsUnknownPermissionMode fell straight through into a real
// launch — `tmux -L ccpool new-session -d -s cc-alpha -c <pkg dir> ... --
// claude ... --permission-mode nope` on the operator's shared default socket,
// a `starting` row in the real store, a trust entry in the real ~/.claude.json
// — and the dead-pane tmux server outlived the run with its cwd in the
// worktree, blocking worktree teardown.
//
// TestMain therefore points every path a default-mode (no --pool) ccpool
// command resolves at a throwaway sandbox BEFORE any test runs:
//
//   - XDG_CONFIG_HOME is empty, so config.Load yields the built-in defaults,
//     which set no claude.plugin_dir: any Ensure stops at its ErrNoPluginDir
//     preflight, before the trust write and before any tmux launch.
//   - XDG_DATA_HOME, XDG_STATE_HOME, XDG_RUNTIME_DIR and CCPOOL_REGISTRY_DIR
//     move the store, the event/diagnostic logs, the lock dir and the pool
//     registry.
//   - TMUX_TMPDIR moves every tmux socket: tmux resolves `-L <name>` to
//     $TMUX_TMPDIR/tmux-<uid>/<name>, so even the DEFAULT socket name is a
//     different socket file from the operator's /tmp/tmux-<uid>/<name>.
//   - CCPOOL_POOL, CCPOOL_EXTERNAL_ID, CCPOOL_AUTONOMOUS, TMUX and TMUX_PANE
//     are cleared, so nothing inherited from the invoking shell (or from
//     running inside a ccpool session) aims a test at a live pool or server.
//
// HOME is deliberately NOT replaced: integration-tagged tests share this
// TestMain and exec `go build`, whose build and module caches derive from
// HOME. The default-mode trust write is unreachable anyway (no plugin_dir),
// and a test that configures a launchable pool sets HOME itself (armedLaunchEnv
// in sandbox_test.go; isolateLabelerEnv does the same). The contract suite has
// its own TestMain (real claude needs the real HOME), hence the !contract
// constraint on this file.
//
// After the run every tmux server left in the sandbox is killed, so no process
// outlives the test binary with its cwd in the worktree. A server left on the
// sandbox's DEFAULT socket also fails the run: it means some test reached a
// real default-mode launch, the exact defect this file exists to contain.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/registry"
)

// hermeticRoot is the sandbox TestMain created. It is "" only when TestMain did
// not run, which TestHermeticEnv_* treats as a failure rather than a skip.
var hermeticRoot string

// hermeticEnvDirs maps each redirected variable to its sandbox subdirectory.
var hermeticEnvDirs = map[string]string{
	"XDG_CONFIG_HOME":     "cfg",
	"XDG_DATA_HOME":       "data",
	"XDG_STATE_HOME":      "state",
	"XDG_RUNTIME_DIR":     "run",
	"CCPOOL_REGISTRY_DIR": "reg",
	"TMUX_TMPDIR":         "tmux",
}

// hermeticEnvCleared lists the inherited variables TestMain unsets.
var hermeticEnvCleared = []string{"CCPOOL_POOL", "CCPOOL_EXTERNAL_ID", "CCPOOL_AUTONOMOUS", "TMUX", "TMUX_PANE"}

func TestMain(m *testing.M) {
	root, err := enterHermeticEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccpool tests: hermetic env setup failed: %v\n", err)
		os.Exit(1)
	}
	hermeticRoot = root
	// The sandbox's default socket, as the production default config names it
	// (no config.toml exists in the sandbox, so this is the built-in default).
	cfg, err := config.LoadForPool("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccpool tests: load sandbox default config: %v\n", err)
		os.Exit(1)
	}
	tmuxDir := filepath.Join(root, hermeticEnvDirs["TMUX_TMPDIR"])
	defaultSock := filepath.Join(tmuxDir, fmt.Sprintf("tmux-%d", os.Getuid()), cfg.Tmux.Socket)

	code := m.Run()

	live := killTmuxServersUnder(tmuxDir)
	if sessions, ok := live[defaultSock]; ok {
		fmt.Fprintf(os.Stderr, "ccpool tests: FAIL: a tmux server was left on the DEFAULT socket %q (sessions %v): a test reached a real default-mode launch (pg2-7jaxd); killed it\n", cfg.Tmux.Socket, sessions)
		if code == 0 {
			code = 1
		}
	}
	for sock, sessions := range live {
		if sock != defaultSock {
			fmt.Fprintf(os.Stderr, "ccpool tests: killed leftover tmux server %s (sessions %v)\n", sock, sessions)
		}
	}
	_ = os.RemoveAll(root)
	os.Exit(code)
}

// enterHermeticEnv creates the sandbox and repoints the process environment at
// it (see the file comment for what each variable covers). Returns the root.
func enterHermeticEnv() (string, error) {
	root, err := shortTempDir("ccpool-test-")
	if err != nil {
		return "", err
	}
	for k, sub := range hermeticEnvDirs {
		p := filepath.Join(root, sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return "", err
		}
		if err := os.Setenv(k, p); err != nil {
			return "", err
		}
	}
	for _, k := range hermeticEnvCleared {
		if err := os.Unsetenv(k); err != nil {
			return "", err
		}
	}
	return root, nil
}

// within reports whether p is root itself or lies below it.
func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// TestHermeticEnv_defaultPoolResolvesIntoSandbox pins TestMain's isolation: with
// no --pool, every path a ccpool command resolves (store, state and logs, lock
// dir, registry, tmux socket dir) lies inside the sandbox, the default config is
// unlaunchable (no plugin_dir), and no inherited pool/tmux selector survives.
// If TestMain is removed or stops covering a path, this fails instead of the
// next leak landing on the operator's live pool.
func TestHermeticEnv_defaultPoolResolvesIntoSandbox(t *testing.T) {
	if hermeticRoot == "" {
		t.Fatal("TestMain did not install the hermetic test environment")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	regDir, err := registry.Dir()
	if err != nil {
		t.Fatalf("registry.Dir: %v", err)
	}
	for name, p := range map[string]string{
		"DBPath":           cfg.DBPath,
		"StateDir":         cfg.StateDir,
		"RuntimeDir":       cfg.RuntimeDir,
		"EventLogPath":     cfg.EventLogPath(),
		"DiagLogPath":      cfg.DiagLogPath(),
		"StateDirPath":     config.StateDirPath(),
		"registry.Dir":     regDir,
		"$TMUX_TMPDIR":     os.Getenv("TMUX_TMPDIR"),
		"$XDG_CONFIG_HOME": os.Getenv("XDG_CONFIG_HOME"),
	} {
		if !within(hermeticRoot, p) {
			t.Errorf("%s = %q, want it inside the sandbox %q", name, p, hermeticRoot)
		}
	}
	if cfg.PoolRoot != "" {
		t.Errorf("PoolRoot = %q, want default mode (\"\")", cfg.PoolRoot)
	}
	if cfg.Claude.PluginDir != "" {
		t.Errorf("default claude.plugin_dir = %q, want empty so a default-mode Ensure cannot launch", cfg.Claude.PluginDir)
	}
	for _, k := range hermeticEnvCleared {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s=%q is set; TestMain must clear it", k, v)
		}
	}
}
