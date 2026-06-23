package tui

import (
	"os"
	"path/filepath"
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
