package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_poolMode(t *testing.T) {
	pool := t.TempDir()
	t.Setenv("CCPOOL_POOL", pool)
	// canonicalize expected path to match resolver output (macOS /var → /private/var symlink)
	poolCanon, _ := filepath.EvalSymlinks(pool)
	// no config.toml in the pool → built-in defaults
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PoolRoot != poolCanon {
		t.Errorf("PoolRoot = %q, want %q", c.PoolRoot, poolCanon)
	}
	if c.DBPath != filepath.Join(poolCanon, "store.db") {
		t.Errorf("DBPath = %q", c.DBPath)
	}
	if c.StateDir != poolCanon {
		t.Errorf("StateDir = %q, want pool root (hook.log lives here)", c.StateDir)
	}
	if c.Pool.MaxSessions != 6 || c.Tmux.Prefix != "cc-" {
		t.Errorf("no-config pool must use built-in defaults: max=%d prefix=%q", c.Pool.MaxSessions, c.Tmux.Prefix)
	}
	if c.Tmux.Socket == "ccpool" || c.Tmux.Socket == "" {
		t.Errorf("pool-mode socket must be derived, got %q", c.Tmux.Socket)
	}
	if c.RuntimeDir != poolCanon {
		t.Errorf("RuntimeDir = %q, want pool root (*.lock files live here)", c.RuntimeDir)
	}
}

// TestLoad_poolMode_socketOverridesConfig asserts that the derived socket (based on
// the pool dir path) overrides any [tmux] socket value written in a pool-local
// config.toml. This is the most surprising behavior: a user writing socket = "custom"
// inside the pool dir does NOT get that socket — the derived socket is always used.
func TestLoad_poolMode_socketOverridesConfig(t *testing.T) {
	pool := t.TempDir()
	t.Setenv("CCPOOL_POOL", pool)
	poolCanon, _ := filepath.EvalSymlinks(pool)

	// Write a config.toml inside the pool with a custom socket and prefix.
	body := "[tmux]\nsocket = \"custom\"\nprefix = \"zz-\"\n"
	if err := os.WriteFile(filepath.Join(pool, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantSocket, _ := filepath.EvalSymlinks(poolCanon)
	wantSocket = SocketFor(wantSocket)
	if c.Tmux.Socket == "custom" || c.Tmux.Socket != wantSocket {
		t.Errorf("Tmux.Socket = %q, want derived socket %q (not config.toml value)", c.Tmux.Socket, wantSocket)
	}
	if c.Tmux.Prefix != "cc-" {
		t.Errorf("Tmux.Prefix = %q, want cc- (pool-mode constant, not config.toml zz-)", c.Tmux.Prefix)
	}
}

func TestLoad_defaultsWhenNoFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Pool.MaxSessions != 6 {
		t.Errorf("MaxSessions = %d, want 6", c.Pool.MaxSessions)
	}
	if c.Tmux.Socket != "ccpool" {
		t.Errorf("Tmux.Socket = %q, want ccpool", c.Tmux.Socket)
	}
	if c.Tmux.Prefix != "cc-" {
		t.Errorf("Tmux.Prefix = %q, want cc-", c.Tmux.Prefix)
	}
	if c.List.DoneTTL != Duration(time.Hour) {
		t.Errorf("List.DoneTTL = %v, want 1h", c.List.DoneTTL)
	}
	if want := filepath.Join(dir, "data", "ccpool", "store.db"); c.DBPath != want {
		t.Errorf("DBPath = %q, want %q", c.DBPath, want)
	}
}

func TestLoad_claudeBinDefaultsToClaude(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Claude.Bin != "claude" {
		t.Errorf("Claude.Bin = %q, want claude", c.Claude.Bin)
	}
}

func TestLoad_notifyDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Notify.Adapter != "desktop" {
		t.Errorf("Notify.Adapter = %q, want desktop", c.Notify.Adapter)
	}
	if len(c.Notify.On) != 2 || c.Notify.On[0] != "needs_input" || c.Notify.On[1] != "failed" {
		t.Errorf("Notify.On = %v, want [needs_input failed]", c.Notify.On)
	}
}

func TestLoad_readsTOMLOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "cfg", "ccpool")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `
[pool]
max_sessions = 3
[tmux]
socket = "ccpooltest"
[list]
done_ttl = "15m"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Pool.MaxSessions != 3 {
		t.Errorf("MaxSessions = %d, want 3", c.Pool.MaxSessions)
	}
	if c.Tmux.Socket != "ccpooltest" {
		t.Errorf("Tmux.Socket = %q, want ccpooltest", c.Tmux.Socket)
	}
	if c.List.DoneTTL != Duration(15*time.Minute) {
		t.Errorf("DoneTTL = %v, want 15m", c.List.DoneTTL)
	}
}
