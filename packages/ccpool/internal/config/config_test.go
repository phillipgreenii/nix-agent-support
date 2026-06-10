package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
