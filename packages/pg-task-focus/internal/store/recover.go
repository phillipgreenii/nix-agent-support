package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// sidecarStamp is the UTC layout of the stamp in a sidecar's name.
const sidecarStamp = "20060102T150405Z"

// maxSidecars bounds the search for a free sidecar name.
const maxSidecars = 10000

// wouldRecover describes what recovery does with a scanned log: cut every
// byte after the end of the last committed record.
func wouldRecover(rep scanReport, endOfLastCommitted int64) Recovery {
	rec := Recovery{
		TornTail:       rep.TornTail,
		TruncatedBytes: rep.Size - endOfLastCommitted,
	}
	if rep.UncommittedEvents > 0 {
		rec.UncommittedBatches = 1
	}
	return rec
}

// recoverTail removes the unacknowledged end of the log (INV-LOG-9,
// INV-LOG-10) and returns the path of the sidecar that keeps its bytes. The
// order is the guarantee: the bytes are copied to a new sidecar and made
// durable, its directory entry included, and only then is the log truncated to the end of the last
// committed record and that made durable. A crash anywhere in between leaves
// the log as it was, so recovering again is safe: it copies the same bytes to
// the next free sidecar name and carries on.
//
// The unacknowledged bytes are one batch and one line, so they are read into
// memory whole.
func recoverTail(fsys FS, log File, dir string, now time.Time, end, size int64) (string, error) {
	tail := make([]byte, size-end)
	if n, err := log.ReadAt(tail, end); n < len(tail) {
		if err == nil || errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return "", fmt.Errorf("reading the unacknowledged bytes: %w", err)
	}

	path, err := writeSidecar(fsys, dir, now, tail)
	if err != nil {
		return "", err
	}
	if err := log.Truncate(end); err != nil {
		return "", fmt.Errorf("truncating the log to its last committed record: %w", err)
	}
	if err := log.Sync(); err != nil {
		return "", fmt.Errorf("syncing the truncated log: %w", err)
	}
	return path, nil
}

// writeSidecar copies data to a new file named
// events.jsonl.recovered-<UTC stamp>-<n>, mode 0600, with the first n that is
// free: an earlier sidecar is never overwritten. The file, and its entry in the
// directory, are durable when it returns. A sidecar that could not be
// completed is removed, so a partial copy is never mistaken for the real one;
// a complete one whose directory could not be synced is kept, since the bytes
// it holds are whole.
func writeSidecar(fsys FS, dir string, now time.Time, data []byte) (string, error) {
	prefix := filepath.Join(dir, logName+".recovered-"+now.UTC().Format(sidecarStamp)+"-")
	for n := 1; n <= maxSidecars; n++ {
		path := prefix + strconv.Itoa(n)
		f, err := fsys.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("creating the recovery sidecar %s: %w", path, err)
		}
		if err := fill(f, data); err != nil {
			_ = fsys.Remove(path)
			return "", fmt.Errorf("writing the recovery sidecar %s: %w", path, err)
		}
		if err := syncDir(fsys, dir); err != nil {
			return "", fmt.Errorf("syncing the directory of the recovery sidecar %s: %w", path, err)
		}
		return path, nil
	}
	return "", fmt.Errorf("no free recovery sidecar name under %s after %d tries", prefix, maxSidecars)
}

// fill writes data to f, makes it durable and closes f.
func fill(f File, data []byte) error {
	if n, err := f.Write(data); err != nil || n < len(data) {
		_ = f.Close()
		if err == nil {
			err = io.ErrShortWrite
		}
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
