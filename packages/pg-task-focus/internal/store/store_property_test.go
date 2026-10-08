package store_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

// fault is what a generated Append runs into.
type fault int

const (
	faultNone              fault = iota
	faultWriteRolledBack         // a partial write, a good rollback: stays writable
	faultWriteTruncate           // a partial write, the rollback truncate fails
	faultWriteRollbackSync       // a partial write, the rollback sync fails
	faultAppendSync              // the append fsync fails
)

func (f fault) readOnly() bool { return f >= faultWriteTruncate }

// INV-LOG-21, INV-LOG-29, INV-LOG-9 and INV-LOG-10 together: whatever faults
// the appends run into and whenever the process restarts, the committed log a
// reopen reads is exactly the batches whose Append returned nil. No
// acknowledged batch is lost, no failed batch comes back, and the file holds
// no NUL byte and no torn line.
func TestPropertyReopenHoldsExactlyTheAcknowledgedBatches(t *testing.T) {
	parent := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp(parent, "run-")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()

		var (
			acked   []event.ID // every event of every batch whose Append returned nil
			n       int        // event numbering
			batches int
			s       *store.Store
			fs      *storefault.FS
			ro      bool
		)
		open := func() {
			fs = storefault.New(nil)
			var evs []event.Event
			var rec store.Recovery
			var err error
			s, evs, rec, err = store.Open(store.Options{Dir: dir, FS: fs, Now: fixedNow})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			ro = false
			if got := ids(evs); !slices.Equal(got, acked) && (len(got) != 0 || len(acked) != 0) {
				t.Fatalf("reopened with events %v, want exactly the acknowledged %v (recovery %+v)", got, acked, rec)
			}
			checkFile(t, dir)
		}
		open()
		defer func() { _ = s.Close() }()

		for range rapid.IntRange(1, 25).Draw(t, "steps") {
			if rapid.IntRange(0, 5).Draw(t, "step") == 0 {
				// A restart, which also clears read-only mode.
				if err := s.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				open()
				continue
			}

			var evs []event.Event
			if rapid.Bool().Draw(t, "is a batch") {
				batches++
				evs = batchOf(n+1, rapid.IntRange(1, 3).Draw(t, "members"), batchID(batches))
			} else {
				evs = []event.Event{plainEvent(n + 1)}
			}
			n += len(evs)

			f := faultNone
			if !ro {
				f = fault(rapid.IntRange(0, 4).Draw(t, "fault"))
			}
			switch f {
			case faultWriteRolledBack, faultWriteTruncate, faultWriteRollbackSync:
				total := len(lines(t, evs...))
				k := rapid.IntRange(0, total-1).Draw(t, "bytes written before the failure")
				fs.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: k})
				switch f {
				case faultWriteTruncate:
					fs.Inject(storefault.Rule{Op: storefault.OpTruncate})
				case faultWriteRollbackSync:
					fs.Inject(storefault.Rule{Op: storefault.OpSync})
				}
			case faultAppendSync:
				fs.Inject(storefault.Rule{Op: storefault.OpSync})
			}

			_, err := s.Append(evs)
			switch {
			case ro:
				if !errors.Is(err, store.ErrStoreUnavailable) {
					t.Fatalf("Append on a read-only store = %v, want ErrStoreUnavailable", err)
				}
			case f == faultNone:
				if err != nil {
					t.Fatalf("Append without a fault: %v", err)
				}
				acked = append(acked, ids(evs)...)
			default:
				var ae *store.AppendError
				if !errors.As(err, &ae) {
					t.Fatalf("faulted Append = %v, want an *AppendError", err)
				}
				wantStage := store.StageWrite
				if f == faultAppendSync {
					wantStage = store.StageSync
				}
				if ae.Stage != wantStage {
					t.Fatalf("stage = %q, want %q", ae.Stage, wantStage)
				}
				if fs.Pending() != 0 {
					t.Fatalf("fault %d was never reached", f)
				}
				ro = f.readOnly()
			}
			if s.Health().ReadOnly != ro || s.Writable() == ro {
				t.Fatalf("Health = %+v, Writable = %v, want read-only = %v", s.Health(), s.Writable(), ro)
			}
		}

		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		open()
	})
}

// checkFile asserts the log on disk, after a reopen, is clean.
func checkFile(t fataler, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		t.Fatalf("the log holds a NUL byte at offset %d", i)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		t.Fatalf("the log ends in a torn line")
	}
	rep, err := store.Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Problem != nil || rep.Recovery != (store.Recovery{}) {
		t.Fatalf("a reopened log needs recovery or is corrupt: %+v", rep)
	}
}
