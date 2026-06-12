package config

import (
	"path/filepath"
	"testing"
)

func TestSocketFor(t *testing.T) {
	a := SocketFor("/Users/x/pools/alpha")
	b := SocketFor("/Users/x/pools/beta")
	if a == b {
		t.Error("distinct paths must yield distinct sockets")
	}
	if a != SocketFor("/Users/x/pools/alpha") {
		t.Error("SocketFor must be deterministic")
	}
	deep := SocketFor("/Users/x/" + filepath.Join(makeLong(40)...))
	if len(deep) != len("cc-")+16 {
		t.Errorf("socket basename = %d chars, want 19 regardless of path depth", len(deep))
	}
}

func makeLong(n int) []string {
	s := make([]string, n)
	for i := range s {
		s[i] = "deeply-nested-segment"
	}
	return s
}

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
