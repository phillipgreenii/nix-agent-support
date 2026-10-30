package store

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cespare/xxhash/v2"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
)

// The archive of a Conversation is a directory archives/<session>/ of segment
// files that only ever grow by a new segment. A segment is self-describing:
//
//	"PGCCRSEG1\n" | u32 manifest length | manifest JSON | gzip blocks | index JSON | u64 index length | "PGCCRIDX"
//
// Each block is an independent gzip member of at most BlockSize raw bytes of one
// (file, generation) range, so a byte range reads by decompressing only the
// blocks it touches. The index in the footer and the ranges table of index.db
// say where the bytes of each (file, generation) live.
const (
	segMagic    = "PGCCRSEG1\n"
	segIdxMagic = "PGCCRIDX"
	// BlockSize is the raw size of one compressed block.
	BlockSize = 1 << 20
)

// SegDir is the archive directory of a session.
func (s *Store) SegDir(session string) string { return filepath.Join(s.dir, ArchivesDir, session) }

// SegName is the file name of a session's seq-th segment.
func SegName(seq int) string { return fmt.Sprintf("%06d.seg", seq) }

// Block locates one compressed block of a segment.
type Block struct {
	File int64 `json:"file"`
	Gen  int   `json:"gen"`
	Off  int64 `json:"off"` // raw offset within the generation
	Len  int64 `json:"len"`
	ZOff int64 `json:"zoff"`
	ZLen int64 `json:"zlen"`
}

type segIndex struct {
	Blocks []Block `json:"blocks"`
}

// SegItem is one range of one generation to write into a segment; Src reads
// the raw bytes at generation offsets.
type SegItem struct {
	File     int64
	Gen      int
	From, To int64
	Src      io.ReaderAt
}

// SegResult is what WriteSegment produced.
type SegResult struct {
	Size int64
	Hash uint64 // xxhash64 of the whole segment file
}

type hashWriter struct {
	w io.Writer
	h *xxhash.Digest
	n int64
}

func (h *hashWriter) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	_, _ = h.h.Write(p[:n])
	h.n += int64(n)
	return n, err
}

// WriteSegment writes a segment to f (a temporary file the caller then syncs,
// verifies and renames into place).
func WriteSegment(f io.Writer, manifest []byte, items []SegItem) (SegResult, error) {
	hw := &hashWriter{w: f, h: xxhash.New()}
	var hdr bytes.Buffer
	hdr.WriteString(segMagic)
	_ = binary.Write(&hdr, binary.LittleEndian, uint32(len(manifest)))
	hdr.Write(manifest)
	if _, err := hw.Write(hdr.Bytes()); err != nil {
		return SegResult{}, err
	}
	var idx segIndex
	buf := make([]byte, BlockSize)
	for _, it := range items {
		for off := it.From; off < it.To; {
			n := int64(len(buf))
			if it.To-off < n {
				n = it.To - off
			}
			if m, err := it.Src.ReadAt(buf[:n], off); int64(m) != n {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return SegResult{}, fmt.Errorf("archive: reading file %d gen %d at %d: %w", it.File, it.Gen, off, err)
			}
			zoff := hw.n
			var z bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&z, gzip.BestSpeed)
			_, _ = zw.Write(buf[:n])
			if err := zw.Close(); err != nil {
				return SegResult{}, err
			}
			if _, err := hw.Write(z.Bytes()); err != nil {
				return SegResult{}, err
			}
			idx.Blocks = append(idx.Blocks, Block{File: it.File, Gen: it.Gen, Off: off, Len: n, ZOff: zoff, ZLen: int64(z.Len())})
			off += n
		}
	}
	ib, err := json.Marshal(idx)
	if err != nil {
		return SegResult{}, err
	}
	var tail bytes.Buffer
	tail.Write(ib)
	_ = binary.Write(&tail, binary.LittleEndian, uint64(len(ib)))
	tail.WriteString(segIdxMagic)
	if _, err := hw.Write(tail.Bytes()); err != nil {
		return SegResult{}, err
	}
	return SegResult{Size: hw.n, Hash: hw.h.Sum64()}, nil
}

// Seg is an open segment file.
type Seg struct {
	f        *os.File
	Blocks   []Block
	Manifest []byte
}

// OpenSeg opens and indexes a segment.
func OpenSeg(path string) (*Seg, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	s, err := readSeg(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("archive %s: %w", path, err)
	}
	return s, nil
}

func readSeg(f *os.File) (*Seg, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const foot = 8 + int64(len(segIdxMagic))
	if fi.Size() < int64(len(segMagic))+4+foot {
		return nil, errors.New("segment too short")
	}
	ft := make([]byte, foot)
	if _, err := f.ReadAt(ft, fi.Size()-foot); err != nil {
		return nil, err
	}
	if string(ft[8:]) != segIdxMagic {
		return nil, errors.New("bad segment footer")
	}
	il := int64(binary.LittleEndian.Uint64(ft[:8]))
	if il <= 0 || il > fi.Size() {
		return nil, errors.New("bad segment index length")
	}
	ib := make([]byte, il)
	if _, err := f.ReadAt(ib, fi.Size()-foot-il); err != nil {
		return nil, err
	}
	var idx segIndex
	if err := json.Unmarshal(ib, &idx); err != nil {
		return nil, err
	}
	head := make([]byte, len(segMagic)+4)
	if _, err := f.ReadAt(head, 0); err != nil || string(head[:len(segMagic)]) != segMagic {
		return nil, errors.New("bad segment header")
	}
	ml := int64(binary.LittleEndian.Uint32(head[len(segMagic):]))
	man := make([]byte, ml)
	if _, err := f.ReadAt(man, int64(len(head))); err != nil {
		return nil, err
	}
	return &Seg{f: f, Blocks: idx.Blocks, Manifest: man}, nil
}

// Close closes the segment.
func (s *Seg) Close() error { return s.f.Close() }

// ReadAt reads generation bytes of (file, gen) at off from the segment's
// blocks. It returns the bytes it could read (a short read means the segment
// holds nothing further at that offset).
func (s *Seg) ReadAt(file int64, gen int, b []byte, off int64) (int, error) {
	n := 0
	for n < len(b) {
		pos := off + int64(n)
		var blk *Block
		for i := range s.Blocks {
			x := &s.Blocks[i]
			if x.File == file && x.Gen == gen && x.Off <= pos && pos < x.Off+x.Len {
				blk = x
				break
			}
		}
		if blk == nil {
			break
		}
		zr, err := gzip.NewReader(io.NewSectionReader(s.f, blk.ZOff, blk.ZLen))
		if err != nil {
			return n, err
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			return n, err
		}
		if int64(len(raw)) != blk.Len {
			return n, errors.New("archive: block length mismatch")
		}
		n += copy(b[n:], raw[pos-blk.Off:])
	}
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

// VerifySegment reads the segment at path back and compares every block with
// the source bytes of items (the read-back before the rename).
func VerifySegment(path string, items []SegItem) error {
	s, err := OpenSeg(path)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	for _, it := range items {
		for off := it.From; off < it.To; {
			n := int64(BlockSize)
			if it.To-off < n {
				n = it.To - off
			}
			want := make([]byte, n)
			if m, err := it.Src.ReadAt(want, off); int64(m) != n {
				return fmt.Errorf("archive verify: source file %d gen %d at %d: %v", it.File, it.Gen, off, err)
			}
			got := make([]byte, n)
			if _, err := s.ReadAt(it.File, it.Gen, got, off); err != nil {
				return fmt.Errorf("archive verify: file %d gen %d at %d: %w", it.File, it.Gen, off, err)
			}
			if !bytes.Equal(got, want) {
				return fmt.Errorf("archive verify: file %d gen %d differs at %d", it.File, it.Gen, off)
			}
			off += n
		}
	}
	return nil
}

// WriteSegmentFile writes a segment into dir as name through fsys with the
// durable order temporary file, flush, read-back, directory flush, rename. The
// caller commits afterwards.
func WriteSegmentFile(fsys fsx.FS, dir, name string, manifest []byte, items []SegItem) (SegResult, error) {
	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		return SegResult{}, err
	}
	final := filepath.Join(dir, name)
	tmp := final + ".tmp"
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return SegResult{}, err
	}
	res, err := WriteSegment(f, manifest, items)
	if err == nil {
		err = fsx.FullSync(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = fsys.Remove(tmp)
		return SegResult{}, err
	}
	faults.At("settle.after-temp")
	if err := VerifySegment(tmp, items); err != nil {
		_ = fsys.Remove(tmp)
		return SegResult{}, err
	}
	fsx.Mark(fsys, "readback")
	if err := fsx.FullSyncDir(fsys, dir); err != nil {
		return SegResult{}, err
	}
	if err := fsys.Rename(tmp, final); err != nil {
		return SegResult{}, err
	}
	if err := fsx.FullSyncDir(fsys, dir); err != nil {
		return SegResult{}, err
	}
	faults.At("settle.after-rename")
	return res, nil
}
