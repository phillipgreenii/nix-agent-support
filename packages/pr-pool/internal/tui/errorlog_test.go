package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/tui/render"
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
