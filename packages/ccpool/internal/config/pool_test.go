package config

import (
	"path/filepath"
	"testing"
)

func TestResolvePool_defaultMode(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	pc, err := ResolvePool("")
	if err != nil {
		t.Fatalf("ResolvePool: %v", err)
	}
	if pc.Root != "" {
		t.Errorf("default mode Root = %q, want empty", pc.Root)
	}
	if pc.DBPath != filepath.Join(home, ".local/share", "ccpool", "store.db") {
		t.Errorf("DBPath = %q", pc.DBPath)
	}
	if pc.StateDir != filepath.Join(home, ".local/state", "ccpool") {
		t.Errorf("StateDir = %q", pc.StateDir)
	}
	if pc.Socket != "" {
		t.Errorf("default mode Socket = %q, want empty (config.toml supplies it)", pc.Socket)
	}
}
