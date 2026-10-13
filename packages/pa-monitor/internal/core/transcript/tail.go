package transcript

import (
	"bufio"
	"io"
	"os"
)

// TailCursor is the resume point of an offset read over one append-only JSONL
// file: the byte offset just past the last complete line consumed, and the file
// identity used to detect rotation or rewrite. Treat it as opaque; obtain one
// from TailLines and pass it back on the next call. A nil cursor means "read
// from the start".
type TailCursor struct {
	offset int64
	info   os.FileInfo
}

// TailLines calls line for every complete newline-terminated line of path that
// was appended since prev, and returns the cursor to resume from, the number of
// bytes consumed, and whether the read started from the beginning of the file.
// Every consumer of an append-only transcript SHOULD read through this function
// so the offset, rotation and rewrite handling lives in one place.
//
// The read starts from the beginning (fresh) when prev is nil, the file was
// replaced (different inode), truncated below the cached offset, or rewritten in
// place (the byte before the cached offset is no longer a newline). reset, when
// non-nil, is called once before the first line of a fresh read so the caller
// can discard state derived from the previous read. A trailing segment with no
// newline is left unconsumed so a half-written final line is read again once
// completed. A single line over maxTranscriptLine is an error (bufio.ErrTooLong).
//
// A missing file yields the os.Open error; check it with os.IsNotExist.
func TailLines(path string, prev *TailCursor, reset func(), line func([]byte)) (cur *TailCursor, n int64, fresh bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}

	fresh = prev == nil
	if !fresh {
		switch {
		case prev.info == nil || !os.SameFile(prev.info, info):
			fresh = true // rotated / replaced (new inode)
		case info.Size() < prev.offset:
			fresh = true // truncated below what we consumed
		case prev.offset > 0 && !newlineAt(f, prev.offset-1):
			fresh = true // rewritten in place — prefix no longer matches
		}
	}
	var offset int64
	if !fresh {
		offset = prev.offset
	} else if reset != nil {
		reset()
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, fresh, err
	}
	n, err = foldLines(f, line)
	if err != nil {
		return nil, n, fresh, err
	}
	return &TailCursor{offset: offset + n, info: info}, n, fresh, nil
}

// foldLines feeds complete newline-terminated lines from r to fn, returning the
// number of bytes consumed (including the terminating newlines). A trailing
// segment with no newline is left unconsumed so a half-written final line is
// reprocessed once completed. A single line exceeding maxTranscriptLine is an
// error, matching Scan's historical limit.
func foldLines(r io.Reader, fn func([]byte)) (int64, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var consumed int64
	for {
		line, err := br.ReadBytes('\n')
		if n := len(line); n > 0 && line[n-1] == '\n' {
			if n > maxTranscriptLine {
				return consumed, bufio.ErrTooLong
			}
			consumed += int64(n)
			fn(line[:n-1])
		} else if n > maxTranscriptLine {
			return consumed, bufio.ErrTooLong
		}
		if err != nil {
			if err == io.EOF {
				return consumed, nil
			}
			return consumed, err
		}
	}
}

// newlineAt reports whether the byte at pos is '\n', without disturbing f's
// current read offset (uses ReadAt).
func newlineAt(f *os.File, pos int64) bool {
	var b [1]byte
	if _, err := f.ReadAt(b[:], pos); err != nil {
		return false
	}
	return b[0] == '\n'
}
