package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestErrorLoggerDefaultFileName(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir}
	l.LogString("hello")
	if _, err := os.Stat(filepath.Join(dir, "signal-errors.log")); err != nil {
		t.Fatalf("default file not created: %v", err)
	}
}

func TestErrorLoggerCustomFileName(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "cmux-bridge.log"}
	l.LogString("hello")
	if _, err := os.Stat(filepath.Join(dir, "cmux-bridge.log")); err != nil {
		t.Fatalf("custom file not created: %v", err)
	}
}

// TestErrorLoggerRotatesAtCap (bead pg2-yqbht): once the live file would
// exceed MaxBytes it is rotated to <name>.1, so the log stays bounded.
func TestErrorLoggerRotatesAtCap(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "x.log", MaxBytes: 100}
	for i := 0; i < 50; i++ {
		l.LogString(strings.Repeat("a", 19)) // 20 bytes with newline
	}
	live, err := os.Stat(filepath.Join(dir, "x.log"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Stat(filepath.Join(dir, "x.log.1"))
	if err != nil {
		t.Fatalf("rotated file missing: %v", err)
	}
	if live.Size() > 100 || old.Size() > 100 {
		t.Errorf("sizes live=%d old=%d, want each <= 100", live.Size(), old.Size())
	}
	if _, err := os.Stat(filepath.Join(dir, "x.log.2")); err == nil {
		t.Error("unexpected x.log.2: only one archive is kept")
	}
}

// TestErrorLoggerRotatesPreExistingOversizedFile: a file already at the cap
// when the process starts is rotated on the first write, not appended to.
func TestErrorLoggerRotatesPreExistingOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &ErrorLogger{CacheDir: dir, FileName: "x.log", MaxBytes: 100}
	l.LogString("fresh")
	data, _ := os.ReadFile(path)
	if string(data) != "fresh\n" {
		t.Errorf("live file = %q, want only the fresh line", data)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("oversized file not archived: %v", err)
	}
}

// TestErrorLoggerDefaultCapIsNotTiny: the zero-value MaxBytes must not
// rotate small logs.
func TestErrorLoggerDefaultCapIsNotTiny(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "x.log"}
	for i := 0; i < 100; i++ {
		l.LogString("line")
	}
	if _, err := os.Stat(filepath.Join(dir, "x.log.1")); err == nil {
		t.Error("rotated under the default cap")
	}
}
