package poller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
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

// TestTwoTierChangeSource_RegistryOnlyChangeFiresFastTier: a registry-only
// change (busy -> waiting written to ~/.claude/sessions/<pid>.json with no
// transcript write) is detected by the FAST tier through the Monitor's real
// WatchPaths, long before the (here hour-long) slow tier.
func TestTwoTierChangeSource_RegistryOnlyChangeFiresFastTier(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := filepath.Join(sessionsDir, "111.json")
	busy := `{"pid":111,"sessionId":"s1","cwd":"/tmp/p","kind":"interactive","status":"busy"}`
	if err := os.WriteFile(reg, []byte(busy), 0o600); err != nil {
		t.Fatal(err)
	}

	disc := &session.Discoverer{
		SessionsDir: sessionsDir,
		PidAlive:    func(int) bool { return true },
		ReadEnv:     func(string, int) (map[string]string, error) { return nil, nil },
	}
	m := corpus.New(home, disc)
	if _, err := m.Scan(time.Now()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	cs := newTwoTierChangeSource(2*time.Millisecond, time.Hour, time.Now)
	cs.SetWatch(m.WatchPaths())

	go func() {
		time.Sleep(10 * time.Millisecond)
		// Same-length status rewrite is not enough on its own to rely on size, so
		// the longer "waiting" payload changes size; mtime moves too.
		waiting := `{"pid":111,"sessionId":"s1","cwd":"/tmp/p","kind":"interactive","status":"waiting","waitingFor":"permission"}`
		_ = os.WriteFile(reg, []byte(waiting), 0o600)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !cs.WaitForChange(ctx) {
		t.Fatal("fast tier did not detect a registry-only change before ctx timeout")
	}
}
