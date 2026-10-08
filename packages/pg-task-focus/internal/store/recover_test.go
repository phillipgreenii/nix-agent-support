package store_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

var sidecarName = regexp.MustCompile(`^events\.jsonl\.recovered-20261008T123045Z-(\d+)$`)

// committedLog is a log of a plain event and a committed batch of two, and
// the ids of its events.
func committedLog(t testing.TB) ([]byte, []event.Event) {
	t.Helper()
	all := append([]event.Event{plainEvent(1)}, batchOf(2, 2, batchID(1))...)
	return lines(t, all...), all
}

func fixtureLine(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "logs", "store", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// wantRecoveredLog asserts everything INV-LOG-9 and INV-LOG-10 promise about a
// recovery of the tail: the order of the writes, the sidecar and the log.
func wantRecoveredLog(t *testing.T, dir string, fs *storefault.FS, rec store.Recovery, committed, tail []byte) {
	t.Helper()
	// INV-LOG-10: the unacknowledged bytes are copied and made durable BEFORE
	// the truncate, and the truncate is made durable.
	sidecar := "events.jsonl.recovered-20261008T123045Z-1"
	wantOrder := []string{"Write " + sidecar, "Sync " + sidecar, "Truncate events.jsonl", "Sync events.jsonl"}
	if got := mutating(fs); !slices.Equal(got, wantOrder) {
		t.Errorf("recovery calls = %v, want %v", got, wantOrder)
	}
	// INV-LOG-1: the only change to the log is the truncation to the end of
	// the last committed record.
	if got := readLog(t, dir); !bytes.Equal(got, committed) {
		t.Errorf("the log holds %d bytes, want exactly the %d committed bytes", len(got), len(committed))
	}
	names := sidecars(t, dir)
	if len(names) != 1 || !sidecarName.MatchString(names[0]) {
		t.Fatalf("sidecars = %v, want one named events.jsonl.recovered-<UTC stamp>-<n>", names)
	}
	path := filepath.Join(dir, names[0])
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("sidecar mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, tail) {
		t.Errorf("the sidecar holds %q, want the unacknowledged bytes %q", data, tail)
	}
	if rec.TruncatedBytes != int64(len(tail)) {
		t.Errorf("Recovery.TruncatedBytes = %d, want %d", rec.TruncatedBytes, len(tail))
	}
	if rec.Sidecar != path {
		t.Errorf("Recovery.Sidecar = %q, want %q", rec.Sidecar, path)
	}
}

// INV-LOG-9, INV-LOG-10 (and INV-LOG-1): a final line that was never
// acknowledged is copied aside, then truncated away.
func TestRecoverTornTail(t *testing.T) {
	tail := lines(t, plainEvent(9))
	cases := map[string][]byte{
		"a final line without its newline":                         tail[:len(tail)-20],
		"a final line cut mid-key":                                 fixtureLine(t, "truncated-line.jsonl"),
		"a complete garbage line":                                  fixtureLine(t, "garbage-line.jsonl"),
		"a complete line of a known version that fails its schema": fixtureLine(t, "schema-fail-line.jsonl"),
		"a blank final line":                                       []byte("\n"),
	}
	for name, torn := range cases {
		t.Run(name, func(t *testing.T) {
			committed, all := committedLog(t)
			dir := seedLog(t, append(bytes.Clone(committed), torn...))
			fs := storefault.New(nil)

			s, evs, rec := openStore(t, dir, fs)
			if !slices.Equal(ids(evs), ids(all)) {
				t.Errorf("events = %v, want %v", ids(evs), ids(all))
			}
			if !rec.TornTail || rec.UncommittedBatches != 0 {
				t.Errorf("Recovery = %+v, want TornTail only", rec)
			}
			wantRecoveredLog(t, dir, fs, rec, committed, torn)
			if s.Size() != int64(len(committed)) {
				t.Errorf("Size = %d, want %d", s.Size(), len(committed))
			}

			// A following Append yields a clean log: the new line follows the
			// committed bytes directly, with nothing of the torn tail between.
			next := plainEvent(10)
			if _, err := s.Append([]event.Event{next}); err != nil {
				t.Fatalf("Append after recovery: %v", err)
			}
			if got, want := readLog(t, dir), append(bytes.Clone(committed), line(t, next)...); !bytes.Equal(got, want) {
				t.Errorf("the log after the Append is not the committed bytes plus the new line")
			}

			// The recovery does not repeat: the next start finds a clean log.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			_, again, rec2 := openStore(t, dir, nil)
			if rec2 != (store.Recovery{}) {
				t.Errorf("second Open recovered again: %+v", rec2)
			}
			if want := append(slices.Clone(all), next); !slices.Equal(ids(again), ids(want)) {
				t.Errorf("second Open events = %v, want %v", ids(again), ids(want))
			}
		})
	}
}

// INV-LOG-9, INV-LOG-10: the events of a batch that never got its
// batch.committed were never acknowledged.
func TestRecoverUncommittedBatch(t *testing.T) {
	committed, all := committedLog(t)
	members := lines(t, memberEvent(5, batchID(2)), memberEvent(6, batchID(2)))
	torn := []byte(`{"v":1,"id":"01J9Z`)

	t.Run("an open batch at the end", func(t *testing.T) {
		dir := seedLog(t, append(bytes.Clone(committed), members...))
		fs := storefault.New(nil)

		s, evs, rec := openStore(t, dir, fs)
		if !slices.Equal(ids(evs), ids(all)) {
			t.Errorf("events = %v, want only the committed %v", ids(evs), ids(all))
		}
		if rec.UncommittedBatches != 1 || rec.TornTail {
			t.Errorf("Recovery = %+v, want UncommittedBatches 1 and no torn tail", rec)
		}
		wantRecoveredLog(t, dir, fs, rec, committed, members)

		// The batch id of the dropped members is free again, and the log
		// after a following Append is clean.
		again := batchOf(5, 2, batchID(2))
		if _, err := s.Append(again); err != nil {
			t.Fatalf("Append after recovery: %v", err)
		}
		if got, want := readLog(t, dir), append(bytes.Clone(committed), lines(t, again...)...); !bytes.Equal(got, want) {
			t.Errorf("the log after the Append is not the committed bytes plus the new batch")
		}
	})

	t.Run("an open batch followed by a torn line reports both", func(t *testing.T) {
		tail := append(bytes.Clone(members), torn...)
		dir := seedLog(t, append(bytes.Clone(committed), tail...))
		fs := storefault.New(nil)

		_, _, rec := openStore(t, dir, fs)
		if rec.UncommittedBatches != 1 || !rec.TornTail {
			t.Errorf("Recovery = %+v, want both a torn tail and an uncommitted batch", rec)
		}
		wantRecoveredLog(t, dir, fs, rec, committed, tail)
	})
}

// INV-LOG-10: a crash in the middle of recovery is survivable by recovering
// again.
func TestRecoverIsIdempotentAfterCrashMidRecovery(t *testing.T) {
	committed, all := committedLog(t)
	tail := []byte(`{"v":1,"id":"01J9Z3K8M2E`)
	original := append(bytes.Clone(committed), tail...)

	t.Run("a crash between the sidecar sync and the truncate", func(t *testing.T) {
		dir := seedLog(t, original)
		fs := storefault.New(nil)
		fs.Inject(storefault.Rule{Op: storefault.OpTruncate})
		_, _, _, err := store.Open(store.Options{Dir: dir, FS: fs, Now: fixedNow})
		if !errors.Is(err, storefault.ErrInjected) {
			t.Fatalf("Open error = %v, want the injected Truncate failure", err)
		}
		if fs.Pending() != 0 {
			t.Fatal("the fault was never reached")
		}
		// Nothing was lost and the log was not changed by the failed attempt.
		if got := readLog(t, dir); !bytes.Equal(got, original) {
			t.Errorf("the failed recovery changed the log")
		}
		if got := sidecars(t, dir); len(got) != 1 {
			t.Fatalf("sidecars after the crash = %v, want the one already synced", got)
		}

		// The restart recovers again, into a second sidecar, never over the first.
		fs2 := storefault.New(nil)
		_, evs, rec := openStore(t, dir, fs2)
		if !slices.Equal(ids(evs), ids(all)) {
			t.Errorf("events after the second recovery = %v, want %v", ids(evs), ids(all))
		}
		if !rec.TornTail {
			t.Errorf("Recovery = %+v, want TornTail", rec)
		}
		names := sidecars(t, dir)
		slices.Sort(names)
		want := []string{
			"events.jsonl.recovered-20261008T123045Z-1",
			"events.jsonl.recovered-20261008T123045Z-2",
		}
		if !slices.Equal(names, want) {
			t.Fatalf("sidecars = %v, want %v", names, want)
		}
		for _, n := range names {
			data, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, tail) {
				t.Errorf("%s holds %q, want %q", n, data, tail)
			}
		}
		if got := readLog(t, dir); !bytes.Equal(got, committed) {
			t.Errorf("the log after the second recovery is not the committed prefix")
		}
	})

	t.Run("a failed sidecar sync never truncates", func(t *testing.T) {
		dir := seedLog(t, original)
		fs := storefault.New(nil)
		fs.Inject(storefault.Rule{Op: storefault.OpSync})
		_, _, _, err := store.Open(store.Options{Dir: dir, FS: fs, Now: fixedNow})
		if !errors.Is(err, storefault.ErrInjected) {
			t.Fatalf("Open error = %v, want the injected Sync failure", err)
		}
		for _, c := range fs.Calls() {
			if c.Op == storefault.OpTruncate {
				t.Fatalf("the log was truncated although the sidecar was not durable: %v", fs.Calls())
			}
		}
		if got := readLog(t, dir); !bytes.Equal(got, original) {
			t.Errorf("the log changed although the sidecar was not durable")
		}
	})

	t.Run("a failed sidecar write leaves the log alone", func(t *testing.T) {
		dir := seedLog(t, original)
		fs := storefault.New(nil)
		fs.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: 5})
		_, _, _, err := store.Open(store.Options{Dir: dir, FS: fs, Now: fixedNow})
		if !errors.Is(err, storefault.ErrInjected) {
			t.Fatalf("Open error = %v, want the injected Write failure", err)
		}
		if got := readLog(t, dir); !bytes.Equal(got, original) {
			t.Errorf("the log changed although the sidecar write failed")
		}
		// A retry still recovers, and the half-written sidecar is not left to
		// be mistaken for the real copy.
		_, evs, _ := openStore(t, dir, nil)
		if !slices.Equal(ids(evs), ids(all)) {
			t.Errorf("events = %v, want %v", ids(evs), ids(all))
		}
		for _, n := range sidecars(t, dir) {
			data, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, tail) {
				t.Errorf("%s holds %q, want the whole tail %q", n, data, tail)
			}
		}
	})
}

// INV-LOG-10: a sidecar is never overwritten, and the name carries the
// stamp and a counter.
func TestRecoverNeverOverwritesASidecar(t *testing.T) {
	committed, _ := committedLog(t)
	tail := []byte("torn")
	dir := seedLog(t, append(bytes.Clone(committed), tail...))
	taken := filepath.Join(dir, "events.jsonl.recovered-20261008T123045Z-1")
	precious := []byte("an earlier recovery's bytes")
	if err := os.WriteFile(taken, precious, 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, rec := openStore(t, dir, nil)
	if got, _ := os.ReadFile(taken); !bytes.Equal(got, precious) {
		t.Errorf("an earlier sidecar was overwritten: %q", got)
	}
	if want := filepath.Join(dir, "events.jsonl.recovered-20261008T123045Z-2"); rec.Sidecar != want {
		t.Errorf("Recovery.Sidecar = %q, want %q", rec.Sidecar, want)
	}
}

// INV-LOG-2, INV-LOG-11: damage that is not the tail, and a version this
// build does not know, refuse to start and leave the file untouched.
func TestOpenRefusesCorruption(t *testing.T) {
	committed, _ := committedLog(t)
	garbage := fixtureLine(t, "garbage-line.jsonl")
	v2 := fixtureLine(t, "unknown-version-line.jsonl")

	cases := []struct {
		name        string
		log         []byte
		wantCorrupt int // the 1-based line of the *CorruptError; 0 when none
		wantVersion int // the line of the *UnknownVersionError; 0 when none
	}{
		{"a torn line followed by a line", join(committed, garbage, lines(t, plainEvent(9))), 5, 0},
		{"an uncommitted batch followed by a plain event", join(committed, lines(t, memberEvent(5, batchID(2))), lines(t, plainEvent(9))), 6, 0},
		{"a batch.committed with no open batch", join(committed, lines(t, commitEvent(7, batchID(3)))), 5, 0},
		{"an unknown version in the middle", join(committed, v2, lines(t, plainEvent(9))), 0, 5},
		{"an unknown version as the final line", join(committed, v2), 0, 5},
		{"a line over the size limit", join(committed, bytes.Repeat([]byte("a"), event.MaxEventBytes+1), []byte("\n")), 5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := seedLog(t, tc.log)
			fs := storefault.New(nil)
			_, _, _, err := store.Open(store.Options{Dir: dir, FS: fs, Now: fixedNow})

			var corrupt *store.CorruptError
			var version *store.UnknownVersionError
			switch {
			case tc.wantCorrupt != 0:
				if !errors.As(err, &corrupt) || corrupt.Line != tc.wantCorrupt {
					t.Fatalf("Open error = %v (%T), want a *CorruptError at line %d", err, err, tc.wantCorrupt)
				}
			default:
				if !errors.As(err, &version) || version.Line != tc.wantVersion || version.V != 2 {
					t.Fatalf("Open error = %v (%T), want an *UnknownVersionError at line %d", err, err, tc.wantVersion)
				}
				if errors.As(err, &corrupt) {
					t.Errorf("an unknown version must not be reported as corruption")
				}
			}
			// Never repaired: not modified, no sidecar, no write of any kind.
			if got := readLog(t, dir); !bytes.Equal(got, tc.log) {
				t.Errorf("the refused log was modified")
			}
			if got := sidecars(t, dir); len(got) != 0 {
				t.Errorf("sidecars = %v, want none", got)
			}
			if got := mutating(fs); len(got) != 0 {
				t.Errorf("a refused Open wrote: %v", got)
			}
		})
	}
}

func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// INV-LOG-10: every step of recovery that fails refuses to start, and what
// has not been made durable has not been acted on.
func TestRecoveryFailuresRefuseToStart(t *testing.T) {
	committed, all := committedLog(t)
	tail := []byte(`{"v":1,"id":"01J9Z3K8M2E`)
	original := append(bytes.Clone(committed), tail...)

	cases := []struct {
		name string
		rule storefault.Rule
		// logCut says whether the failed attempt had already truncated the log.
		logCut bool
	}{
		{"reading the unacknowledged bytes", storefault.Rule{Op: storefault.OpReadAt, Nth: 2}, false},
		{"creating the sidecar", storefault.Rule{Op: storefault.OpOpenFile, Name: "recovered"}, false},
		{"closing the sidecar", storefault.Rule{Op: storefault.OpClose, Name: "recovered"}, false},
		{"syncing the truncated log", storefault.Rule{Op: storefault.OpSync, Nth: 2}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := seedLog(t, original)
			fs := storefault.New(nil)
			fs.Inject(tc.rule)
			_, _, _, err := store.Open(store.Options{Dir: dir, FS: fs, Now: fixedNow})
			if !errors.Is(err, storefault.ErrInjected) {
				t.Fatalf("Open error = %v, want the injected failure", err)
			}
			if fs.Pending() != 0 {
				t.Fatal("the fault was never reached")
			}
			want := original
			if tc.logCut {
				want = committed
			}
			if got := readLog(t, dir); !bytes.Equal(got, want) {
				t.Errorf("the log holds %d bytes, want %d", len(got), len(want))
			}

			// The next start succeeds and loses nothing.
			_, evs, _ := openStore(t, dir, nil)
			if !slices.Equal(ids(evs), ids(all)) {
				t.Errorf("events after the retry = %v, want %v", ids(evs), ids(all))
			}
			if got := readLog(t, dir); !bytes.Equal(got, committed) {
				t.Errorf("the log after the retry is not the committed prefix")
			}
		})
	}
}
