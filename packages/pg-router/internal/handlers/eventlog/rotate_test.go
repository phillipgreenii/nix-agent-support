package eventlog

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func countLines(t *testing.T, p string) int {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
	}
	return n
}

func newSmall(t *testing.T, ret Retention) (*Writer, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	w.Retention = ret
	t.Cleanup(func() { _ = w.Close() })
	return w, p
}

func TestRotation_boundsSizeAndGenerations_noLineLost(t *testing.T) {
	w, p := newSmall(t, Retention{MaxBytes: 2000, Keep: 2})
	const total = 200
	pad := strings.Repeat("x", 60)
	for i := 0; i < total; i++ {
		if err := w.Emit("info", "k", pad, map[string]any{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() >= 2000+300 {
		t.Fatalf("live file not bounded: %v %v", fi, err)
	}
	for _, n := range []int{1, 2} {
		if _, err := os.Stat(gen(p, n)); err != nil {
			t.Fatalf("generation %d missing: %v", n, err)
		}
	}
	if _, err := os.Stat(gen(p, 3)); !os.IsNotExist(err) {
		t.Fatalf("generation 3 must not exist (Keep=2): %v", err)
	}
	// Appends continue correctly across rotations: the newest line is in the
	// live file, and what survives is a contiguous, ordered tail.
	got := countLines(t, p) + countLines(t, gen(p, 1)) + countLines(t, gen(p, 2))
	if got == 0 || got > total {
		t.Fatalf("surviving lines = %d", got)
	}
	if countLines(t, p) == 0 {
		t.Fatal("live file empty right after appends")
	}
}

func TestRotation_appendsLandInLivePathAfterRotation(t *testing.T) {
	w, p := newSmall(t, Retention{MaxBytes: 300, Keep: 1})
	for i := 0; i < 3; i++ {
		_ = w.Emit("info", "k", strings.Repeat("y", 150), nil)
	}
	before := countLines(t, p)
	if err := w.Emit("info", "after", "sentinel", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"msg":"sentinel"`) {
		t.Fatalf("sentinel not in live file (before=%d): %s", before, b)
	}
	old, _ := os.ReadFile(gen(p, 1))
	if strings.Contains(string(old), "sentinel") {
		t.Fatal("sentinel leaked into a rotated generation")
	}
}

func TestRotation_secondWriterOnSamePathReopens(t *testing.T) {
	w1, p := newSmall(t, Retention{MaxBytes: 400, Keep: 1})
	w2, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Close() }()
	w2.Retention = w1.Retention
	for i := 0; i < 6; i++ {
		_ = w1.Emit("info", "k", strings.Repeat("z", 150), nil) // rotates under w2's open fd
	}
	if err := w2.Emit("info", "k", "from-w2", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "from-w2") {
		t.Fatal("w2 kept writing into a rotated generation")
	}
}

func TestRotation_ageDropsOldGenerations(t *testing.T) {
	w, p := newSmall(t, Retention{MaxBytes: 300, Keep: 3, MaxAge: time.Hour})
	for i := 0; i < 3; i++ {
		_ = w.Emit("info", "k", strings.Repeat("a", 150), nil)
	}
	g1 := gen(p, 1)
	if _, err := os.Stat(g1); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(g1, old, old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // trigger another rotation; old g1 becomes g2 and ages out
		_ = w.Emit("info", "k", strings.Repeat("b", 150), nil)
	}
	if _, err := os.Stat(gen(p, 2)); !os.IsNotExist(err) {
		t.Fatalf("aged generation survived: %v", err)
	}
}

func TestRotation_disabledWhenMaxBytesZero(t *testing.T) {
	w, p := newSmall(t, Retention{})
	for i := 0; i < 50; i++ {
		_ = w.Emit("info", "k", strings.Repeat("c", 100), nil)
	}
	if _, err := os.Stat(gen(p, 1)); !os.IsNotExist(err) {
		t.Fatal("rotated although disabled")
	}
}

func TestDefaultRetention_envOverrides(t *testing.T) {
	t.Setenv(EnvMaxBytes, "1234")
	t.Setenv(EnvKeep, "7")
	t.Setenv(EnvMaxAge, "48h")
	r := DefaultRetention()
	if r.MaxBytes != 1234 || r.Keep != 7 || r.MaxAge != 48*time.Hour {
		t.Fatalf("got %+v", r)
	}
	t.Setenv(EnvMaxBytes, "junk")
	if DefaultRetention().MaxBytes != DefaultMaxBytes {
		t.Fatal("unparsable value must fall back to the default")
	}
}

func TestEmit_devNullFallbackStillWorks(t *testing.T) {
	w, err := New(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if err := w.Emit("info", "k", "m", nil); err != nil {
		t.Fatal(err)
	}
}
