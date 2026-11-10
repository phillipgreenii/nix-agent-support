package serve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
)

// largeSnap is a snapshot size that the earlier 64 KiB floor skipped.
const largeSnap = 64<<10 + 1

// snapEnv is a daemon whose copier opens its source files through a counting
// seam, over one tool-result snapshot file of the given size.
type snapEnv struct {
	e     *env
	d     *Daemon
	cnt   *copier.CountingSrcFS
	path  string
	old   time.Time
	write func(content string)
}

func newSnapEnv(t *testing.T, size int) *snapEnv {
	t.Helper()
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	dir := filepath.Join(e.claude, "projects", "example-proj", "0f8fad5b-d9cb-469f-a165-70867728950e", "tool-results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &snapEnv{e: e, cnt: &copier.CountingSrcFS{}, path: filepath.Join(dir, "out.txt"), old: old}
	s.write = func(content string) {
		t.Helper()
		if err := os.WriteFile(s.path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(s.path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	s.write(strings.Repeat("a", size))
	e.transcript("a.jsonl", "{\"type\":\"user\"}\n", old)
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, SrcFS: s.cnt})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	s.d = d
	rescan(t, d) // first copy
	rescan(t, d) // ingested to unchanged and cached where cacheable
	return s
}

// opensDuring runs one sweep and returns how many source opens it made.
func (s *snapEnv) opensDuring(t *testing.T) int64 {
	t.Helper()
	before := s.cnt.Opens()
	rescan(t, s.d)
	return s.cnt.Opens() - before
}

// An unchanged snapshot file above the size floor is not opened by a sweep;
// one change to it is read and stored as a new version.
func TestUnchangedLargeSnapshotIsNotOpened(t *testing.T) {
	s := newSnapEnv(t, largeSnap)
	// The transcript is skipped too, so a sweep opens nothing at all.
	if n := s.opensDuring(t); n != 0 {
		t.Fatalf("a sweep over unchanged files opened %d source files, want 0", n)
	}
	before := ingestCount(s.d, "snapshot")
	s.write(strings.Repeat("b", largeSnap)) // same size, new content and mtime now
	if err := os.Chtimes(s.path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := s.opensDuring(t); n == 0 {
		t.Fatal("a changed snapshot file was not opened")
	}
	if got := ingestCount(s.d, "snapshot") - before; got != 1 {
		t.Fatalf("changed snapshot stored %v times, want 1", got)
	}
}

// A small, old, unchanged snapshot file (the registry and session-mode JSON
// files: about half of the files a real sweep walks) is skipped without being
// opened, like a large one. This is the regression guard for sweeps that
// re-ingested every small snapshot file every time.
func TestUnchangedSmallSnapshotIsNotOpened(t *testing.T) {
	for _, size := range []int{10, 64 << 10, largeSnap} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := newSnapEnv(t, size)
			for i := range 3 {
				if n := s.opensDuring(t); n != 0 {
					t.Fatalf("sweep %d opened %d source files over unchanged files, want 0", i, n)
				}
			}
			before := ingestCount(s.d, "snapshot")
			s.write(strings.Repeat("b", size)) // same size, new content, mtime restored to old
			if err := os.Chtimes(s.path, time.Now().Add(-30*time.Minute), time.Now().Add(-30*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if n := s.opensDuring(t); n == 0 {
				t.Fatal("a small snapshot file with a new mtime was not opened")
			}
			if got := ingestCount(s.d, "snapshot") - before; got != 1 {
				t.Fatalf("changed snapshot stored %v times, want 1", got)
			}
		})
	}
}

// A snapshot file written within snapshotRacyWindow of the ingest is read
// again by the next sweep (a same-size rewrite could share its mtime), and is
// skipped once its mtime is older than the window.
func TestRacySnapshotIsReadUntilItsMtimeAges(t *testing.T) {
	s := newSnapEnv(t, 10)
	fresh := time.Now()
	s.write("0123456789")
	if err := os.Chtimes(s.path, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	rescan(t, s.d)
	if n := s.opensDuring(t); n == 0 {
		t.Fatal("a snapshot file with a fresh mtime was skipped")
	}
	old := fresh.Add(-time.Hour)
	if err := os.Chtimes(s.path, old, old); err != nil {
		t.Fatal(err)
	}
	rescan(t, s.d) // sees the new mtime, ingests to unchanged, caches
	if n := s.opensDuring(t); n != 0 {
		t.Fatalf("an aged snapshot file was opened %d times, want 0", n)
	}
}

// A registry snapshot whose JSON is invalid is never skipped, however many
// sweeps look at it, and is stored once it becomes valid; padded above the
// floor, so only the validity (not the size gate) keeps it from the cache.
func TestInvalidJSONSnapshotIsNeverSkippedAndIsReadOnceValid(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	p := filepath.Join(e.claude, "sessions", "4242.json")
	pad := strings.Repeat("x", largeSnap)
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	cnt := &copier.CountingSrcFS{}
	write(`{"pid":4242,"pad":"` + pad) // truncated: invalid
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, SrcFS: cnt})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	for i := range 3 {
		before := cnt.Opens()
		rescan(t, d)
		if cnt.Opens() == before {
			t.Fatalf("sweep %d skipped an invalid-JSON snapshot", i)
		}
	}
	if _, ok := d.seen.caught[p]; ok {
		t.Fatal("an invalid-JSON snapshot got a cache entry")
	}
	before := ingestCount(d, "snapshot")
	write(`{"pid":4242,"pad":"` + pad + `"}`)
	rescan(t, d)
	if got := ingestCount(d, "snapshot") - before; got != 1 {
		t.Fatalf("snapshot stored %v times once valid, want 1", got)
	}
}
