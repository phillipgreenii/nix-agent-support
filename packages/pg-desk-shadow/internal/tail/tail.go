// Package tail copies a growing live log into a scratch file incrementally:
// it tracks inode and offset, survives rotation and compaction, and its
// appends are idempotent across a crash.
package tail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// State is persisted between ticks (and across a collector restart).
type State struct {
	Initialized bool   `json:"initialized"`
	Inode       uint64 `json:"inode"`
	Offset      int64  `json:"offset"`
	// DstSize is the size of the scratch copy after the last COMPLETED sync:
	// at resume the copy is truncated back to it, so an append whose state
	// write was lost is repeated byte for byte instead of duplicated.
	DstSize int64 `json:"dst_size"`
	// Resets counts offset resets behind a marker row (compaction, unmatched
	// rotation, truncation) and PossibleLoss those where rows may be lost.
	Resets       int `json:"resets"`
	PossibleLoss int `json:"possible_loss"`
}

// Tailer copies Src into Dst.
type Tailer struct {
	Src, Dst string
	// StartAtEnd makes the FIRST sync start at the current end of Src (only
	// rows written after the run started matter) instead of at byte 0.
	StartAtEnd bool
	Now        func() time.Time
}

// maxPerSync bounds one sync's copy.
const maxPerSync = 64 << 20

func (t *Tailer) now() time.Time {
	if t.Now != nil {
		return t.Now().UTC()
	}
	return time.Now().UTC()
}

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

func (t *Tailer) marker(reason string) []byte {
	b, _ := json.Marshal(map[string]string{"_shadow": "reset", "reason": reason, "at": t.now().Format(time.RFC3339Nano)})
	return append(b, '\n')
}

// Sync copies what is new and returns the number of source bytes copied.
func (t *Tailer) Sync(st *State) (int64, error) {
	// Idempotence: drop whatever a crashed sync appended past the last
	// recorded size.
	if fi, err := os.Stat(t.Dst); err == nil {
		switch {
		case fi.Size() > st.DstSize && st.Initialized:
			if err := os.Truncate(t.Dst, st.DstSize); err != nil {
				return 0, fmt.Errorf("tail: truncate %s: %w", t.Dst, err)
			}
		case fi.Size() < st.DstSize:
			st.DstSize = fi.Size()
		}
	} else if os.IsNotExist(err) {
		st.DstSize = 0
	}

	fi, err := os.Stat(t.Src)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var out bytes.Buffer
	var copied int64

	if !st.Initialized {
		st.Initialized = true
		st.Inode = inodeOf(fi)
		st.Offset = 0
		if t.StartAtEnd {
			off, err := lineBoundaryBefore(t.Src, fi.Size())
			if err != nil {
				return 0, err
			}
			st.Offset = off
		}
	}

	ino := inodeOf(fi)
	switch {
	case ino != st.Inode:
		// Rotation (rename) or compaction (replace). A rotated file keeps its
		// inode under <src>.1: drain its tail first.
		if rfi, err := os.Stat(t.Src + ".1"); err == nil && inodeOf(rfi) == st.Inode {
			n, err := readFrom(t.Src+".1", st.Offset, &out)
			if err != nil {
				return 0, err
			}
			copied += n
		} else {
			out.Write(t.marker("inode-changed"))
			st.PossibleLoss++
		}
		st.Resets++
		st.Inode, st.Offset = ino, 0
	case fi.Size() < st.Offset:
		out.Write(t.marker("truncated"))
		st.Resets++
		st.PossibleLoss++
		st.Offset = 0
	}

	n, err := readFrom(t.Src, st.Offset, &out)
	if err != nil {
		return 0, err
	}
	copied += n
	st.Offset += n

	if out.Len() > 0 {
		f, err := os.OpenFile(t.Dst, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		if _, err := f.Write(out.Bytes()); err != nil {
			_ = f.Close()
			return 0, err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return 0, err
		}
		if err := f.Close(); err != nil {
			return 0, err
		}
	}
	if dfi, err := os.Stat(t.Dst); err == nil {
		st.DstSize = dfi.Size()
	}
	return copied, nil
}

// readFrom appends the complete lines of path from off to w and returns the
// number of source bytes consumed (up to and including the last newline).
func readFrom(path string, off int64, w *bytes.Buffer) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPerSync))
	if err != nil {
		return 0, err
	}
	i := bytes.LastIndexByte(b, '\n')
	if i < 0 {
		return 0, nil
	}
	w.Write(b[:i+1])
	return int64(i + 1), nil
}

// lineBoundaryBefore returns the offset just after the last newline at or
// before size, so a first sync never starts inside a half-written row.
func lineBoundaryBefore(path string, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	const window = 1 << 20
	start := size - window
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return 0, err
	}
	i := bytes.LastIndexByte(buf, '\n')
	if i < 0 {
		return start, nil
	}
	return start + int64(i) + 1, nil
}
