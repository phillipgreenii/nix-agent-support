package lease

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveStateDirFrom(t *testing.T) {
	cases := []struct{ xdg, home, want string }{
		{"/x/state", "/h", "/x/state/pg-keep-awake"},
		{"", "/h", "/h/.local/state/pg-keep-awake"},
		{"relative/state", "/h", "/h/.local/state/pg-keep-awake"}, // relative XDG is ignored
	}
	for _, c := range cases {
		got, err := ResolveStateDirFrom(c.xdg, c.home)
		if err != nil || got != c.want {
			t.Errorf("ResolveStateDirFrom(%q,%q) = %q, %v; want %q", c.xdg, c.home, got, err, c.want)
		}
	}
	if _, err := ResolveStateDirFrom("", ""); err == nil {
		t.Error("no XDG and no home must be an error")
	}
}

func TestEnsureDirCreatesPrivateDirs(t *testing.T) {
	d := newDir(t)
	for _, p := range []string{d.Path(), d.LeasesPath()} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", p, fi.Mode().Perm())
		}
	}
	// Idempotent on an existing good directory.
	if _, err := EnsureDir(d.Path()); err != nil {
		t.Errorf("second EnsureDir: %v", err)
	}
}

func TestEnsureDirModeIsUmaskProof(t *testing.T) {
	old := setUmask(0)
	defer setUmask(old)
	d := newDir(t)
	fi, _ := os.Stat(d.Path())
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode under umask 0 = %04o, want 0700", fi.Mode().Perm())
	}
}

func TestEnsureDirRefusesPermissiveAndDoesNotChmod(t *testing.T) {
	path := filepath.Join(t.TempDir(), DirName)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureDir(path)
	var sde *StateDirError
	if !errors.As(err, &sde) || !errors.Is(err, ErrStateDir) {
		t.Fatalf("want *StateDirError matching ErrStateDir, got %v", err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("EnsureDir chmod-ed a directory it refused: %04o", fi.Mode().Perm())
	}
	if _, err := CheckDir(path); !errors.Is(err, ErrStateDir) {
		t.Errorf("CheckDir permissive: %v", err)
	}
}

func TestEnsureDirRefusesSymlinkAndFile(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDir(link); !errors.Is(err, ErrStateDir) {
		t.Errorf("symlinked state dir must be refused, got %v", err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDir(file); !errors.Is(err, ErrStateDir) {
		t.Errorf("file as state dir must be refused, got %v", err)
	}
}

func TestEnsureDirRefusesPermissiveLeasesSubdir(t *testing.T) {
	d := newDir(t)
	if err := os.Chmod(d.LeasesPath(), 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDir(d.Path()); !errors.Is(err, ErrStateDir) {
		t.Errorf("permissive leases/ must be refused, got %v", err)
	}
}

func TestEnsureDirRefusesForeignOwner(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root owns everything it can reach")
	}
	// /private/var (macOS) and /var, /usr are root-owned and not ours.
	for _, p := range []string{"/usr", "/var", "/"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if _, err := EnsureDir(p); !errors.Is(err, ErrStateDir) {
			t.Errorf("EnsureDir(%s) = %v, want ErrStateDir", p, err)
		}
	}
}

func TestCheckDirMissingIsNotExist(t *testing.T) {
	_, err := CheckDir(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("CheckDir missing = %v, want ErrNotExist", err)
	}
}
