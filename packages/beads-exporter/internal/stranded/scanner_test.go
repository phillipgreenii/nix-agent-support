package stranded

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// scan stats path and runs one Claims pass over it.
func scan(t *testing.T, s *Scanner, paths ...string) map[string]struct{} {
	t.Helper()
	var srcs []Source
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, Source{Path: p, Info: info})
	}
	got, err := s.Claims(context.Background(), srcs)
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	return got
}

func keys(m map[string]struct{}) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestIncrementalCacheReadsOnlyAppendedBytes(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, commandEvent("bd update x --actor worker-1"), textEvent(strings.Repeat("filler ", 400)))
	s := NewScanner(f.opener.Open)

	got := scan(t, s, p)
	if !equalStrings(keys(got), []string{"worker-1"}) {
		t.Fatalf("first scan = %v", keys(got))
	}
	info, _ := os.Stat(p)
	if n := f.opener.bytesRead(p); n != info.Size() {
		t.Fatalf("first scan read %d bytes of a %d byte file", n, info.Size())
	}

	// Nothing appended: the file is not even opened.
	f.opener.reset()
	if got := scan(t, s, p); !equalStrings(keys(got), []string{"worker-1"}) {
		t.Fatalf("unchanged scan = %v", keys(got))
	}
	if opened := f.opener.openedPaths(); len(opened) != 0 {
		t.Fatalf("an unchanged transcript was opened: %v", opened)
	}

	// Append one event: only the new bytes plus the overlap are read, and a new
	// claim value in them is found.
	before := info.Size()
	f.append(p, time.Minute, commandEvent("bd update y --actor worker-2"))
	info, _ = os.Stat(p)
	appended := info.Size() - before
	f.opener.reset()
	got = scan(t, s, p)
	if !equalStrings(keys(got), []string{"worker-1", "worker-2"}) {
		t.Fatalf("scan after append = %v", keys(got))
	}
	if want := appended + int64(maxNeedleLen-1); f.opener.bytesRead(p) != want {
		t.Fatalf("read %d bytes after appending %d, want appended + overlap = %d", f.opener.bytesRead(p), appended, want)
	}
}

func TestOverlapDecidesAClaimCutOffAtTheOldEnd(t *testing.T) {
	f := newFixture(t)
	path := f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	s := NewScanner(f.opener.Open)
	scan(t, s, path)

	// Append a claim that is written in two pieces: the first scan sees the
	// value cut off and must not decide it; the next scan sees the rest.
	appendRaw := func(text string) {
		fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fh.WriteString(text); err != nil {
			t.Fatal(err)
		}
		_ = fh.Close()
	}
	appendRaw(`bd update x --actor worker-1`)
	if got := scan(t, s, path); len(got) != 0 {
		t.Fatalf("an unterminated value was decided: %v", keys(got))
	}
	appendRaw(`-extra --claim` + "\n")
	if got := scan(t, s, path); !equalStrings(keys(got), []string{"worker-1-extra"}) {
		t.Fatalf("scan = %v, want only the completed value", keys(got))
	}
}

func TestChunkedReadMatchesAWholeRead(t *testing.T) {
	f := newFixture(t)
	events := []map[string]any{textEvent("start")}
	want := []string{}
	for _, name := range []string{"alpha-1", "beta-22", "gamma-333", "delta-4444"} {
		events = append(events, commandEvent("bd update x --actor "+name), textEvent(strings.Repeat("pad ", 30)))
		want = append(want, name)
	}
	p := f.write("-slug/a.jsonl", time.Hour, events...)
	sort.Strings(want)
	for _, chunk := range []int{1, 2, 3, 7, 16, 64, 255, 256, 1024, readChunk} {
		s := NewScanner(nil)
		s.chunk = chunk
		if got := keys(scan(t, s, p)); !equalStrings(got, want) {
			t.Fatalf("chunk %d: claims = %v, want %v", chunk, got, want)
		}
	}
}

func TestShrunkFileIsReadAgainFromTheStart(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, commandEvent("bd update x --actor worker-1"), textEvent(strings.Repeat("pad ", 100)))
	s := NewScanner(f.opener.Open)
	scan(t, s, p)

	// Rewrite the same inode with shorter, different content.
	if err := os.WriteFile(p, marshalLines(t, commandEvent("bd update x --actor worker-2")), 0o644); err != nil {
		t.Fatal(err)
	}
	f.touch(p, time.Minute)
	if got := scan(t, s, p); !equalStrings(keys(got), []string{"worker-2"}) {
		t.Fatalf("scan after truncation = %v, want only the new content", keys(got))
	}
}

func TestEntriesForFilesNoLongerScannedAreDropped(t *testing.T) {
	f := newFixture(t)
	a := f.write("-slug/a.jsonl", time.Hour, commandEvent("bd update x --actor worker-1"))
	b := f.write("-slug/b.jsonl", time.Hour, commandEvent("bd update x --actor worker-2"))
	s := NewScanner(f.opener.Open)
	if got := scan(t, s, a, b); !equalStrings(keys(got), []string{"worker-1", "worker-2"}) {
		t.Fatalf("scan = %v", keys(got))
	}
	if len(s.entries) != 2 {
		t.Fatalf("%d cache entries, want 2", len(s.entries))
	}
	if got := scan(t, s, a); !equalStrings(keys(got), []string{"worker-1"}) {
		t.Fatalf("scan = %v", keys(got))
	}
	if len(s.entries) != 1 {
		t.Fatalf("%d cache entries after dropping a file, want 1", len(s.entries))
	}
	// The dropped file is read from scratch when it comes back.
	f.opener.reset()
	scan(t, s, b)
	info, _ := os.Stat(b)
	if f.opener.bytesRead(b) != info.Size() {
		t.Fatalf("a returning file was read for %d of %d bytes", f.opener.bytesRead(b), info.Size())
	}
}

func TestRenamedFileKeepsItsCacheEntry(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, commandEvent("bd update x --actor worker-1"))
	s := NewScanner(f.opener.Open)
	scan(t, s, p)
	moved := p + ".moved"
	if err := os.Rename(p, moved); err != nil {
		t.Fatal(err)
	}
	f.opener.reset()
	if got := scan(t, s, moved); !equalStrings(keys(got), []string{"worker-1"}) {
		t.Fatalf("scan = %v", keys(got))
	}
	if opened := f.opener.openedPaths(); len(opened) != 0 {
		t.Fatalf("a renamed, unchanged file was re-read: %v", opened)
	}
}

func TestEmptyFileIsNotOpened(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour)
	s := NewScanner(f.opener.Open)
	if got := scan(t, s, p); len(got) != 0 {
		t.Fatalf("scan = %v", keys(got))
	}
	if opened := f.opener.openedPaths(); len(opened) != 0 {
		t.Fatalf("an empty file was opened: %v", opened)
	}
}

func TestClaimsFromEveryFileAreUnioned(t *testing.T) {
	f := newFixture(t)
	a := f.write("-slug/a.jsonl", time.Hour, commandEvent("bd update x --actor worker-1"))
	b := f.write("-slug/b.jsonl", time.Hour, commandEvent("bd update x --actor worker-2"), commandEvent("bd update x --actor worker-1"))
	if got := scan(t, NewScanner(nil), a, b); !equalStrings(keys(got), []string{"worker-1", "worker-2"}) {
		t.Fatalf("scan = %v", keys(got))
	}
}

func TestOpenErrorFailsTheScan(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	info, _ := os.Stat(p)
	s := NewScanner(func(string) (File, error) { return nil, os.ErrPermission })
	if _, err := s.Claims(context.Background(), []Source{{Path: p, Info: info}}); err == nil {
		t.Fatal("want the open error")
	}
}

func TestCancelledContextStopsAScan(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	info, _ := os.Stat(p)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewScanner(nil).Claims(ctx, []Source{{Path: p, Info: info}}); err == nil {
		t.Fatal("want the context error")
	}
}

// failingFile wraps an os.File and fails or distorts selected calls.
type failingFile struct {
	*os.File
	statErr  error
	readErr  error
	extra    int64 // added to the size Stat reports
	onRead   func()
	readCall int
}

func (f *failingFile) Stat() (fs.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return inflated{info, f.extra}, nil
}

func (f *failingFile) ReadAt(p []byte, off int64) (int, error) {
	f.readCall++
	if f.readCall > 1000 {
		return 0, errors.New("runaway read loop")
	}
	if f.onRead != nil {
		f.onRead()
	}
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.File.ReadAt(p, off)
}

type inflated struct {
	fs.FileInfo
	extra int64
}

func (i inflated) Size() int64 { return i.FileInfo.Size() + i.extra }

func opener(wrap func(*os.File) *failingFile) Opener {
	return func(path string) (File, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return wrap(f), nil
	}
}

func sourceOf(t *testing.T, p string) Source {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return Source{Path: p, Info: info}
}

func TestStatErrorOfAnOpenFileFailsTheScan(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	boom := errors.New("stat boom")
	s := NewScanner(opener(func(fh *os.File) *failingFile { return &failingFile{File: fh, statErr: boom} }))
	if _, err := s.Claims(context.Background(), []Source{sourceOf(t, p)}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the stat error", err)
	}
}

func TestReadErrorFailsTheScan(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	boom := errors.New("read boom")
	s := NewScanner(opener(func(fh *os.File) *failingFile { return &failingFile{File: fh, readErr: boom} }))
	if _, err := s.Claims(context.Background(), []Source{sourceOf(t, p)}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the read error", err)
	}
}

func TestAFileShorterThanItsStatEndsTheReadAndResumesFromWhereItStopped(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, commandEvent("bd update x --actor worker-1"))
	real, _ := os.Stat(p)
	s := NewScanner(opener(func(fh *os.File) *failingFile { return &failingFile{File: fh, extra: 500} }))
	src := sourceOf(t, p)
	got, err := s.Claims(context.Background(), []Source{src})
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if !equalStrings(keys(got), []string{"worker-1"}) {
		t.Fatalf("claims = %v", keys(got))
	}
	for _, e := range s.entries {
		if e.offset != real.Size() {
			t.Fatalf("offset = %d, want the %d bytes actually read", e.offset, real.Size())
		}
	}
}

func TestCancellationBetweenReadsStopsAMultiChunkRead(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, commandEvent("bd update x --actor worker-1"), textEvent(strings.Repeat("pad ", 200)))
	ctx, cancel := context.WithCancel(context.Background())
	s := NewScanner(opener(func(fh *os.File) *failingFile { return &failingFile{File: fh, onRead: cancel} }))
	s.chunk = 64
	_, err := s.Claims(ctx, []Source{sourceOf(t, p)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(s.entries) != 0 {
		t.Fatalf("a failed scan left %d cache entries", len(s.entries))
	}
}

func TestCancelledContextFailsEvenWhenNothingNeedsReading(t *testing.T) {
	f := newFixture(t)
	p := f.write("-slug/a.jsonl", time.Hour, textEvent("x"))
	s := NewScanner(nil)
	src := sourceOf(t, p)
	if _, err := s.Claims(context.Background(), []Source{src}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Claims(ctx, []Source{src}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
