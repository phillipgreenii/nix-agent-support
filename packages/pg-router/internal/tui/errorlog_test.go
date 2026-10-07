package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// TestErrorLogger_WritesToFile is the acceptance criterion, literally:
// ErrorLogger writes to <LogDir>/tui-errors.log. Isolated to a fresh
// t.TempDir() rather than any real path.
func TestErrorLogger_WritesToFile(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "tui-errors.log"}
	l.LogString("something went wrong")

	path := filepath.Join(dir, "tui-errors.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("tui-errors.log not created at %s: %v", path, err)
	}
	if !strings.Contains(string(data), "something went wrong") {
		t.Errorf("log content = %q, want it to contain the logged message", data)
	}
}

// TestErrorLogger_DefaultFileNameIsInherited pins pa-monitor's own default
// (preserved verbatim in this reproduction), even though NewModel never
// exercises it in production (it always sets FileName explicitly).
func TestErrorLogger_DefaultFileNameIsInherited(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir}
	l.LogString("hello")
	if _, err := os.Stat(filepath.Join(dir, "signal-errors.log")); err != nil {
		t.Fatalf("default file not created: %v", err)
	}
}

// TestErrorLogger_NilAndEmptyCacheDirAreNoops: LogString must be safe to
// call on a nil *ErrorLogger (Options.CacheDir was empty, so NewModel
// never constructs one) and on one with an empty CacheDir.
func TestErrorLogger_NilAndEmptyCacheDirAreNoops(t *testing.T) {
	var nilLogger *ErrorLogger
	nilLogger.LogString("should not panic")

	empty := &ErrorLogger{}
	empty.LogString("dropped")
}

// TestErrorLogPath_MatchesNewModelsConstruction: help.go's footer names
// exactly the path an ErrorLogger built the way NewModel builds it
// (FileName: "tui-errors.log") actually writes to.
func TestErrorLogPath_MatchesNewModelsConstruction(t *testing.T) {
	dir := t.TempDir()
	l := &ErrorLogger{CacheDir: dir, FileName: "tui-errors.log"}
	l.LogString("x")

	want := errorLogPath(dir)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("errorLogPath(%q) = %q does not match where the logger actually wrote: %v", dir, want, err)
	}
}

// TestNewModel_ConstructsErrorLoggerFromCacheDir: Options.CacheDir (Task
// 4.5's own field, unused before now) must actually reach the Model's
// ErrorLogger, targeting tui-errors.log specifically.
func TestNewModel_ConstructsErrorLoggerFromCacheDir(t *testing.T) {
	dir := t.TempDir()
	m := NewModel(Options{CacheDir: dir}, render.NewTheme(false))
	if m.errorLogger == nil {
		t.Fatal("NewModel with a non-empty CacheDir left errorLogger nil")
	}
	m.errorLogger.LogString("wired")
	if _, err := os.Stat(filepath.Join(dir, "tui-errors.log")); err != nil {
		t.Fatalf("NewModel's ErrorLogger did not write to <CacheDir>/tui-errors.log: %v", err)
	}
}

// TestNewModel_EmptyCacheDirLeavesErrorLoggerNil: no directory to log to
// means no logger at all -- LogString's nil-receiver safety is what
// makes every OTHER caller unconditional.
func TestNewModel_EmptyCacheDirLeavesErrorLoggerNil(t *testing.T) {
	m := NewModel(Options{}, render.NewTheme(false))
	if m.errorLogger != nil {
		t.Fatal("NewModel with no CacheDir constructed an ErrorLogger anyway")
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
