package rundir

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

var fixed = time.Date(2026, 10, 2, 14, 3, 11, 0, time.UTC)

func clock() time.Time { return fixed }

func TestNewIDFormat(t *testing.T) {
	id, err := NewID(fixed, bytes.NewReader([]byte{0x7f, 0x3a, 0x9c, 0x2e}))
	if err != nil {
		t.Fatal(err)
	}
	if id != "20261002T140311Z-7f3a9c2e" {
		t.Errorf("id = %q", id)
	}
	if !IDPattern.MatchString(id) {
		t.Errorf("id %q does not match IDPattern", id)
	}
}

func TestNewIDConvertsToUTC(t *testing.T) {
	zone := time.FixedZone("x", 5*3600)
	id, err := NewID(fixed.In(zone), bytes.NewReader(make([]byte, 4)))
	if err != nil || id != "20261002T140311Z-00000000" {
		t.Errorf("id = %q, err = %v", id, err)
	}
}

func TestNewIDShortRandomSourceIsAnError(t *testing.T) {
	if _, err := NewID(fixed, bytes.NewReader([]byte{1, 2})); err == nil {
		t.Error("expected an error from a short random source")
	}
}

func TestStateRoot(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if got, want := StateRoot(get, "/h"), "/h/.local/state/pg-rescue"; got != want {
		t.Errorf("default = %q want %q", got, want)
	}
	env["XDG_STATE_HOME"] = ""
	if got, want := StateRoot(get, "/h"), "/h/.local/state/pg-rescue"; got != want {
		t.Errorf("empty XDG = %q want %q", got, want)
	}
	env["XDG_STATE_HOME"] = "/x"
	if got, want := StateRoot(get, "/h"), "/x/pg-rescue"; got != want {
		t.Errorf("XDG = %q want %q", got, want)
	}
}

func TestCreateModesHoldUnderPermissiveUmask(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	root := filepath.Join(t.TempDir(), "state", "pg-rescue")
	id, dir, err := Create(root, clock, bytes.NewReader([]byte{1, 2, 3, 4}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "runs", id); dir != want {
		t.Errorf("dir = %q want %q", dir, want)
	}
	for _, d := range []string{root, filepath.Join(root, "runs"), dir} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %04o; want 0700", d, fi.Mode().Perm())
		}
	}
}

func TestCreateRetriesOnCollision(t *testing.T) {
	root := t.TempDir()
	id1, _, err := Create(root, clock, bytes.NewReader([]byte{1, 2, 3, 4}))
	if err != nil {
		t.Fatal(err)
	}
	// The first id drawn here collides with the existing directory.
	id2, _, err := Create(root, clock, bytes.NewReader([]byte{1, 2, 3, 4, 9, 9, 9, 9}))
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("collision not retried: both %q", id1)
	}
	if id2 != "20261002T140311Z-09090909" {
		t.Errorf("id2 = %q", id2)
	}
}

func TestCreateGivesUpAfterRepeatedCollisions(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Create(root, clock, bytes.NewReader([]byte{1, 1, 1, 1})); err != nil {
		t.Fatal(err)
	}
	same := bytes.Repeat([]byte{1, 1, 1, 1}, 64)
	if _, _, err := Create(root, clock, bytes.NewReader(same)); err == nil {
		t.Error("expected failure when every id collides")
	}
}

func TestCreateFailsWhenStateRootIsAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create(filepath.Join(f, "pg-rescue"), clock, bytes.NewReader(make([]byte, 4))); err == nil {
		t.Error("expected an error when the state root cannot be created")
	}
}
