package store_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

// INV-LOG-20: exactly one service may write a data directory; a claim left
// behind by a process that is gone does not block.
func TestOpenLocksDirectory(t *testing.T) {
	t.Run("a second Open on a held directory is ErrLocked until the first closes", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		first, _, _, err := store.Open(store.Options{Dir: dir})
		if err != nil {
			t.Fatalf("first Open: %v", err)
		}
		_, _, _, err = store.Open(store.Options{Dir: dir})
		if !errors.Is(err, store.ErrLocked) {
			t.Fatalf("second Open error = %v, want ErrLocked", err)
		}
		if err := first.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		second, _, _, err := store.Open(store.Options{Dir: dir})
		if err != nil {
			t.Fatalf("Open after Close: %v", err)
		}
		if err := second.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("a stale lock file with no holder does not block", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		first, _, _, err := store.Open(store.Options{Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		// Whatever an earlier process left in the directory, including a lock
		// file with content in it, no process holds the lock.
		locks, err := filepath.Glob(filepath.Join(dir, "*.lock"))
		if err != nil || len(locks) != 1 {
			t.Fatalf("lock files after Close = %v (%v), want the one the store left behind", locks, err)
		}
		if err := os.WriteFile(locks[0], []byte("pid 424242\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		again, _, _, err := store.Open(store.Options{Dir: dir})
		if err != nil {
			t.Fatalf("Open with a stale lock file: %v", err)
		}
		_ = again.Close()
	})

	t.Run("a refused Open does not touch the log, not even to recover it", func(t *testing.T) {
		committed := lines(t, plainEvent(1))
		dir := seedLog(t, committed)
		holder, _, _ := openStore(t, dir, nil)
		_ = holder

		// The holder is alive and a torn tail appears (another writer, or the
		// holder mid-append): the loser must leave it alone.
		torn := append(bytes.Clone(committed), []byte(`{"v":1,"id":"01J9Z3K8M2E00`)...)
		if err := os.WriteFile(logPath(dir), torn, 0o600); err != nil {
			t.Fatal(err)
		}
		fs := storefault.New(nil)
		_, _, _, err := store.Open(store.Options{Dir: dir, FS: fs})
		if !errors.Is(err, store.ErrLocked) {
			t.Fatalf("Open error = %v, want ErrLocked", err)
		}
		if got := readLog(t, dir); !bytes.Equal(got, torn) {
			t.Errorf("a refused Open changed the log")
		}
		if got := mutating(fs); len(got) != 0 {
			t.Errorf("a refused Open wrote: %v", got)
		}
		if got := sidecars(t, dir); len(got) != 0 {
			t.Errorf("a refused Open made sidecars: %v", got)
		}
	})

	t.Run("an Open that fails releases the claim", func(t *testing.T) {
		// Corrupt: a bad line followed by another line.
		dir := seedLog(t, append([]byte("not an event\n"), lines(t, plainEvent(1))...))
		for range 2 {
			_, _, _, err := store.Open(store.Options{Dir: dir})
			var corrupt *store.CorruptError
			if !errors.As(err, &corrupt) {
				t.Fatalf("Open error = %v, want a *CorruptError every time, never ErrLocked", err)
			}
		}
	})
}

// INTF-LOG: the log is events.jsonl in the data directory, created empty with
// the directory when absent.
func TestOpenCreatesEmptyLogAndDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "share", "pg-task-focus")
	s, evs, rec, err := store.Open(store.Options{Dir: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("data directory not created: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("data directory mode = %v, want 0700", info.Mode().Perm())
	}
	logInfo, err := os.Stat(logPath(dir))
	if err != nil {
		t.Fatalf("log not created: %v", err)
	}
	if logInfo.Size() != 0 {
		t.Errorf("new log has %d bytes, want 0", logInfo.Size())
	}
	if logInfo.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v, want 0600", logInfo.Mode().Perm())
	}
	if len(evs) != 0 {
		t.Errorf("events = %d, want none", len(evs))
	}
	if rec != (store.Recovery{}) {
		t.Errorf("Recovery = %+v, want the zero value", rec)
	}
	if s.Size() != 0 {
		t.Errorf("Size = %d, want 0", s.Size())
	}
}

func TestOpenRequiresADirectory(t *testing.T) {
	if _, _, _, err := store.Open(store.Options{}); err == nil {
		t.Fatal("Open with no Dir succeeded")
	}
}

func TestOpenReturnsTheCommittedEventsInOrder(t *testing.T) {
	b := batchOf(3, 2, batchID(1))
	all := append([]event.Event{plainEvent(1), plainEvent(2)}, b...)
	dir := seedLog(t, lines(t, all...))

	s, evs, rec := openStore(t, dir, nil)
	if !slices.Equal(ids(evs), ids(all)) {
		t.Fatalf("events = %v, want %v", ids(evs), ids(all))
	}
	for i, e := range evs {
		if e.Line != i+1 {
			t.Errorf("event %d Line = %d, want %d", i, e.Line, i+1)
		}
	}
	if rec != (store.Recovery{}) {
		t.Errorf("Recovery = %+v, want the zero value for a clean log", rec)
	}
	if want := int64(len(lines(t, all...))); s.Size() != want {
		t.Errorf("Size = %d, want %d", s.Size(), want)
	}
}

// INV-LOG-1 and the rollback design: the log is opened with O_APPEND, so a
// write after a rollback truncate cannot leave a hole at the old offset.
func TestLogIsOpenedWithOAppend(t *testing.T) {
	dir := seedLog(t, lines(t, plainEvent(1)))
	fs := storefault.New(nil)
	openStore(t, dir, fs)

	var opens []storefault.Call
	for _, c := range fs.Calls() {
		if c.Op == storefault.OpOpenFile && filepath.Base(c.Name) == "events.jsonl" {
			opens = append(opens, c)
		}
	}
	if len(opens) != 1 {
		t.Fatalf("the log was opened %d times, want once: %v", len(opens), opens)
	}
	if opens[0].Flag&os.O_APPEND == 0 {
		t.Errorf("the log was opened with flag %#x, which lacks O_APPEND", opens[0].Flag)
	}
}

// INV-LOG-10 and INV-LOG-29: a log file that has just been created is only
// durable once its directory entry is, so a start that creates the log syncs
// the directory, through the FS seam, after creating the file.
func TestFirstCreationOfTheLogSyncsTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	fs := storefault.New(nil)
	openStore(t, dir, fs)

	var got []string
	for _, c := range fs.Calls() {
		switch {
		case c.Op == storefault.OpOpenFile && c.Name == logPath(dir):
			if c.Flag&os.O_CREATE == 0 {
				t.Errorf("the log was opened with flag %#x, which lacks O_CREATE", c.Flag)
			}
			got = append(got, "create the log")
		case c.Op == storefault.OpOpenFile && c.Name == dir:
			if c.Flag != os.O_RDONLY {
				t.Errorf("the directory was opened with flag %#x, want O_RDONLY", c.Flag)
			}
			got = append(got, "open the directory")
		case c.Op == storefault.OpSync && c.Name == dir:
			got = append(got, "sync the directory")
		case c.Op == storefault.OpClose && c.Name == dir:
			got = append(got, "close the directory")
		case c.Op == storefault.OpSync:
			t.Errorf("unexpected sync of %s on a first start", c.Name)
		}
	}
	want := []string{"create the log", "open the directory", "sync the directory", "close the directory"}
	if !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestFirstCreationOfTheLogReportsAFailedDirectorySync(t *testing.T) {
	cases := map[string]storefault.Rule{
		"the sync fails":              {Op: storefault.OpSync},
		"the directory will not open": {Op: storefault.OpOpenFile, Name: "data", Nth: 2},
	}
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			fs := storefault.New(nil)
			fs.Inject(rule)
			_, _, _, err := store.Open(store.Options{Dir: dir, FS: fs, Now: fixedNow})
			if !errors.Is(err, storefault.ErrInjected) {
				t.Fatalf("Open error = %v, want the injected failure", err)
			}
			if fs.Pending() != 0 {
				t.Fatal("the fault was never reached")
			}
			// The claim was released and the next start succeeds.
			openStore(t, dir, nil)
		})
	}
}

// A log that already holds records was made durable by the start that created
// it, and a clean start writes nothing, so it syncs nothing.
func TestOpenOfAnExistingLogDoesNotSyncTheDirectory(t *testing.T) {
	dir := seedLog(t, lines(t, plainEvent(1)))
	fs := storefault.New(nil)
	openStore(t, dir, fs)
	if got := mutating(fs); len(got) != 0 {
		t.Errorf("a clean start of an existing log wrote or synced: %v", got)
	}
}

func TestCloseIsIdempotentAndReleasesTheClaim(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, _, _, err := store.Open(store.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

func TestOpenReportsAFailureToCreateTheDirectory(t *testing.T) {
	fs := storefault.New(nil)
	fs.Inject(storefault.Rule{Op: storefault.OpMkdirAll})
	_, _, _, err := store.Open(store.Options{Dir: filepath.Join(t.TempDir(), "data"), FS: fs})
	if !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("Open error = %v, want the MkdirAll failure", err)
	}
}

func TestOpenReportsAFailureToOpenTheLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	fs := storefault.New(nil)
	fs.Inject(storefault.Rule{Op: storefault.OpOpenFile, Name: "events.jsonl"})
	_, _, _, err := store.Open(store.Options{Dir: dir, FS: fs})
	if !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("Open error = %v, want the OpenFile failure", err)
	}
	// The claim was released: a plain Open now succeeds.
	s, _, _, err := store.Open(store.Options{Dir: dir})
	if err != nil {
		t.Fatalf("Open after the failure: %v", err)
	}
	_ = s.Close()
}
