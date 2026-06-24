package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/registry"
)

// TestResolvePool_registersOnCreate: the moment a named pool dir is first created,
// a registry symlink (named by the pool's socket hash, pointing at the canonical
// root) is written exactly once.
func TestResolvePool_registersOnCreate(t *testing.T) {
	t.Setenv("CCPOOL_REGISTRY_DIR", t.TempDir())
	parent := t.TempDir()
	leaf := filepath.Join(parent, "newpool")

	pc, err := ResolvePool(leaf)
	if err != nil {
		t.Fatalf("ResolvePool: %v", err)
	}
	entries, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("creating a named pool must register exactly one symlink, got %d", len(entries))
	}
	if entries[0].Name != SocketFor(pc.Root) {
		t.Errorf("symlink name = %q, want SocketFor(root) %q", entries[0].Name, SocketFor(pc.Root))
	}
	if entries[0].Target != pc.Root {
		t.Errorf("symlink target = %q, want canonical root %q", entries[0].Target, pc.Root)
	}
}

// TestResolvePool_existingPoolDoesNotRegister: resolving an ALREADY-existing pool
// takes the validate branch and must not write to the registry (read-verb purity —
// `--pool P list`/`state`/`doctor` never enroll P in auto-reaping).
func TestResolvePool_existingPoolDoesNotRegister(t *testing.T) {
	t.Setenv("CCPOOL_REGISTRY_DIR", t.TempDir())
	pool := t.TempDir() // already exists
	if _, err := ResolvePool(pool); err != nil {
		t.Fatalf("ResolvePool: %v", err)
	}
	entries, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("resolving an existing pool must NOT register, got %d entries", len(entries))
	}
}

// TestResolvePool_defaultNeverRegisters: the default (XDG) pool early-returns before
// ensurePoolDir, so it can never self-register — this is what keeps reap-all's
// separate default-pool reap from double-reaping.
func TestResolvePool_defaultNeverRegisters(t *testing.T) {
	t.Setenv("CCPOOL_REGISTRY_DIR", t.TempDir())
	if _, err := ResolvePool(""); err != nil {
		t.Fatalf("ResolvePool(\"\"): %v", err)
	}
	entries, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("default pool must never self-register, got %d entries", len(entries))
	}
}

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

func TestEnsurePoolDir_allowlist(t *testing.T) {
	dir := t.TempDir()
	// allowlist-only contents → OK
	for _, f := range []string{"config.toml", "store.db", "store.db-wal", "store.db-shm", "store.db-journal", "alpha.lock", "beta.lock", "hook.log", "events.jsonl", "diagnostics.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensurePoolDir(dir); err != nil {
		t.Fatalf("allowlist-only dir must be accepted: %v", err)
	}
	// a foreign file → refuse
	foreign := filepath.Join(dir, "README.md")
	_ = os.WriteFile(foreign, nil, 0o600)
	if err := ensurePoolDir(dir); err == nil {
		t.Error("foreign file must be refused")
	}
	_ = os.Remove(foreign)
	// a foreign subdirectory → refuse
	_ = os.Mkdir(filepath.Join(dir, "src"), 0o700)
	if err := ensurePoolDir(dir); err == nil {
		t.Error("foreign subdirectory must be refused")
	}
}

func TestEnsurePoolDir_create(t *testing.T) {
	parent := t.TempDir()
	leaf := filepath.Join(parent, "newpool")
	if err := ensurePoolDir(leaf); err != nil {
		t.Fatalf("leaf with existing parent must be created: %v", err)
	}
	info, err := os.Stat(leaf)
	if err != nil || !info.IsDir() {
		t.Fatalf("leaf not created: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("leaf mode = %o, want 0700", info.Mode().Perm())
	}
	// missing parent → error (no mkdir -p)
	if err := ensurePoolDir(filepath.Join(parent, "missing", "deep")); err == nil {
		t.Error("missing parent must error (no mkdir -p)")
	}
}

// TestValidatePoolDir covers the read-only validator GC calls: it must NOT create
// the dir, must reject a missing dir (dangling target) and foreign content, and must
// accept an allowlist-only dir.
func TestValidatePoolDir(t *testing.T) {
	parent := t.TempDir()
	// non-existent dir → invalid (dangling), and must NOT be created
	missing := filepath.Join(parent, "nope")
	if err := ValidatePoolDir(missing); err == nil {
		t.Error("non-existent dir must be invalid")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("ValidatePoolDir must not create the dir")
	}
	// allowlist-only → ok
	dir := t.TempDir()
	for _, f := range []string{"config.toml", "store.db", "store.db-wal", "alpha.lock", "hook.log", "events.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidatePoolDir(dir); err != nil {
		t.Errorf("allowlist-only dir must validate: %v", err)
	}
	// a foreign file → refuse
	if err := os.WriteFile(filepath.Join(dir, "README.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePoolDir(dir); err == nil {
		t.Error("foreign file must be refused")
	}
}

func TestResolvePool_canonicalIdentity(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	parent := t.TempDir()
	real := filepath.Join(parent, "pool")
	_ = os.Mkdir(real, 0o700)
	link := filepath.Join(parent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	a, _ := ResolvePool(real)
	b, _ := ResolvePool(link)       // via symlink
	c, _ := ResolvePool(real + "/") // trailing slash
	if a.Root != b.Root || a.Root != c.Root {
		t.Errorf("same dir mapped to different roots: %q %q %q", a.Root, b.Root, c.Root)
	}
	if a.Socket != b.Socket {
		t.Errorf("same dir mapped to different sockets: %q %q", a.Socket, b.Socket)
	}
}
