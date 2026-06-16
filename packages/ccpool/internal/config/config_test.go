package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/registry"
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
	if c.EventLogPath() != filepath.Join(poolCanon, "events.jsonl") {
		t.Errorf("EventLogPath = %q, want events.jsonl beside hook.log", c.EventLogPath())
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

// TestLoad_autoReapDefaultsTrue: a pool with no (or no [pool].auto_reap) config is
// governed by reap-all — auto_reap defaults to true.
func TestLoad_autoReapDefaultsTrue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Pool.AutoReap {
		t.Error("Pool.AutoReap must default to true")
	}
}

// TestLoad_autoReapOptOut: auto_reap = false opts the pool out of reap-all.
func TestLoad_autoReapOptOut(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "cfg", "ccpool")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[pool]\nauto_reap = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Pool.AutoReap {
		t.Error("Pool.AutoReap must be false when config sets auto_reap = false")
	}
}

// TestLoadForPool_namedPool: LoadForPool reads a specific pool's config WITHOUT
// consulting CCPOOL_POOL — this is the seam reap-all uses to load each registered
// pool's config in-process (no per-iteration os.Setenv).
func TestLoadForPool_namedPool(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "/some/other/pool") // must be ignored by LoadForPool
	t.Setenv("CCPOOL_REGISTRY_DIR", t.TempDir())
	pool := t.TempDir()
	poolCanon, _ := filepath.EvalSymlinks(pool)
	if err := os.WriteFile(filepath.Join(pool, "config.toml"), []byte("[pool]\nauto_reap = false\nmax_sessions = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadForPool(pool)
	if err != nil {
		t.Fatalf("LoadForPool: %v", err)
	}
	if c.PoolRoot != poolCanon {
		t.Errorf("PoolRoot = %q, want %q (not CCPOOL_POOL)", c.PoolRoot, poolCanon)
	}
	if c.Pool.AutoReap {
		t.Error("auto_reap=false must be parsed")
	}
	if c.Pool.MaxSessions != 2 {
		t.Errorf("MaxSessions = %d, want 2", c.Pool.MaxSessions)
	}
	if c.Tmux.Socket != SocketFor(poolCanon) {
		t.Errorf("Socket = %q, want derived %q", c.Tmux.Socket, SocketFor(poolCanon))
	}
}

// TestLoadForPool_emptyIsDefault: LoadForPool("") loads the default (XDG) pool,
// which is exactly how reap-all reaps the default pool regardless of env.
func TestLoadForPool_emptyIsDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPOOL_POOL", "/ignored")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	c, err := LoadForPool("")
	if err != nil {
		t.Fatalf("LoadForPool(\"\"): %v", err)
	}
	if c.PoolRoot != "" {
		t.Errorf("PoolRoot = %q, want empty (default mode)", c.PoolRoot)
	}
	if c.Tmux.Socket != "ccpool" {
		t.Errorf("Socket = %q, want ccpool (default)", c.Tmux.Socket)
	}
}

// TestLoadForPool_doesNotRegister: loading a pool's config is a read; it must not
// enroll the pool in the registry.
func TestLoadForPool_doesNotRegister(t *testing.T) {
	t.Setenv("CCPOOL_REGISTRY_DIR", t.TempDir())
	pool := t.TempDir()
	if _, err := LoadForPool(pool); err != nil {
		t.Fatalf("LoadForPool: %v", err)
	}
	entries, err := registry.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("LoadForPool must not register, got %d entries", len(entries))
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

// TestLoad_retryDefaults pins the [retry] block defaults (transient-retry design).
func TestLoad_retryDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Retry.Enabled {
		t.Error("Retry.Enabled must default to true")
	}
	if c.Retry.MaxAttempts != 3 {
		t.Errorf("Retry.MaxAttempts = %d, want 3", c.Retry.MaxAttempts)
	}
	if c.Retry.BaseDelay != Duration(time.Second) {
		t.Errorf("Retry.BaseDelay = %v, want 1s", time.Duration(c.Retry.BaseDelay))
	}
	if c.Retry.Timeout != Duration(60*time.Second) {
		t.Errorf("Retry.Timeout = %v, want 60s", time.Duration(c.Retry.Timeout))
	}
	if len(c.Retry.Classes) != 2 || c.Retry.Classes[0] != "transient_server" || c.Retry.Classes[1] != "transient_network" {
		t.Errorf("Retry.Classes = %v, want [transient_server transient_network]", c.Retry.Classes)
	}
}

// TestLoad_retryTOMLOverrides confirms the [retry] block decodes over defaults,
// including the replace-list semantics for classes.
func TestLoad_retryTOMLOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "cfg", "ccpool")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `
[retry]
enabled = false
max_attempts = 5
base_delay = "2s"
timeout = "90s"
classes = ["transient_network"]
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
	if c.Retry.Enabled {
		t.Error("Retry.Enabled = true, want false (config override)")
	}
	if c.Retry.MaxAttempts != 5 {
		t.Errorf("Retry.MaxAttempts = %d, want 5", c.Retry.MaxAttempts)
	}
	if c.Retry.BaseDelay != Duration(2*time.Second) {
		t.Errorf("Retry.BaseDelay = %v, want 2s", time.Duration(c.Retry.BaseDelay))
	}
	if c.Retry.Timeout != Duration(90*time.Second) {
		t.Errorf("Retry.Timeout = %v, want 90s", time.Duration(c.Retry.Timeout))
	}
	if len(c.Retry.Classes) != 1 || c.Retry.Classes[0] != "transient_network" {
		t.Errorf("Retry.Classes = %v, want [transient_network] (replace-list)", c.Retry.Classes)
	}
}
