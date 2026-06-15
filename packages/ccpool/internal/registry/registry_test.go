package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDir_honorsEnvAndCreates(t *testing.T) {
	want := filepath.Join(t.TempDir(), "reg")
	t.Setenv("CCPOOL_REGISTRY_DIR", want)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	info, err := os.Stat(want)
	if err != nil || !info.IsDir() {
		t.Fatalf("Dir must create the directory: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("registry dir mode = %o, want 0700", info.Mode().Perm())
	}
}

func TestDir_defaultsToXDGStateHome(t *testing.T) {
	t.Setenv("CCPOOL_REGISTRY_DIR", "")
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join(state, "ccpool", "pools.d"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

func TestEnsure_createsAndList(t *testing.T) {
	reg := t.TempDir()
	t.Setenv("CCPOOL_REGISTRY_DIR", reg)
	target := t.TempDir()
	if err := Ensure("cc-abc", target); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	entries, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List len = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Name != "cc-abc" {
		t.Errorf("Name = %q, want cc-abc", e.Name)
	}
	if e.Target != target {
		t.Errorf("Target = %q, want %q", e.Target, target)
	}
	if e.Link != filepath.Join(reg, "cc-abc") {
		t.Errorf("Link = %q, want %q", e.Link, filepath.Join(reg, "cc-abc"))
	}
}

func TestEnsure_idempotent(t *testing.T) {
	reg := t.TempDir()
	t.Setenv("CCPOOL_REGISTRY_DIR", reg)
	target := t.TempDir()
	if err := Ensure("cc-x", target); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	if err := Ensure("cc-x", target); err != nil {
		t.Fatalf("second Ensure (same target) must succeed: %v", err)
	}
	entries, _ := List()
	if len(entries) != 1 {
		t.Fatalf("idempotent Ensure must leave exactly one link, got %d", len(entries))
	}
	if entries[0].Target != target {
		t.Errorf("Target = %q, want %q", entries[0].Target, target)
	}
}

func TestEnsure_repairsWrongTarget(t *testing.T) {
	reg := t.TempDir()
	t.Setenv("CCPOOL_REGISTRY_DIR", reg)
	a := t.TempDir()
	b := t.TempDir()
	if err := Ensure("cc-y", a); err != nil {
		t.Fatalf("Ensure a: %v", err)
	}
	if err := Ensure("cc-y", b); err != nil {
		t.Fatalf("Ensure b (repair): %v", err)
	}
	entries, _ := List()
	if len(entries) != 1 {
		t.Fatalf("repair must leave one link, got %d", len(entries))
	}
	if entries[0].Target != b {
		t.Errorf("Target = %q, want repaired %q", entries[0].Target, b)
	}
}

func TestRemove_toleratesENOENT(t *testing.T) {
	reg := t.TempDir()
	t.Setenv("CCPOOL_REGISTRY_DIR", reg)
	// removing a name that was never registered must NOT error
	if err := Remove("cc-ghost"); err != nil {
		t.Errorf("Remove of missing name must tolerate ENOENT: %v", err)
	}
	target := t.TempDir()
	_ = Ensure("cc-z", target)
	if err := Remove("cc-z"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	entries, _ := List()
	if len(entries) != 0 {
		t.Errorf("after Remove, List len = %d, want 0", len(entries))
	}
}

// TestList_emptyWhenNoDir: List on a never-created registry returns empty, not an error.
func TestList_emptyWhenNoDir(t *testing.T) {
	t.Setenv("CCPOOL_REGISTRY_DIR", filepath.Join(t.TempDir(), "never"))
	entries, err := List()
	if err != nil {
		t.Fatalf("List on fresh registry must not error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("fresh registry List len = %d, want 0", len(entries))
	}
}
