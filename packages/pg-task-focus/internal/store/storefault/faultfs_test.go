package storefault_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

// Compile-time proof that the fault FS is a store.FS.
var _ store.FS = (*storefault.FS)(nil)

func open(t *testing.T, fs *storefault.FS, name string, flag int) store.File {
	t.Helper()
	f, err := fs.OpenFile(name, flag, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", name, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func content(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRecordsTheOrderedCallLog(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "log")
	fs := storefault.New(nil)

	f := open(t, fs, name, os.O_RDWR|os.O_CREATE|os.O_APPEND)
	if _, err := f.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(name); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := fs.MkdirAll(filepath.Join(dir, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	tmp, tmpName, err := fs.CreateTemp(dir, "t-*")
	if err != nil {
		t.Fatal(err)
	}
	_ = tmp.Close()

	want := []storefault.Call{
		{Op: storefault.OpOpenFile, Name: name, Flag: os.O_RDWR | os.O_CREATE | os.O_APPEND},
		{Op: storefault.OpWrite, Name: name, Size: 3},
		{Op: storefault.OpSync, Name: name},
		{Op: storefault.OpTruncate, Name: name, Size: 1},
		{Op: storefault.OpStat, Name: name},
		{Op: storefault.OpReadAt, Name: name, Size: 1},
		{Op: storefault.OpClose, Name: name},
		{Op: storefault.OpStat, Name: name},
		{Op: storefault.OpRemove, Name: name},
		{Op: storefault.OpMkdirAll, Name: filepath.Join(dir, "a", "b")},
		{Op: storefault.OpCreateTemp, Name: filepath.Join(dir, "t-*")},
		{Op: storefault.OpClose, Name: tmpName},
	}
	got := fs.Calls()
	if len(got) != len(want) {
		t.Fatalf("recorded %d calls, want %d:\n%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	fs.ResetCalls()
	if n := len(fs.Calls()); n != 0 {
		t.Errorf("after ResetCalls the log holds %d calls", n)
	}
}

func TestCallsReturnsACopy(t *testing.T) {
	fs := storefault.New(nil)
	if err := fs.MkdirAll(filepath.Join(t.TempDir(), "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	calls := fs.Calls()
	calls[0].Name = "mutated"
	if fs.Calls()[0].Name == "mutated" {
		t.Error("Calls exposes the internal log")
	}
}

func TestFailsOnlyTheNthCall(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fs := storefault.New(nil)
	f := open(t, fs, name, os.O_RDWR|os.O_CREATE)
	fs.Inject(storefault.Rule{Op: storefault.OpSync, Nth: 2})
	if fs.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", fs.Pending())
	}

	if err := f.Sync(); err != nil {
		t.Fatalf("the 1st Sync failed: %v", err)
	}
	if err := f.Sync(); !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("the 2nd Sync = %v, want ErrInjected", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("the 3rd Sync failed: %v", err)
	}
	if fs.Pending() != 0 {
		t.Errorf("Pending = %d after the rule fired, want 0", fs.Pending())
	}
	// The failed call is recorded like any other.
	syncs := 0
	for _, c := range fs.Calls() {
		if c.Op == storefault.OpSync {
			syncs++
		}
	}
	if syncs != 3 {
		t.Errorf("the log holds %d Sync calls, want 3", syncs)
	}
}

func TestZeroNthMeansTheFirstCall(t *testing.T) {
	fs := storefault.New(nil)
	fs.Inject(storefault.Rule{Op: storefault.OpMkdirAll})
	if err := fs.MkdirAll(filepath.Join(t.TempDir(), "x"), 0o700); !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("MkdirAll = %v, want ErrInjected", err)
	}
}

func TestRuleCountsOnlyCallsAfterInjection(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fs := storefault.New(nil)
	f := open(t, fs, name, os.O_RDWR|os.O_CREATE)
	for range 3 {
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	fs.Inject(storefault.Rule{Op: storefault.OpSync, Nth: 1})
	if err := f.Sync(); !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("the first Sync after Inject = %v, want ErrInjected", err)
	}
}

func TestRuleReturnsItsOwnErrorVerbatim(t *testing.T) {
	boom := errors.New("disk on fire")
	fs := storefault.New(nil)
	fs.Inject(storefault.Rule{Op: storefault.OpRemove, Err: boom})
	if err := fs.Remove(filepath.Join(t.TempDir(), "missing")); err != boom {
		t.Fatalf("Remove = %v, want the rule's own error", err)
	}
}

func TestRuleNameSelectsByFileNameSubstring(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "events.jsonl")
	b := filepath.Join(dir, "events.jsonl.recovered-1")
	fs := storefault.New(nil)
	fa := open(t, fs, a, os.O_WRONLY|os.O_CREATE)
	fb := open(t, fs, b, os.O_WRONLY|os.O_CREATE)

	fs.Inject(storefault.Rule{Op: storefault.OpWrite, Name: "recovered", Nth: 1})
	if _, err := fa.Write([]byte("x")); err != nil {
		t.Fatalf("a write to a file the rule does not name failed: %v", err)
	}
	if _, err := fb.Write([]byte("y")); !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("the write to the named file = %v, want ErrInjected", err)
	}
}

func TestEveryOperationCanBeFailed(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "log")
	seed := filepath.Join(dir, "seed")
	if err := os.WriteFile(seed, []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := map[storefault.Op]func(fs *storefault.FS, f store.File) error{
		storefault.OpOpenFile: func(fs *storefault.FS, _ store.File) error {
			f, err := fs.OpenFile(seed, os.O_RDONLY, 0)
			if err == nil {
				_ = f.Close()
			}
			return err
		},
		storefault.OpStat:   func(fs *storefault.FS, _ store.File) error { _, err := fs.Stat(seed); return err },
		storefault.OpRemove: func(fs *storefault.FS, _ store.File) error { return fs.Remove(filepath.Join(dir, "none")) },
		storefault.OpMkdirAll: func(fs *storefault.FS, _ store.File) error {
			return fs.MkdirAll(filepath.Join(dir, "d"), 0o700)
		},
		storefault.OpCreateTemp: func(fs *storefault.FS, _ store.File) error {
			f, _, err := fs.CreateTemp(dir, "t-*")
			if err == nil {
				_ = f.Close()
			}
			return err
		},
		storefault.OpReadAt:   func(_ *storefault.FS, f store.File) error { _, err := f.ReadAt(make([]byte, 1), 0); return err },
		storefault.OpWrite:    func(_ *storefault.FS, f store.File) error { _, err := f.Write([]byte("w")); return err },
		storefault.OpSync:     func(_ *storefault.FS, f store.File) error { return f.Sync() },
		storefault.OpTruncate: func(_ *storefault.FS, f store.File) error { return f.Truncate(0) },
		storefault.OpClose:    func(_ *storefault.FS, f store.File) error { return f.Close() },
	}
	// File.Stat shares OpStat with FS.Stat.
	fileStat := func(_ *storefault.FS, f store.File) error { _, err := f.Stat(); return err }

	for op, call := range ops {
		t.Run(string(op), func(t *testing.T) {
			fs := storefault.New(nil)
			f := open(t, fs, name, os.O_RDWR|os.O_CREATE)
			fs.Inject(storefault.Rule{Op: op})
			if err := call(fs, f); !errors.Is(err, storefault.ErrInjected) {
				t.Fatalf("%s = %v, want ErrInjected", op, err)
			}
			if fs.Pending() != 0 {
				t.Errorf("the rule did not fire")
			}
		})
	}
	t.Run("File.Stat", func(t *testing.T) {
		fs := storefault.New(nil)
		f := open(t, fs, name, os.O_RDWR|os.O_CREATE)
		fs.Inject(storefault.Rule{Op: storefault.OpStat})
		if err := fileStat(fs, f); !errors.Is(err, storefault.ErrInjected) {
			t.Fatalf("File.Stat = %v, want ErrInjected", err)
		}
	})
}

func TestAFailedCloseStillClosesTheFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fs := storefault.New(nil)
	f := open(t, fs, name, os.O_RDWR|os.O_CREATE)
	fs.Inject(storefault.Rule{Op: storefault.OpClose})
	if err := f.Close(); !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("Close = %v, want ErrInjected", err)
	}
	if _, err := f.Write([]byte("x")); err == nil {
		t.Error("the file is still writable after a failed Close: the descriptor leaked")
	}
}

func TestPartialWriteWritesThePrefixThenFails(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fs := storefault.New(nil)
	f := open(t, fs, name, os.O_RDWR|os.O_CREATE|os.O_APPEND)
	fs.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: 4})

	n, err := f.Write([]byte("0123456789"))
	if !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("Write error = %v, want ErrInjected", err)
	}
	if n != 4 {
		t.Errorf("Write reported %d bytes, want 4", n)
	}
	if got := content(t, name); got != "0123" {
		t.Errorf("the file holds %q, want the 4-byte prefix %q", got, "0123")
	}
}

func TestPartialLongerThanTheWriteIsClamped(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fs := storefault.New(nil)
	f := open(t, fs, name, os.O_RDWR|os.O_CREATE)
	fs.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: 100})
	n, err := f.Write([]byte("abc"))
	if !errors.Is(err, storefault.ErrInjected) || n != 3 {
		t.Fatalf("Write = (%d, %v), want (3, ErrInjected)", n, err)
	}
}

func TestAFailedWriteWithoutPartialWritesNothing(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fs := storefault.New(nil)
	f := open(t, fs, name, os.O_RDWR|os.O_CREATE)
	fs.Inject(storefault.Rule{Op: storefault.OpWrite})
	n, err := f.Write([]byte("abc"))
	if !errors.Is(err, storefault.ErrInjected) || n != 0 {
		t.Fatalf("Write = (%d, %v), want (0, ErrInjected)", n, err)
	}
	if got := content(t, name); got != "" {
		t.Errorf("the file holds %q, want it empty", got)
	}
}

// A file opened with O_APPEND writes at the current end of the file even
// after a truncate; one opened without it writes at its own offset and leaves
// a NUL hole. The store's rollback relies on the first behaviour.
func TestHonoursOAppendAfterATruncate(t *testing.T) {
	dir := t.TempDir()
	appendName := filepath.Join(dir, "append")
	plainName := filepath.Join(dir, "plain")
	fs := storefault.New(nil)

	for _, tc := range []struct {
		name string
		flag int
		want string
	}{
		{appendName, os.O_RDWR | os.O_CREATE | os.O_APPEND, "d"},
		{plainName, os.O_RDWR | os.O_CREATE, "\x00\x00\x00d"},
	} {
		f := open(t, fs, tc.name, tc.flag)
		if _, err := f.Write([]byte("abc")); err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("d")); err != nil {
			t.Fatal(err)
		}
		if got := content(t, tc.name); got != tc.want {
			t.Errorf("%s: the file holds %q, want %q", filepath.Base(tc.name), got, tc.want)
		}
	}
}

func TestSeveralRulesOnOneOperationEachFireOnTheirOwnCount(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fs := storefault.New(nil)
	f := open(t, fs, name, os.O_RDWR|os.O_CREATE)
	fs.Inject(storefault.Rule{Op: storefault.OpSync, Nth: 1})
	fs.Inject(storefault.Rule{Op: storefault.OpSync, Nth: 3})
	var failed []int
	for i := 1; i <= 4; i++ {
		if err := f.Sync(); err != nil {
			failed = append(failed, i)
		}
	}
	if len(failed) != 2 || failed[0] != 1 || failed[1] != 3 {
		t.Errorf("Sync calls %v failed, want 1 and 3", failed)
	}
}

func TestWrapsAnotherFS(t *testing.T) {
	inner := storefault.New(nil)
	outer := storefault.New(inner)
	name := filepath.Join(t.TempDir(), "log")
	f := open(t, outer, name, os.O_RDWR|os.O_CREATE)
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(inner.Calls()) == 0 || len(outer.Calls()) == 0 {
		t.Errorf("the call went to inner=%d outer=%d recorded calls, want both", len(inner.Calls()), len(outer.Calls()))
	}
}

// The fault FS is shared by goroutines of the store tests and the engine
// tests; run with -race.
func TestSafeForConcurrentUse(t *testing.T) {
	dir := t.TempDir()
	fs := storefault.New(nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := filepath.Join(dir, "f"+string(rune('a'+i)))
			f, err := fs.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
			if err != nil {
				return
			}
			defer func() { _ = f.Close() }()
			for range 20 {
				fs.Inject(storefault.Rule{Op: storefault.OpSync, Nth: 2})
				_, _ = f.Write([]byte("x"))
				_ = f.Sync()
				_ = fs.Pending()
				_ = fs.Calls()
			}
		}()
	}
	wg.Wait()
	fs.ResetCalls()
}
