package lease

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Size limits.
const (
	// MaxLeaseFileBytes is the largest lease file a reader accepts (64 KiB).
	MaxLeaseFileBytes = 64 << 10
	// MaxStateFileBytes bounds the other state files; status.json lists every
	// lease so it is allowed more room than one lease file.
	MaxStateFileBytes = 4 << 20
)

// Errors a safe read can report instead of returning contents.
var (
	// ErrNotRegular: the path is a symlink, FIFO, directory or other
	// non-regular file.
	ErrNotRegular = errors.New("not a regular file")
	// ErrTooLarge: the file exceeds the size cap.
	ErrTooLarge = errors.New("file too large")
)

// fileMeta is what a safe read learned about the file it opened.
type fileMeta struct {
	ModTimeUnixNano int64
}

// readSafe opens path with O_RDONLY|O_NOFOLLOW|O_NONBLOCK, requires a regular
// file by fstat, and refuses contents over max bytes. O_NONBLOCK keeps a FIFO
// from blocking the open; the fstat then rejects it.
func readSafe(path string, max int64) ([]byte, fileMeta, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fileMeta{}, &fs.PathError{Op: "open", Path: path, Err: ErrNotRegular}
		}
		return nil, fileMeta{}, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fileMeta{}, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fileMeta{}, &fs.PathError{Op: "read", Path: path, Err: ErrNotRegular}
	}
	if fi.Size() > max {
		return nil, fileMeta{}, &fs.PathError{Op: "read", Path: path, Err: ErrTooLarge}
	}
	// Read one past the cap so a file that grew after fstat is still caught.
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fileMeta{}, err
	}
	if int64(len(data)) > max {
		return nil, fileMeta{}, &fs.PathError{Op: "read", Path: path, Err: ErrTooLarge}
	}
	return data, fileMeta{ModTimeUnixNano: fi.ModTime().UnixNano()}, nil
}

// decodeVersioned unmarshals data into v, first checking schema_version: a
// version greater than SchemaVersion is *UnreadableError and v is untouched.
// Unknown fields are ignored (encoding/json default).
func decodeVersioned(path string, data []byte, v any) error {
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if head.SchemaVersion > SchemaVersion {
		return &UnreadableError{Path: path, Version: head.SchemaVersion}
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// WriteAtomic writes data to rel (a slash-separated path under the state
// directory, such as "status.json" or "leases/x.json") by creating a mode 0600
// temp file ".<name>.<pid>.<random>.tmp" in the same directory and
// rename(2)-ing it into place, so a reader never sees a partial file. It is
// the one writer for every non-lease state file; callers of lease files hold
// leases/.lock around it.
func (d *Dir) WriteAtomic(rel string, data []byte) error {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("state file path %q escapes the state directory", rel)
	}
	final := filepath.Join(d.path, clean)
	dir, base := filepath.Split(final)
	tmp := filepath.Join(dir, "."+strings.TrimSuffix(base, ".json")+"."+strconv.Itoa(os.Getpid())+"."+
		strconv.FormatUint(rand.Uint64(), 36)+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(tmp) }
	// The umask can only remove bits, but make the mode exact regardless.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		cleanup()
		return err
	}
	return nil
}

// ReadFile reads a non-lease state file safely (no symlinks, regular files
// only, MaxStateFileBytes). A missing file is fs.ErrNotExist.
func (d *Dir) ReadFile(rel string) ([]byte, error) {
	data, _, err := readSafe(filepath.Join(d.path, filepath.FromSlash(rel)), MaxStateFileBytes)
	return data, err
}

// Remove deletes a state file; a missing file is not an error.
func (d *Dir) Remove(rel string) error {
	err := os.Remove(filepath.Join(d.path, filepath.FromSlash(rel)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func readJSONFile[T any](d *Dir, name string) (*T, error) {
	path := filepath.Join(d.path, name)
	data, _, err := readSafe(path, MaxStateFileBytes)
	if err != nil {
		return nil, err
	}
	v := new(T)
	if err := decodeVersioned(path, data, v); err != nil {
		return nil, err
	}
	return v, nil
}

func writeJSONFile(d *Dir, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return d.WriteAtomic(name, append(data, '\n'))
}

// ReadAssertion reads assertion.json. Errors: fs.ErrNotExist when absent,
// *UnreadableError for a newer schema_version.
func (d *Dir) ReadAssertion() (*AssertionFile, error) {
	return readJSONFile[AssertionFile](d, FileAssertion)
}

// WriteAssertion atomically writes assertion.json.
func (d *Dir) WriteAssertion(a *AssertionFile) error {
	c := *a
	c.SchemaVersion = SchemaVersion
	return writeJSONFile(d, FileAssertion, &c)
}

// ReadStatus reads status.json (see ReadAssertion for the errors).
func (d *Dir) ReadStatus() (*StatusFile, error) { return readJSONFile[StatusFile](d, FileStatus) }

// WriteStatus atomically writes status.json.
func (d *Dir) WriteStatus(s *StatusFile) error {
	c := *s
	c.SchemaVersion = SchemaVersion
	return writeJSONFile(d, FileStatus, &c)
}

// ReadPause reads pause.json (see ReadAssertion for the errors).
func (d *Dir) ReadPause() (*PauseFile, error) { return readJSONFile[PauseFile](d, FilePause) }

// WritePause atomically writes pause.json.
func (d *Dir) WritePause(p *PauseFile) error {
	c := *p
	c.SchemaVersion = SchemaVersion
	return writeJSONFile(d, FilePause, &c)
}

// RemovePause deletes pause.json; absent is not an error.
func (d *Dir) RemovePause() error { return d.Remove(FilePause) }

// ReadAnchorExit reads anchor-exit.json (see ReadAssertion for the errors).
func (d *Dir) ReadAnchorExit() (*AnchorExitFile, error) {
	return readJSONFile[AnchorExitFile](d, FileAnchorExit)
}

// WriteAnchorExit atomically writes anchor-exit.json.
func (d *Dir) WriteAnchorExit(a *AnchorExitFile) error {
	c := *a
	c.SchemaVersion = SchemaVersion
	return writeJSONFile(d, FileAnchorExit, &c)
}

// RemoveAnchorExit deletes anchor-exit.json; absent is not an error.
func (d *Dir) RemoveAnchorExit() error { return d.Remove(FileAnchorExit) }
