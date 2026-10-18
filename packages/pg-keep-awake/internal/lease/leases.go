package lease

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Scan and cleanup bounds.
const (
	// MaxLeaseFiles is how many lease files one scan considers; the rest are
	// counted as rejected.
	MaxLeaseFiles = 1024
	// StaleAfter is how old an unparsable *.json or a *.tmp file must be
	// before a sweep may delete it.
	StaleAfter = 60 * time.Second
)

func (d *Dir) leaseFile(id string) string { return filepath.Join(d.LeasesPath(), id+".json") }

// PutLease creates or refreshes a lease under leases/.lock. The lease is
// validated first (ID and SchemaVersion are filled; CreatedAt defaults to
// now). Failure to get the lock within LockTimeout is ErrLockTimeout.
func (d *Dir) PutLease(l *Lease) error {
	lk, err := d.TakeLock()
	if err != nil {
		return err
	}
	defer func() { _ = lk.Unlock() }()
	return lk.Put(l)
}

// RemoveLease removes lease id under leases/.lock; a missing lease is fine.
func (d *Dir) RemoveLease(id string) error {
	lk, err := d.TakeLock()
	if err != nil {
		return err
	}
	defer func() { _ = lk.Unlock() }()
	return lk.Remove(id)
}

// Put validates and writes l (temp file, rename) while the lock is held.
func (lk *Lock) Put(l *Lease) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now().UTC()
	}
	if err := l.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return lk.d.WriteAtomic("leases/"+l.ID+".json", append(data, '\n'))
}

// Remove unlinks lease id while the lock is held; a missing lease is fine.
func (lk *Lock) Remove(id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	err := os.Remove(lk.d.leaseFile(id))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ReadLease reads one lease by id with the safe-open rules. Errors:
// fs.ErrNotExist, *UnreadableError for a newer schema_version, or a plain
// error for an unparsable or invalid file.
func (d *Dir) ReadLease(id string) (*Lease, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	path := d.leaseFile(id)
	l, _, err := parseLeaseFile(path, id+".json")
	return l, err
}

func parseLeaseFile(path, name string) (*Lease, fileMeta, error) {
	data, meta, err := readSafe(path, MaxLeaseFileBytes)
	if err != nil {
		return nil, meta, err
	}
	l := new(Lease)
	if err := decodeVersioned(path, data, l); err != nil {
		return nil, meta, err
	}
	if err := l.Validate(); err != nil {
		return nil, meta, fmt.Errorf("%s: %w", path, err)
	}
	if l.ID+".json" != name {
		return nil, meta, fmt.Errorf("%s: lease id %q does not match the file name", path, l.ID)
	}
	return l, meta, nil
}

// JunkFile is an unparsable (or invalid) *.json lease file. It MUST NOT be
// deleted until its mtime is more than StaleAfter old.
type JunkFile struct {
	Name    string
	ModTime time.Time
}

// Stale reports whether the file may now be deleted.
func (j JunkFile) Stale(now time.Time) bool { return now.Sub(j.ModTime) > StaleAfter }

// ScanResult is one directory scan of leases/.
type ScanResult struct {
	// Leases are the valid leases, in file-name order.
	Leases []*Lease
	// Unreadable names files whose schema_version is newer than this reader's;
	// `status` shows them as unreadable and nothing deletes them.
	Unreadable []string
	// Unparsable are the *.json files that failed to parse or validate; they
	// are included in Rejected.
	Unparsable []JunkFile
	// Rejected counts every *.json file that was skipped: unparsable, invalid,
	// symlink, FIFO or other non-regular, over 64 KiB, or past MaxLeaseFiles.
	// Future-schema files are NOT counted here (see Unreadable).
	Rejected int
}

// ListLeases scans leases/, reading only *.json and ignoring names that start
// with a dot or end in .tmp. It needs no lock: writers publish by rename.
func (d *Dir) ListLeases() (*ScanResult, error) {
	entries, err := os.ReadDir(d.LeasesPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &ScanResult{}, nil
		}
		return nil, err
	}
	res := &ScanResult{}
	considered := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if considered >= MaxLeaseFiles {
			res.Rejected++
			continue
		}
		considered++
		l, meta, err := parseLeaseFile(filepath.Join(d.LeasesPath(), name), name)
		switch {
		case err == nil:
			res.Leases = append(res.Leases, l)
		case errors.Is(err, ErrUnreadable):
			res.Unreadable = append(res.Unreadable, name)
		case errors.Is(err, fs.ErrNotExist):
			// Removed between the directory read and the open.
			considered--
		case errors.Is(err, ErrNotRegular), errors.Is(err, ErrTooLarge):
			res.Rejected++
		default:
			var pe *fs.PathError
			if errors.As(err, &pe) && pe.Op == "open" {
				// Could not even open it (permissions, ...): rejected, not junk.
				res.Rejected++
				break
			}
			res.Rejected++
			res.Unparsable = append(res.Unparsable, JunkFile{Name: name, ModTime: time.Unix(0, meta.ModTimeUnixNano)})
		}
	}
	return res, nil
}

// SweepJunk removes, while the lock is held, unparsable *.json files and
// leftover .*.tmp files whose mtime is more than StaleAfter before now. It
// never removes a symlink, FIFO, future-schema or valid lease file. It returns
// how many files it removed.
func (lk *Lock) SweepJunk(now time.Time) (int, error) {
	removed := 0
	res, err := lk.d.ListLeases()
	if err != nil {
		return 0, err
	}
	for _, j := range res.Unparsable {
		if !j.Stale(now) {
			continue
		}
		if err := os.Remove(filepath.Join(lk.d.LeasesPath(), j.Name)); err == nil {
			removed++
		} else if !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
	}
	entries, err := os.ReadDir(lk.d.LeasesPath())
	if err != nil {
		return removed, err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".tmp") {
			continue
		}
		fi, err := os.Lstat(filepath.Join(lk.d.LeasesPath(), name))
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) <= StaleAfter {
			continue
		}
		if err := os.Remove(filepath.Join(lk.d.LeasesPath(), name)); err == nil {
			removed++
		}
	}
	return removed, nil
}

func checkID(id string) error {
	owner, key, ok := strings.Cut(id, ".")
	if !ok || !ValidIDPart(owner) || !ValidIDPart(key) {
		return fmt.Errorf("invalid lease id %q", id)
	}
	return nil
}
