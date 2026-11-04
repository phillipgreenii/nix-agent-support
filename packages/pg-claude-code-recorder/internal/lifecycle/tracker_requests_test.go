package lifecycle

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// readSpan is one ReadAt a counting FS saw.
type readSpan struct{ off, n int64 }

// countFS wraps the real file system and records each ReadAt on a file.
type countFS struct {
	fsx.OS
	mu    sync.Mutex
	reads []readSpan
	opens int
}

func (c *countFS) OpenFile(name string, flag int, perm fs.FileMode) (fsx.File, error) {
	f, err := c.OS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.opens++
	c.mu.Unlock()
	return &countFile{File: f, c: c}, nil
}

type countFile struct {
	fsx.File
	c *countFS
}

func (f *countFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(p, off)
	f.c.mu.Lock()
	f.c.reads = append(f.c.reads, readSpan{off, int64(n)})
	f.c.mu.Unlock()
	return n, err
}

func (c *countFS) reset() {
	c.mu.Lock()
	c.reads, c.opens = nil, 0
	c.mu.Unlock()
}

// payloadBytes sums the bytes read at or beyond from; hashBytes sums the rest
// (the cursor verification reads, at most two windows of reqHashLen).
func (c *countFS) split(from int64) (payload, other int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.reads {
		if r.off >= from {
			payload += r.n
		} else {
			other += r.n
		}
	}
	return
}

type reqEnv struct {
	t    *testing.T
	tr   *Tracker
	fs   *countFS
	path string
}

func newReqEnv(t *testing.T) *reqEnv {
	t.Helper()
	dir := t.TempDir()
	cfs := &countFS{}
	tr := NewTracker(TrackerConfig{ControlDir: dir, FS: cfs})
	return &reqEnv{t: t, tr: tr, fs: cfs, path: filepath.Join(dir, "requests.jsonl")}
}

func reqLine(i int) string {
	return fmt.Sprintf(`{"ts":%d,"id":"r%d","verb":"feed","outcome":"ok"}`+"\n", 1000+i, i)
}

func reqLines(from, to int) string {
	var sb strings.Builder
	for i := from; i < to; i++ {
		sb.WriteString(reqLine(i))
	}
	return sb.String()
}

func (e *reqEnv) write(s string) {
	e.t.Helper()
	if err := os.WriteFile(e.path, []byte(s), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *reqEnv) append(s string) {
	e.t.Helper()
	f, err := os.OpenFile(e.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(s); err != nil {
		e.t.Fatal(err)
	}
}

func (e *reqEnv) size() int64 {
	fi, err := os.Stat(e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	return fi.Size()
}

// total is control_requests_total summed over every label set.
func (e *reqEnv) total() int {
	counters, _, _ := e.tr.cfg.Obs.Reg.Snapshot()
	var sum float64
	for k, v := range counters {
		if strings.HasPrefix(k, obs.MControlRequests+"{") || k == obs.MControlRequests {
			sum += v
		}
	}
	return int(sum)
}

func (e *reqEnv) reads(path string) int {
	v, _ := e.tr.cfg.Obs.Reg.Counter(obs.MRequestsJSONReads, map[string]string{"path": path})
	return int(v)
}

func (e *reqEnv) poll() {
	e.fs.reset()
	e.tr.readRequests()
}

func TestRequestsTailReadsOnlyAppendedBytes(t *testing.T) {
	e := newReqEnv(t)
	e.write(reqLines(0, 3))
	e.poll()
	if e.total() != 3 || e.reads("full") != 1 || e.reads("tail") != 0 {
		t.Fatalf("first poll: total=%d full=%d tail=%d", e.total(), e.reads("full"), e.reads("tail"))
	}
	before := e.size()
	add := reqLines(3, 5)
	e.append(add)
	e.poll()
	if e.total() != 5 {
		t.Fatalf("total = %d, want 5 (each line once)", e.total())
	}
	payload, other := e.fs.split(before)
	if payload != int64(len(add)) {
		t.Fatalf("bytes read from the offset = %d, want the appended %d", payload, len(add))
	}
	if other > 2*reqHashLen {
		t.Fatalf("verification read %d bytes, want at most %d", other, 2*reqHashLen)
	}
	if e.reads("tail") != 1 || e.reads("full") != 1 {
		t.Fatalf("tail=%d full=%d, want 1/1", e.reads("tail"), e.reads("full"))
	}
}

func TestRequestsUnchangedFileIsNotRead(t *testing.T) {
	e := newReqEnv(t)
	e.write(reqLines(0, 2))
	e.poll()
	e.poll()
	if e.fs.opens != 0 || len(e.fs.reads) != 0 {
		t.Fatalf("unchanged poll opened %d, read %d spans; want none", e.fs.opens, len(e.fs.reads))
	}
	if e.total() != 2 {
		t.Fatalf("total = %d", e.total())
	}
}

func TestRequestsPartialLineWaitsForNewline(t *testing.T) {
	e := newReqEnv(t)
	e.write(reqLines(0, 1))
	e.poll()
	whole := reqLine(1)
	e.append(whole[:len(whole)/2])
	e.poll()
	if e.total() != 1 {
		t.Fatalf("partial line counted: total=%d", e.total())
	}
	if want := int64(len(reqLine(0))); e.tr.reqCur.off != want {
		t.Fatalf("offset = %d, want %d (not past the partial line)", e.tr.reqCur.off, want)
	}
	e.append(whole[len(whole)/2:])
	e.poll()
	if e.total() != 2 {
		t.Fatalf("completed line not counted once: total=%d", e.total())
	}
	if want := int64(len(reqLines(0, 2))); e.tr.reqCur.off != want {
		t.Fatalf("offset = %d, want %d", e.tr.reqCur.off, want)
	}
}

// The mod keeps the last 1,000 lines, so the file is rewritten with the oldest
// dropped and new lines at the end.
func TestRequestsRewriteWithOldestDroppedCountsNewOnce(t *testing.T) {
	e := newReqEnv(t)
	e.write(reqLines(0, 10))
	e.poll()
	e.write(reqLines(3, 12))
	e.poll()
	if e.total() != 12 {
		t.Fatalf("total = %d, want 12", e.total())
	}
	if e.reads("full") != 2 {
		t.Fatalf("full reads = %d, want 2 (the rewrite falls back)", e.reads("full"))
	}
	// A later fallback must not re-count lines from the earlier windows.
	e.write(reqLines(5, 14))
	e.poll()
	if e.total() != 14 {
		t.Fatalf("total = %d, want 14", e.total())
	}
}

// A rewrite that happens to leave the file longer, with the same inode, is
// caught by the prefix hashes, not by the size.
func TestRequestsRewriteLongerSameInodeFallsBack(t *testing.T) {
	e := newReqEnv(t)
	e.write(reqLines(0, 4))
	e.poll()
	e.write(reqLines(2, 12)) // os.WriteFile truncates in place: same inode, larger size
	e.poll()
	if e.total() != 12 {
		t.Fatalf("total = %d, want 12", e.total())
	}
	if e.reads("full") != 2 || e.reads("tail") != 0 {
		t.Fatalf("full=%d tail=%d, want 2/0", e.reads("full"), e.reads("tail"))
	}
}

func TestRequestsNewInodeCountsNewOnce(t *testing.T) {
	e := newReqEnv(t)
	e.write(reqLines(0, 3))
	e.poll()
	tmp := e.path + ".new"
	if err := os.WriteFile(tmp, []byte(reqLines(1, 6)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, e.path); err != nil {
		t.Fatal(err)
	}
	e.poll()
	if e.total() != 6 {
		t.Fatalf("total = %d, want 6", e.total())
	}
	if e.reads("full") != 2 {
		t.Fatalf("full = %d, want 2", e.reads("full"))
	}
}

func TestRequestsShrinkingFileCountsNothingTwice(t *testing.T) {
	e := newReqEnv(t)
	e.write(reqLines(0, 6))
	e.poll()
	e.write(reqLines(4, 6))
	e.poll()
	if e.total() != 6 {
		t.Fatalf("total = %d, want 6", e.total())
	}
	e.append(reqLines(6, 7))
	e.poll()
	if e.total() != 7 || e.reads("tail") != 1 {
		t.Fatalf("total=%d tail=%d, want 7 and 1 tail read after the fallback reset the cursor", e.total(), e.reads("tail"))
	}
}

func TestRequestsFallbackAfterTailDoesNotRecount(t *testing.T) {
	e := newReqEnv(t)
	e.write(reqLines(0, 3))
	e.poll()
	e.append(reqLines(3, 5))
	e.poll()
	e.write(reqLines(2, 5)) // shrinks; lines 2-4 were all counted before
	e.poll()
	if e.total() != 5 {
		t.Fatalf("total = %d, want 5", e.total())
	}
}

func TestRequestsMissingFileIsQuiet(t *testing.T) {
	e := newReqEnv(t)
	e.poll()
	if e.total() != 0 || e.reads("full") != 0 {
		t.Fatalf("missing file counted: total=%d", e.total())
	}
}
