package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// ArchivePath is the legacy whole-generation archive file name; settled
// generations are read through the ranges table instead, but a file here is
// still honoured.
func (s *Store) ArchivePath(file int64, gen int) string {
	return filepath.Join(s.dir, ArchivesDir, fmt.Sprintf("%d.%d", file, gen))
}

// Pointer reads the bytes a record pointer (file id, generation) names, from the
// copy file and from the archive segments that hold the settled part, as one
// seekless byte stream: ReadAt and Read see the same bytes before and after a
// settle.
type Pointer struct {
	st     *Store
	plain  *os.File
	ranges []Range

	mu   sync.Mutex
	segs map[string]*Seg
	pos  int64
}

// OpenPointer opens the bytes a record pointer (file id, generation) names: the
// copy of an append file's generation, the stored bytes of a snapshot version
// (gen is the version), and the archive ranges holding what a settle moved out
// of the copy. It is the one abstraction every reader of stored bytes goes
// through.
func (s *Store) OpenPointer(file int64, gen int) (*Pointer, error) {
	mode := ""
	_ = s.q.QueryRow(`SELECT mode FROM files WHERE id=?`, file).Scan(&mode)
	primary := s.CopyPath(file, gen)
	if mode == "snapshot" {
		primary = s.SnapshotPath(file, gen)
	}
	rs, err := s.Ranges(file, gen)
	if err != nil {
		return nil, err
	}
	p := &Pointer{st: s, ranges: rs, segs: map[string]*Seg{}}
	f, err := os.Open(primary)
	switch {
	case err == nil:
		p.plain = f
	case errors.Is(err, fs.ErrNotExist) && len(rs) > 0:
	case errors.Is(err, fs.ErrNotExist):
		lf, lerr := os.Open(s.ArchivePath(file, gen))
		if lerr != nil {
			if errors.Is(lerr, fs.ErrNotExist) {
				return nil, fmt.Errorf("store: no bytes for file %d generation %d: %w", file, gen, ErrNotFound)
			}
			return nil, lerr
		}
		p.plain = lf
	default:
		return nil, err
	}
	return p, nil
}

func (p *Pointer) seg(r Range) (*Seg, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := r.Session + "/" + r.Seg
	if sg := p.segs[key]; sg != nil {
		return sg, nil
	}
	sg, err := OpenSeg(filepath.Join(p.st.SegDir(r.Session), r.Seg))
	if err != nil {
		return nil, err
	}
	p.segs[key] = sg
	return sg, nil
}

// Size is the length of the stream: the end of the last archived range or of the
// copy file, whichever is further.
func (p *Pointer) Size() int64 {
	var n int64
	for _, r := range p.ranges {
		if r.To > n {
			n = r.To
		}
	}
	if p.plain != nil {
		if fi, err := p.plain.Stat(); err == nil && fi.Size() > n {
			n = fi.Size()
		}
	}
	return n
}

// ReadAt implements io.ReaderAt.
func (p *Pointer) ReadAt(b []byte, off int64) (int, error) {
	n := 0
	for n < len(b) {
		pos := off + int64(n)
		var in *Range
		next := int64(-1)
		for i := range p.ranges {
			r := &p.ranges[i]
			if r.From <= pos && pos < r.To {
				in = r
				break
			}
			if r.From > pos && (next < 0 || r.From < next) {
				next = r.From
			}
		}
		if in != nil {
			want := len(b) - n
			if rest := in.To - pos; int64(want) > rest {
				want = int(rest)
			}
			sg, err := p.seg(*in)
			if err != nil {
				return n, err
			}
			m, err := sg.ReadAt(in.File, in.Gen, b[n:n+want], pos)
			n += m
			if err != nil {
				return n, err
			}
			continue
		}
		if p.plain == nil {
			return n, io.EOF
		}
		want := len(b) - n
		if next >= 0 && int64(want) > next-pos {
			want = int(next - pos)
		}
		m, err := p.plain.ReadAt(b[n:n+want], pos)
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// Read implements io.Reader.
func (p *Pointer) Read(b []byte) (int, error) {
	n, err := p.ReadAt(b, p.pos)
	p.pos += int64(n)
	if n > 0 && errors.Is(err, io.EOF) {
		err = nil
	}
	return n, err
}

// Close closes the copy file and every opened segment.
func (p *Pointer) Close() error {
	var first error
	if p.plain != nil {
		first = p.plain.Close()
	}
	p.mu.Lock()
	for _, sg := range p.segs {
		if err := sg.Close(); err != nil && first == nil {
			first = err
		}
	}
	p.mu.Unlock()
	return first
}

// ReadPointer reads up to n bytes at off through OpenPointer.
func (s *Store) ReadPointer(file int64, gen int, off int64, n int) ([]byte, error) {
	f, err := s.OpenPointer(file, gen)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	m, err := f.ReadAt(buf, off)
	if err != nil && m == 0 && n > 0 {
		return nil, err
	}
	return buf[:m], nil
}
