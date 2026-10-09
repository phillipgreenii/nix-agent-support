package stranded

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// readChunk is how many new bytes one read pulls from a transcript.
const readChunk = 1 << 20

// File is the part of an open file the scanner needs. *os.File satisfies it.
type File interface {
	io.ReaderAt
	io.Closer
	Stat() (fs.FileInfo, error)
}

// Opener opens a transcript for reading. Tests wrap it to count the files
// opened and the bytes read.
type Opener func(path string) (File, error)

func osOpen(path string) (File, error) { return os.Open(path) }

// Source is one transcript the scanner should read: its path and the stat the
// caller already took (which names the file's identity and size).
type Source struct {
	Path string
	Info fs.FileInfo
}

// entry is what the cache keeps for one transcript: how far it has been read
// and every claim value found in the bytes read so far.
type entry struct {
	offset int64
	values map[string]struct{}
}

// Scanner extracts the claim values written in transcripts, incrementally.
//
// It is an append-only cache keyed by file identity (device and inode, so a
// rename does not lose the file's history). A transcript that was read before
// is read again only from maxNeedleLen-1 bytes before the old end, so the
// re-read is the appended bytes plus a fixed overlap; a transcript that has
// not grown is not even opened. A file that shrank, or whose identity now names
// a smaller file, is read again from the start. Entries for files not passed to
// a scan are dropped, so the cache never outlives the activity window.
type Scanner struct {
	open    Opener
	chunk   int
	entries map[fileID]*entry
}

// NewScanner builds a scanner over open; nil means os.Open.
func NewScanner(open Opener) *Scanner {
	if open == nil {
		open = osOpen
	}
	return &Scanner{open: open, chunk: readChunk, entries: map[fileID]*entry{}}
}

// Claims reads the new bytes of every source and returns the union of the claim
// values found in all of them. An error means a source could not be read, in
// which case no answer is returned: a transcript that cannot be read might be
// the one that proves a claim live.
func (s *Scanner) Claims(ctx context.Context, sources []Source) (map[string]struct{}, error) {
	seen := make(map[fileID]*entry, len(sources))
	out := map[string]struct{}{}
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := idOf(src.Info)
		e := s.entries[id]
		if e == nil || e.offset > src.Info.Size() {
			e = &entry{values: map[string]struct{}{}}
		}
		if e.offset < src.Info.Size() {
			if err := s.read(ctx, src, e); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue // removed since it was listed: nothing to read
				}
				return nil, fmt.Errorf("read transcript: %w", err)
			}
		}
		seen[id] = e
		for v := range e.values {
			out[v] = struct{}{}
		}
	}
	s.entries = seen
	return out, nil
}

// read scans the not-yet-read tail of src into e.
func (s *Scanner) read(ctx context.Context, src Source, e *entry) error {
	f, err := s.open(src.Path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	pos := max(0, e.offset-int64(maxNeedleLen-1))
	add := func(v string) { e.values[v] = struct{}{} }
	var carry []byte
	buf := make([]byte, s.chunk)
	for pos < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := f.ReadAt(buf, pos)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n == 0 {
			break // the file shrank under the read; resume from here next time
		}
		data := append(carry, buf[:n]...)
		scanClaims(data, add)
		keep := min(maxNeedleLen-1, len(data))
		carry = append([]byte(nil), data[len(data)-keep:]...)
		pos += int64(n)
	}
	e.offset = pos
	return nil
}
