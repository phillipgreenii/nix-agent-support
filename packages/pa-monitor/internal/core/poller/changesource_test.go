package poller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTwoTierChangeSource_FastDetectsSizeChange: the fast tier returns as soon as
// a watched file's size changes (even with an unchanged mtime), well before the
// slow period elapses.
func TestTwoTierChangeSource_FastDetectsSizeChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	fixed := time.Unix(1_776_000_000, 0)
	if err := os.WriteFile(path, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, fixed, fixed)

	cs := newTwoTierChangeSource(2*time.Millisecond, time.Hour, time.Now)
	cs.SetWatch([]string{path})

	// Grow the file but keep the SAME mtime — the fast tier must still notice.
	go func() {
		time.Sleep(10 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		_, _ = f.WriteString("bb\n")
		_ = f.Close()
		_ = os.Chtimes(path, fixed, fixed)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !cs.WaitForChange(ctx) {
		t.Fatal("fast tier did not detect the same-mtime size change before ctx timeout")
	}
}

// TestTwoTierChangeSource_SlowFiresWithoutChange: with no watched change, the
// slow tier still fires periodically so newly-in-window files are caught.
func TestTwoTierChangeSource_SlowFiresWithoutChange(t *testing.T) {
	cs := newTwoTierChangeSource(2*time.Millisecond, 20*time.Millisecond, time.Now)
	cs.SetWatch(nil) // nothing to watch → only the slow tier can fire

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if !cs.WaitForChange(ctx) {
		t.Fatal("slow tier never fired")
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Errorf("slow tier fired too early (%v); expected ~the slow period", elapsed)
	}
}

// TestTwoTierChangeSource_CtxDoneReturnsFalse: a cancelled ctx unblocks WaitForChange.
func TestTwoTierChangeSource_CtxDoneReturnsFalse(t *testing.T) {
	cs := newTwoTierChangeSource(2*time.Millisecond, time.Hour, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if cs.WaitForChange(ctx) {
		t.Error("WaitForChange must return false when ctx is already done")
	}
}
