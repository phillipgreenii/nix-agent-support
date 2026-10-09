package storefault_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

// TestARuleFailsOnlyItsOwnOperation pins that a rule matches its operation
// exactly: calls of every other operation on the same file, whether their
// names sort before or after it, pass through and are not counted.
func TestARuleFailsOnlyItsOwnOperation(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fsys := storefault.New(nil)
	f := open(t, fsys, name, os.O_RDWR|os.O_CREATE)
	fsys.Inject(storefault.Rule{Op: storefault.OpStat})
	if _, err := f.Write([]byte("abc")); err != nil { // "Write" sorts after "Stat"
		t.Fatalf("Write: %v", err)
	}
	if err := f.Sync(); err != nil { // "Sync" sorts after "Stat"
		t.Fatalf("Sync: %v", err)
	}
	if _, err := f.ReadAt(make([]byte, 1), 0); err != nil { // "ReadAt" sorts before "Stat"
		t.Fatalf("ReadAt: %v", err)
	}
	if err := fsys.MkdirAll(filepath.Dir(name), 0o700); err != nil { // "MkdirAll" sorts before "Stat"
		t.Fatalf("MkdirAll: %v", err)
	}
	if fsys.Pending() != 1 {
		t.Fatalf("the Stat rule fired on another operation")
	}
	if _, err := f.Stat(); !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("Stat = %v, want the injected failure", err)
	}
}

// TestASpentRuleNoLongerFires pins that a rule fires once: the matching call
// after it passes through.
func TestASpentRuleNoLongerFires(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fsys := storefault.New(nil)
	f := open(t, fsys, name, os.O_RDWR|os.O_CREATE)
	fsys.Inject(storefault.Rule{Op: storefault.OpSync, Name: "log"})
	if err := f.Sync(); !errors.Is(err, storefault.ErrInjected) {
		t.Fatalf("first Sync = %v, want the injected failure", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("second Sync = %v, want it to pass once the rule is spent", err)
	}
}

// TestTheFirstRuleInjectedWinsWhenTwoFire pins that when two rules fire on one
// call, the call fails with the error of the rule injected first, and both
// are spent.
func TestTheFirstRuleInjectedWinsWhenTwoFire(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	fsys := storefault.New(nil)
	f := open(t, fsys, name, os.O_RDWR|os.O_CREATE)
	first, second := errors.New("first"), errors.New("second")
	fsys.Inject(storefault.Rule{Op: storefault.OpSync, Err: first})
	fsys.Inject(storefault.Rule{Op: storefault.OpSync, Err: second})
	if err := f.Sync(); !errors.Is(err, first) {
		t.Fatalf("Sync = %v, want the first rule's error", err)
	}
	if fsys.Pending() != 0 {
		t.Errorf("%d rules still pending, want both spent", fsys.Pending())
	}
}

// TestErrorsOfTheInnerFSPassThrough pins that a call the fault FS lets through
// reports the inner file system's own failure, and that the partial write
// of a failed Write reports the inner write's failure when it has one.
func TestErrorsOfTheInnerFSPassThrough(t *testing.T) {
	dir := t.TempDir()
	fsys := storefault.New(nil)
	if f, err := fsys.OpenFile(filepath.Join(dir, "absent"), os.O_RDONLY, 0); !errors.Is(err, fs.ErrNotExist) || f != nil {
		t.Errorf("OpenFile of a missing file = %v, %v; want no file and fs.ErrNotExist", f, err)
	}
	if f, name, err := fsys.CreateTemp(filepath.Join(dir, "absent"), "x-*"); !errors.Is(err, fs.ErrNotExist) || f != nil || name != "" {
		t.Errorf("CreateTemp in a missing directory = %v, %q, %v; want no file and fs.ErrNotExist", f, name, err)
	}

	inner := storefault.New(nil)
	innerErr := errors.New("the disk is full")
	inner.Inject(storefault.Rule{Op: storefault.OpWrite, Err: innerErr})
	outer := storefault.New(inner)
	outer.Inject(storefault.Rule{Op: storefault.OpWrite, Partial: 2})
	f := open(t, outer, filepath.Join(dir, "log"), os.O_RDWR|os.O_CREATE)
	if _, err := f.Write([]byte("abcdef")); !errors.Is(err, innerErr) {
		t.Errorf("Write = %v, want the inner write's failure", err)
	}
}
