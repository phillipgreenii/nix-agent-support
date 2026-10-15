package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// DirName is the state directory's final path element.
const DirName = "pg-keep-awake"

// ErrStateDir matches (via errors.Is) every *StateDirError. Callers map it to
// exit status 3.
var ErrStateDir = errors.New("pg-keep-awake state directory unusable")

// StateDirError says why a state directory was refused. It is never produced
// for a missing directory by CheckDir (that is fs.ErrNotExist).
type StateDirError struct {
	Path   string
	Reason string
}

func (e *StateDirError) Error() string {
	return fmt.Sprintf("state directory %s: %s", e.Path, e.Reason)
}

// Is reports that every StateDirError matches ErrStateDir.
func (e *StateDirError) Is(target error) bool { return target == ErrStateDir }

// ResolveStateDir returns ${XDG_STATE_HOME:-~/.local/state}/pg-keep-awake
// from the process environment.
func ResolveStateDir() (string, error) {
	home, _ := os.UserHomeDir()
	return ResolveStateDirFrom(os.Getenv("XDG_STATE_HOME"), home)
}

// ResolveStateDirFrom is ResolveStateDir with the inputs supplied. Per the XDG
// spec a relative xdg value is invalid and ignored.
func ResolveStateDirFrom(xdgStateHome, home string) (string, error) {
	if xdgStateHome != "" && filepath.IsAbs(xdgStateHome) {
		return filepath.Join(xdgStateHome, DirName), nil
	}
	if home == "" {
		return "", errors.New("cannot resolve state directory: XDG_STATE_HOME unset and no home directory")
	}
	return filepath.Join(home, ".local", "state", DirName), nil
}

// Dir is a checked state directory.
type Dir struct {
	path string
}

// Path is the state directory's absolute path.
func (d *Dir) Path() string { return d.path }

// LeasesPath is the leases/ subdirectory.
func (d *Dir) LeasesPath() string { return filepath.Join(d.path, "leases") }

// EnsureDir creates the state directory (mode 0700) and its leases/
// subdirectory when missing, and checks ownership and permissions of whatever
// exists. An existing directory that is not owned by the current user or has
// group/other bits is refused with a *StateDirError and left untouched.
func EnsureDir(path string) (*Dir, error) {
	if err := ensureOne(path, true); err != nil {
		return nil, err
	}
	d := &Dir{path: path}
	if err := ensureOne(d.LeasesPath(), true); err != nil {
		return nil, err
	}
	return d, nil
}

// CheckDir is the read-only variant of EnsureDir: it creates nothing. A
// missing state directory is fs.ErrNotExist; a missing leases/ is tolerated.
func CheckDir(path string) (*Dir, error) {
	if err := ensureOne(path, false); err != nil {
		return nil, err
	}
	d := &Dir{path: path}
	if err := ensureOne(d.LeasesPath(), false); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return d, nil
}

func ensureOne(path string, create bool) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if !create {
			return err
		}
		return makeOwned(path)
	}
	if err != nil {
		return &StateDirError{Path: path, Reason: err.Error()}
	}
	return checkOwned(path, fi)
}

func makeOwned(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return &StateDirError{Path: path, Reason: "cannot create parent: " + err.Error()}
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Lost a creation race; judge what is there.
			fi, lerr := os.Lstat(path)
			if lerr != nil {
				return &StateDirError{Path: path, Reason: lerr.Error()}
			}
			return checkOwned(path, fi)
		}
		return &StateDirError{Path: path, Reason: "cannot create: " + err.Error()}
	}
	// Mkdir honours the umask; this directory is ours, so make the mode exact.
	if err := os.Chmod(path, 0o700); err != nil {
		return &StateDirError{Path: path, Reason: "cannot set mode 0700: " + err.Error()}
	}
	return nil
}

func checkOwned(path string, fi fs.FileInfo) error {
	if !fi.IsDir() {
		return &StateDirError{Path: path, Reason: "exists and is not a directory (symlinks are refused)"}
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return &StateDirError{Path: path, Reason: "cannot determine owner"}
	}
	if int(st.Uid) != os.Getuid() {
		return &StateDirError{Path: path, Reason: fmt.Sprintf("owned by uid %d, not the current user (uid %d)", st.Uid, os.Getuid())}
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return &StateDirError{Path: path, Reason: fmt.Sprintf("mode %04o grants group or other access; it must be 0700", perm)}
	}
	return nil
}
