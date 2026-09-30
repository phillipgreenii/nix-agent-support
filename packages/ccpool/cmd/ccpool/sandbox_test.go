package main

// Per-test sandbox helpers for in-process runs of ccpool commands (pg2-7jaxd).
// Untagged, so they build under every tag set; the package-level TestMain that
// sandboxes the whole run lives in hermetic_test.go.

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/config"
)

// shortTempDir makes a temp dir under /tmp when it can. macOS's $TMPDIR
// (/var/folders/...) is long enough that $TMUX_TMPDIR/tmux-<uid>/<socket>
// overflows the ~104-byte Unix socket path limit ("File name too long").
func shortTempDir(pattern string) (string, error) {
	if d, err := os.MkdirTemp("/tmp", pattern); err == nil {
		return d, nil
	}
	return os.MkdirTemp("", pattern)
}

// tmuxSocketsUnder returns every Unix socket file below dir (tmux servers
// started with TMUX_TMPDIR at or under dir). A missing dir yields none.
func tmuxSocketsUnder(dir string) []string {
	var socks []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSocket != 0 {
			socks = append(socks, p)
		}
		return nil
	})
	return socks
}

// killTmuxServersUnder kills the tmux server behind every socket below dir and
// returns the sessions each LIVE server was holding, keyed by socket path.
// Without tmux on PATH no server can have been started, so it is a no-op.
func killTmuxServersUnder(dir string) map[string][]string {
	live := map[string][]string{}
	if _, err := exec.LookPath("tmux"); err != nil {
		return live
	}
	for _, sock := range tmuxSocketsUnder(dir) {
		out, err := exec.Command("tmux", "-S", sock, "list-sessions", "-F", "#{session_name}").Output()
		_ = exec.Command("tmux", "-S", sock, "kill-server").Run()
		if err == nil {
			live[sock] = strings.Fields(string(out))
		}
	}
	return live
}

// armedEnv is one test's private, LAUNCHABLE default pool (see armedLaunchEnv).
type armedEnv struct {
	home    string // $HOME: the trust write would land in home/.claude.json
	cwd     string // a --cwd for the command under test, outside the worktree
	dbPath  string // the store the command would open
	tmuxDir string // $TMUX_TMPDIR: any tmux server it started lives below here
}

// armedLaunchEnv gives one test its own sandbox whose default pool CAN launch:
// claude.plugin_dir is set (so Ensure passes its preflight), claude.bin names a
// binary that does not exist (so nothing real ever starts), the ready wait is
// 1s, and HOME, XDG_*, the registry and TMUX_TMPDIR are all per-test. A test
// expecting an early usage-error return pairs it with assertNoLaunch to prove
// the return came BEFORE any I/O: if the guard regresses, the command reaches
// store/trust/tmux for real — inside this sandbox, where assertNoLaunch sees
// it — rather than passing vacuously on an unlaunchable default config. Any
// tmux server it did start is killed on cleanup (and reported as a failure).
func armedLaunchEnv(t *testing.T) armedEnv {
	t.Helper()
	base := t.TempDir()
	// Short, under /tmp: t.TempDir() is too long for a tmux socket path.
	tmuxDir, err := shortTempDir("ccpool-armed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if live := killTmuxServersUnder(tmuxDir); len(live) > 0 {
			t.Errorf("killed tmux server(s) this test started: %v", live)
		}
		_ = os.RemoveAll(tmuxDir)
	})
	a := armedEnv{home: filepath.Join(base, "home"), cwd: filepath.Join(base, "work"), tmuxDir: tmuxDir}
	for _, d := range []string{a.home, a.cwd, filepath.Join(base, "plugin"), filepath.Join(base, "cfg", "ccpool")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{
		"HOME":                a.home,
		"XDG_CONFIG_HOME":     filepath.Join(base, "cfg"),
		"XDG_DATA_HOME":       filepath.Join(base, "data"),
		"XDG_STATE_HOME":      filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR":     filepath.Join(base, "run"),
		"CCPOOL_REGISTRY_DIR": filepath.Join(base, "reg"),
		"TMUX_TMPDIR":         tmuxDir,
		"CCPOOL_POOL":         "",
	} {
		t.Setenv(k, v)
	}
	noClaude := filepath.Join(base, "no-such-claude")
	body := fmt.Sprintf("[claude]\nplugin_dir = %q\nbin = %q\n\n[wait]\ntimeout = \"1s\"\n\n[notify]\nadapter = \"none\"\non = []\n",
		filepath.Join(base, "plugin"), noClaude)
	if err := os.WriteFile(filepath.Join(base, "cfg", "ccpool", "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load (armed): %v", err)
	}
	if cfg.Claude.PluginDir == "" || cfg.Claude.Bin != noClaude {
		t.Fatalf("armed config not in effect (plugin_dir=%q bin=%q): the test would pass vacuously", cfg.Claude.PluginDir, cfg.Claude.Bin)
	}
	a.dbPath = cfg.DBPath
	return a
}

// assertNoLaunch fails if the command under test opened the store, wrote the
// trust file, or started a tmux server in a's sandbox.
func (a armedEnv) assertNoLaunch(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(a.dbPath); !os.IsNotExist(err) {
		t.Errorf("store %s exists (stat err=%v): the command reached store I/O", a.dbPath, err)
	}
	if _, err := os.Stat(filepath.Join(a.home, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf("%s/.claude.json exists (stat err=%v): the command reached the trust write", a.home, err)
	}
	if socks := tmuxSocketsUnder(a.tmuxDir); len(socks) > 0 {
		t.Errorf("tmux socket(s) %v exist: the command started a tmux server", socks)
	}
}
