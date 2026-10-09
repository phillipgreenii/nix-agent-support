package stranded

import (
	"bytes"
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
// is read again from the start of its first unfinished line, so the re-read is
// the appended bytes (plus the partial last line, normally none); a transcript
// that has not grown is not even opened. Values printed by tool results are not
// claims and are not recorded. A file that shrank, or whose identity now names
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

// Markers of a tool's output rather than of something the session itself wrote.
// A transcript line that carries one of them is a tool result: whatever claim
// values it prints (a bd list, a grep over another transcript) are mentions,
// not claims. Both are JSON keys/values written raw, never inside an escaped
// string, so a message that merely talks about them does not match.
var resultMarkers = [][]byte{
	[]byte(`"type":"tool_result"`),
	[]byte(`"toolUseResult":`),
}

// lineScan accumulates the claim values of one transcript line and commits them
// only if the line turns out not to be a tool result (the marker can come after
// the values in the line).
type lineScan struct {
	result  bool
	pending map[string]struct{}
}

func (l *lineScan) feed(piece []byte) {
	if l.result {
		return
	}
	for _, m := range resultMarkers {
		if bytes.Contains(piece, m) {
			l.result = true
			l.pending = nil
			return
		}
	}
	scanClaims(piece, func(v string) {
		if l.pending == nil {
			l.pending = map[string]struct{}{}
		}
		l.pending[v] = struct{}{}
	})
}

// end finishes the line: its values count unless it was a tool result.
func (l *lineScan) end(commit func(string)) {
	if !l.result {
		for v := range l.pending {
			commit(v)
		}
	}
	l.result, l.pending = false, nil
}

// read scans the not-yet-read tail of src into e. Only complete lines are
// consumed: the offset the entry keeps is the start of the first line that has
// no terminating newline yet (a transcript being written), so that line is read
// again, whole, next time. Within one pass the last maxNeedleLen-1 bytes of an
// unfinished line are carried into the next chunk so a claim value or a marker
// cut by a chunk boundary is still seen.
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
	pos := e.offset // always the start of a line
	committed := pos
	add := func(v string) { e.values[v] = struct{}{} }
	var (
		carry []byte
		line  lineScan
	)
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
			break // the file shrank under the read; resume from the last complete line
		}
		data := append(carry, buf[:n]...)
		dataStart := pos - int64(len(carry)) // file offset of data[0]
		rest := data
		for {
			i := bytes.IndexByte(rest, '\n')
			if i < 0 {
				line.feed(rest)
				break
			}
			line.feed(rest[:i+1])
			line.end(add)
			rest = rest[i+1:]
			committed = dataStart + int64(len(data)-len(rest))
		}
		keep := min(maxNeedleLen-1, len(rest))
		carry = append([]byte(nil), rest[len(rest)-keep:]...)
		pos += int64(n)
	}
	e.offset = committed
	return nil
}
