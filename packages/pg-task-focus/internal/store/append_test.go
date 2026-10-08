package store_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

// ticker is a settable clock for Options.Now.
type ticker struct {
	mu  sync.Mutex
	now time.Time
}

func (c *ticker) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ticker) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// rig is an open store over a seeded log, through a fault FS whose call log
// starts empty, with a clock the test steps.
type rig struct {
	s     *store.Store
	fs    *storefault.FS
	clock *ticker
	dir   string
	seed  []byte
}

func newRig(t testing.TB) *rig {
	t.Helper()
	seed, _ := committedLog(t)
	dir := seedLog(t, seed)
	fs := storefault.New(nil)
	clock := &ticker{now: failureAt}
	s, _, _, err := store.Open(store.Options{Dir: dir, FS: fs, Now: clock.Now})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	fs.ResetCalls()
	return &rig{s: s, fs: fs, clock: clock, dir: dir, seed: seed}
}

// batch is a committed batch of two numbered from first.
func batch(first int, b event.ID) []event.Event { return batchOf(first, 2, b) }

func wantAppendError(t *testing.T, err error, stage store.AppendStage) *store.AppendError {
	t.Helper()
	var ae *store.AppendError
	if !errors.As(err, &ae) {
		t.Fatalf("Append error = %v (%T), want an *AppendError", err, err)
	}
	if ae.Stage != stage {
		t.Errorf("AppendError.Stage = %q, want %q", ae.Stage, stage)
	}
	return ae
}

func wantRefused(t *testing.T, r *rig) {
	t.Helper()
	r.fs.ResetCalls()
	_, err := r.s.Append([]event.Event{plainEvent(90)})
	if !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("Append on a read-only store = %v, want ErrStoreUnavailable", err)
	}
	if got := r.fs.Calls(); len(got) != 0 {
		t.Errorf("a refused Append touched the file system: %v", got)
	}
}

// INV-LOG-29 and INV-LOG-9: a batch is written with one Write of
// newline-terminated lines, then one Sync, and Append returns after that.
func TestAppendWritesAllLinesOneSyscallAndSyncs(t *testing.T) {
	r := newRig(t)
	b := batch(10, batchID(5))
	want := lines(t, b...)

	stats, err := r.s.Append(b)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	log := filepath.Join(r.dir, "events.jsonl")
	wantCalls := []string{"Write events.jsonl", "Sync events.jsonl"}
	if got := mutating(r.fs); !slices.Equal(got, wantCalls) {
		t.Errorf("calls = %v, want one Write then one Sync %v", got, wantCalls)
	}
	for _, c := range r.fs.Calls() {
		if c.Op == storefault.OpWrite && (c.Name != log || c.Size != int64(len(want))) {
			t.Errorf("the Write was %+v, want all %d bytes to the log", c, len(want))
		}
	}
	if got := readLog(t, r.dir); !bytes.Equal(got, append(bytes.Clone(r.seed), want...)) {
		t.Errorf("the log is not the seed followed by the %d new lines", len(b))
	}
	if stats.Events != 3 || stats.Bytes != int64(len(want)) {
		t.Errorf("AppendStats = %+v, want 3 events and %d bytes", stats, len(want))
	}
	if r.s.Size() != int64(len(r.seed)+len(want)) {
		t.Errorf("Size = %d, want %d", r.s.Size(), len(r.seed)+len(want))
	}
	if h := r.s.Health(); h != (store.Health{}) {
		t.Errorf("Health = %+v after a good append, want the zero value", h)
	}
}

func TestAppendOfNothingDoesNothing(t *testing.T) {
	r := newRig(t)
	if _, err := r.s.Append(nil); err != nil {
		t.Fatal(err)
	}
	if got := r.fs.Calls(); len(got) != 0 {
		t.Errorf("an empty Append touched the file system: %v", got)
	}
}

func TestAppendRefusesAnEventItCannotEncodeBeforeWriting(t *testing.T) {
	r := newRig(t)
	bad := event.Event{Envelope: envelope(20), Payload: event.CycleAnnotated{CycleID: event.CycleID(newID('c', 1)), Note: "\xff"}}
	_, err := r.s.Append([]event.Event{plainEvent(19), bad})
	if !errors.Is(err, event.ErrInvalidUTF8) {
		t.Fatalf("Append error = %v, want the encoding failure", err)
	}
	if got := r.fs.Calls(); len(got) != 0 {
		t.Errorf("an unencodable event reached the file system: %v", got)
	}
	if !r.s.Writable() || r.s.Health() != (store.Health{}) {
		t.Errorf("an encoding failure must not touch the store's health")
	}
}

func TestAppendAfterCloseIsRefused(t *testing.T) {
	r := newRig(t)
	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	if r.s.Writable() {
		t.Error("a closed store reports Writable")
	}
	if _, err := r.s.Append([]event.Event{plainEvent(30)}); !errors.Is(err, store.ErrStoreUnavailable) {
		t.Errorf("Append on a closed store = %v, want ErrStoreUnavailable", err)
	}
}

// INV-LOG-21: a failed write is rolled back to the size before the append and
// that is made durable; the store stays writable.
func TestAppendWriteFailureRollsBack(t *testing.T) {
	r := newRig(t)
	r.fs.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: 40})

	_, err := r.s.Append(batch(10, batchID(5)))
	ae := wantAppendError(t, err, store.StageWrite)
	if !errors.Is(ae, storefault.ErrInjected) {
		t.Errorf("the AppendError does not wrap the write failure: %v", ae)
	}

	wantCalls := []string{"Write events.jsonl", "Truncate events.jsonl", "Sync events.jsonl"}
	if got := mutating(r.fs); !slices.Equal(got, wantCalls) {
		t.Errorf("calls = %v, want the write, then the rollback truncate and its sync %v", got, wantCalls)
	}
	for _, c := range r.fs.Calls() {
		if c.Op == storefault.OpTruncate && c.Size != int64(len(r.seed)) {
			t.Errorf("the rollback truncated to %d, want the pre-append size %d", c.Size, len(r.seed))
		}
	}
	if got := readLog(t, r.dir); !bytes.Equal(got, r.seed) {
		t.Errorf("the log holds %d bytes after the rollback, want the %d it had", len(got), len(r.seed))
	}
	if r.s.Size() != int64(len(r.seed)) {
		t.Errorf("Size = %d, want %d", r.s.Size(), len(r.seed))
	}
	if !r.s.Writable() || r.s.Health() != (store.Health{}) {
		t.Errorf("a write that rolled back cleanly must leave the store writable and healthy, got %+v", r.s.Health())
	}

	// The retry works.
	if _, err := r.s.Append(batch(10, batchID(5))); err != nil {
		t.Fatalf("Append after the rollback: %v", err)
	}
}

// INV-LOG-21, INV-LOG-23: the sync of the append failed, so the file's state
// is unknown: the store tries to take the append back out and goes read-only.
func TestAppendFsyncFailureEntersReadOnly(t *testing.T) {
	r := newRig(t)
	r.fs.Inject(storefault.Rule{Op: storefault.OpSync})

	_, err := r.s.Append(batch(10, batchID(5)))
	_ = wantAppendError(t, err, store.StageSync)

	wantCalls := []string{"Write events.jsonl", "Sync events.jsonl", "Truncate events.jsonl", "Sync events.jsonl"}
	if got := mutating(r.fs); !slices.Equal(got, wantCalls) {
		t.Errorf("calls = %v, want the append, then an attempted rollback %v", got, wantCalls)
	}
	if r.s.Writable() {
		t.Error("Writable is true after a failed append fsync")
	}
	if got := readLog(t, r.dir); !bytes.Equal(got, r.seed) {
		t.Errorf("the attempted rollback did not restore the log")
	}
	wantRefused(t, r)
}

// INV-LOG-21, INV-LOG-23: when the rollback after a failed fsync fails as
// well, the first cause (the fsync) is the one kept, and the error carries
// both failures.
func TestFsyncFailureKeepsItsReasonWhenTheRollbackFailsToo(t *testing.T) {
	r := newRig(t)
	r.fs.Inject(storefault.Rule{Op: storefault.OpSync})
	r.fs.Inject(storefault.Rule{Op: storefault.OpTruncate, Err: errors.New("rollback refused")})

	_, err := r.s.Append(batch(10, batchID(5)))
	ae := wantAppendError(t, err, store.StageSync)
	if !errors.Is(ae, storefault.ErrInjected) {
		t.Errorf("the error lost the fsync failure: %v", ae)
	}
	if !strings.Contains(ae.Error(), "rollback refused") || !strings.Contains(ae.Error(), "sync") {
		t.Errorf("message %q does not name both the step and the rollback failure", ae.Error())
	}
	if h := r.s.Health(); !h.ReadOnly || h.Reason != store.ReasonAppendSync {
		t.Errorf("Health = %+v, want the fsync as the first cause", h)
	}
}

// INV-LOG-21: a rollback that cannot be completed also enters read-only mode.
func TestRollbackFailureEntersReadOnly(t *testing.T) {
	cases := map[string]storefault.Rule{
		"the truncate fails":             {Op: storefault.OpTruncate},
		"the sync of the rollback fails": {Op: storefault.OpSync},
	}
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.fs.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: 40})
			r.fs.Inject(rule)

			_, err := r.s.Append(batch(10, batchID(5)))
			ae := wantAppendError(t, err, store.StageWrite)
			if !errors.Is(ae, storefault.ErrInjected) {
				t.Errorf("the AppendError does not wrap the failures: %v", ae)
			}
			if r.fs.Pending() != 0 {
				t.Fatal("a fault was never reached")
			}
			if r.s.Writable() {
				t.Error("Writable is true after a failed rollback")
			}
			wantRefused(t, r)
		})
	}
}

// INV-LOG-23: read-only mode ends only by reopening, which runs recovery;
// nothing else, retries included, clears it.
func TestReadOnlyClearedOnlyByReopen(t *testing.T) {
	r := newRig(t)
	r.fs.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: 40})
	r.fs.Inject(storefault.Rule{Op: storefault.OpTruncate})
	if _, err := r.s.Append(batch(10, batchID(5))); err == nil {
		t.Fatal("the faulted Append succeeded")
	}
	// The failed rollback left the partial bytes in the log.
	if got := readLog(t, r.dir); len(got) <= len(r.seed) {
		t.Fatalf("the log holds %d bytes, want the partial write left behind", len(got))
	}

	for range 3 {
		wantRefused(t, r) // retries do not clear the mode
	}
	if r.s.Probe() != nil {
		t.Error("Probe failed on a healthy file system")
	}
	if !r.s.Health().ReadOnly {
		t.Error("a successful Probe cleared read-only mode")
	}

	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, evs, rec, err := store.Open(store.Options{Dir: r.dir, Now: fixedNow})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if !rec.TornTail && rec.UncommittedBatches == 0 {
		t.Errorf("reopening did not run recovery: %+v", rec)
	}
	if len(evs) != 4 {
		t.Errorf("reopened with %d events, want the 4 acknowledged ones", len(evs))
	}
	if h := s2.Health(); h != (store.Health{}) {
		t.Errorf("Health after reopening = %+v, want the zero value", h)
	}
	if !s2.Writable() {
		t.Error("the reopened store is not writable")
	}
	if _, err := s2.Append(batch(10, batchID(5))); err != nil {
		t.Fatalf("Append after reopening: %v", err)
	}
}

// INV-LOG-21, INV-LOG-22, INV-LOG-23: the store keeps the first cause, as one
// of the four closed sentences, and the instant it began.
func TestHealthReportsReadOnlyReasonAndSince(t *testing.T) {
	t.Run("the four sentences are exactly the closed set", func(t *testing.T) {
		want := map[store.ReadOnlyReason]string{
			store.ReasonWriteRollbackTruncate: "the append write failed and the rollback could not truncate the log",
			store.ReasonWriteRollbackSync:     "the append write failed and the rollback could not be synced",
			store.ReasonAppendSync:            "the append fsync failed",
			store.ReasonAdopt:                 "the new state could not be adopted after a durable append",
		}
		for got, text := range want {
			if string(got) != text {
				t.Errorf("reason = %q, want %q", got, text)
			}
		}
	})

	cases := []struct {
		name   string
		rules  []storefault.Rule
		reason store.ReadOnlyReason
	}{
		{
			"the append sync fails",
			[]storefault.Rule{{Op: storefault.OpSync}},
			store.ReasonAppendSync,
		},
		{
			"a write fault followed by a truncate fault",
			[]storefault.Rule{{Op: storefault.OpWrite, Partial: 10}, {Op: storefault.OpTruncate}},
			store.ReasonWriteRollbackTruncate,
		},
		{
			"a write fault followed by a rollback sync fault",
			[]storefault.Rule{{Op: storefault.OpWrite, Partial: 10}, {Op: storefault.OpSync}},
			store.ReasonWriteRollbackSync,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			if h := r.s.Health(); h != (store.Health{}) {
				t.Fatalf("a healthy store reports %+v, want the zero value", h)
			}
			failed := time.Date(2026, 10, 8, 14, 1, 2, 345_000_000, time.UTC)
			r.clock.set(failed)
			for _, rule := range tc.rules {
				r.fs.Inject(rule)
			}
			if _, err := r.s.Append(batch(10, batchID(5))); err == nil {
				t.Fatal("the faulted Append succeeded")
			}
			want := store.Health{ReadOnly: true, Reason: tc.reason, Since: failed}
			if h := r.s.Health(); h != want {
				t.Fatalf("Health = %+v, want %+v", h, want)
			}

			// A later failure and a later MarkReadOnly change neither the
			// reason nor the instant.
			r.clock.set(failed.Add(time.Hour))
			r.s.MarkReadOnly(store.ReasonAdopt)
			if _, err := r.s.Append(batch(20, batchID(6))); !errors.Is(err, store.ErrStoreUnavailable) {
				t.Fatalf("later Append = %v, want ErrStoreUnavailable", err)
			}
			if h := r.s.Health(); h != want {
				t.Errorf("Health after later failures = %+v, want it unchanged %+v", h, want)
			}
		})
	}

	t.Run("MarkReadOnly records its sentence and keeps the first cause", func(t *testing.T) {
		r := newRig(t)
		first := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
		r.clock.set(first)
		r.s.MarkReadOnly(store.ReasonAdopt)
		want := store.Health{ReadOnly: true, Reason: store.ReasonAdopt, Since: first}
		if h := r.s.Health(); h != want {
			t.Fatalf("Health = %+v, want %+v", h, want)
		}
		if r.s.Writable() {
			t.Error("Writable is true after MarkReadOnly")
		}
		r.clock.set(first.Add(time.Minute))
		r.s.MarkReadOnly(store.ReasonAppendSync)
		if h := r.s.Health(); h != want {
			t.Errorf("a second MarkReadOnly changed Health to %+v", h)
		}
		wantRefused(t, r)
	})

	t.Run("a write that rolled back cleanly reports the zero value", func(t *testing.T) {
		r := newRig(t)
		r.fs.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: 10})
		if _, err := r.s.Append(batch(10, batchID(5))); err == nil {
			t.Fatal("the faulted Append succeeded")
		}
		if h := r.s.Health(); h != (store.Health{}) {
			t.Errorf("Health = %+v, want the zero value", h)
		}
	})
}

// The store_writable probe: it makes and removes a temp file in the data
// directory and opens the log for append, writing nothing.
func TestProbe(t *testing.T) {
	t.Run("a healthy directory", func(t *testing.T) {
		r := newRig(t)
		before := dirListing(t, r.dir)
		if err := r.s.Probe(); err != nil {
			t.Fatalf("Probe: %v", err)
		}
		var ops []storefault.Op
		for _, c := range r.fs.Calls() {
			ops = append(ops, c.Op)
			if c.Op == storefault.OpOpenFile && c.Flag&os.O_APPEND == 0 {
				t.Errorf("the log was probed without O_APPEND: %+v", c)
			}
		}
		want := []storefault.Op{storefault.OpCreateTemp, storefault.OpClose, storefault.OpRemove, storefault.OpOpenFile, storefault.OpClose}
		if !slices.Equal(ops, want) {
			t.Errorf("Probe calls = %v, want %v (no Write, no Truncate)", ops, want)
		}
		if after := dirListing(t, r.dir); !slices.Equal(after, before) {
			t.Errorf("Probe left %v, want the directory as it was %v", after, before)
		}
		if got := readLog(t, r.dir); !bytes.Equal(got, r.seed) {
			t.Errorf("Probe changed the log")
		}
	})

	// Failures come from the fault FS, never from chmod, which would be
	// vacuous when the tests run as root.
	cases := map[string]storefault.Rule{
		"creating the temp file is refused": {Op: storefault.OpCreateTemp},
		"removing the temp file fails":      {Op: storefault.OpRemove},
		"opening the log for append fails":  {Op: storefault.OpOpenFile, Name: "events.jsonl"},
	}
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.fs.Inject(rule)
			if err := r.s.Probe(); !errors.Is(err, storefault.ErrInjected) {
				t.Fatalf("Probe = %v, want the injected failure", err)
			}
			if r.s.Health() != (store.Health{}) {
				t.Errorf("a failed Probe changed Health")
			}
		})
	}
}

// INV-LOG-21: with O_APPEND, the write after a rollback lands at the new end
// of the file, so no NUL hole is left at the old offset.
func TestNoNulHoleAfterRollback(t *testing.T) {
	r := newRig(t)
	r.fs.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: 37})
	if _, err := r.s.Append(batch(10, batchID(5))); err == nil {
		t.Fatal("the faulted Append succeeded")
	}
	second := batch(10, batchID(5))
	if _, err := r.s.Append(second); err != nil {
		t.Fatalf("Append after the rollback: %v", err)
	}
	got := readLog(t, r.dir)
	if bytes.IndexByte(got, 0) >= 0 {
		t.Fatalf("the log holds a NUL byte at offset %d", bytes.IndexByte(got, 0))
	}
	if want := append(bytes.Clone(r.seed), lines(t, second...)...); !bytes.Equal(got, want) {
		t.Errorf("the two writes are not contiguous: the log holds %d bytes, want %d", len(got), len(want))
	}
}

// The health accessors never wait for the disk and never race a failing
// Append (run with -race).
func TestHealthWritableAndProbeAreRaceFreeAgainstAFailingAppend(t *testing.T) {
	r := newRig(t)
	r.fs.Inject(storefault.Rule{Op: storefault.OpSync})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = r.s.Health()
				_ = r.s.Writable()
				_ = r.s.Probe()
				_ = r.s.Size()
			}
		}()
	}
	for i := range 20 {
		_, _ = r.s.Append([]event.Event{plainEvent(100 + i)})
	}
	close(stop)
	wg.Wait()

	if h := r.s.Health(); !h.ReadOnly || h.Reason != store.ReasonAppendSync {
		t.Errorf("Health = %+v, want read-only after the failed fsync", h)
	}
}
