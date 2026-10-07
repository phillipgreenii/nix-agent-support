package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logAnchorLine mirrors the sync stage's anchor-write record (rules.go
// logAnchorWrite) so these tests pin the on-disk text format the bead's
// acceptance greps for: "anchor write" and a cause= field.
func logAnchorLine(l *slog.Logger, bead, cause string) {
	l.Info("pg-desk sync: anchor write", "bead", bead, "cause", cause)
}

func TestAnchorWriteLogger_PersistsAndTeesToStderr(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "pg-desk", anchorWriteLogName)
	var stderr bytes.Buffer
	l, closeFn := newAnchorWriteLogger(&stderr, path, anchorWriteLogMaxBytes)

	logAnchorLine(l, "zr-fvuor", "conflict-flip")
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The file survives the run (the process-level close above).
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("anchor-write log not persisted: %v", err)
	}
	for _, want := range []string{"anchor write", "bead=zr-fvuor", "cause=conflict-flip"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("file missing %q: %s", want, b)
		}
	}
	if !strings.Contains(stderr.String(), "cause=conflict-flip") {
		t.Errorf("stderr no longer receives the line: %q", stderr.String())
	}
}

func TestAnchorWriteLogger_AppendsAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), anchorWriteLogName)
	for _, bead := range []string{"zr-1", "zr-2"} {
		l, closeFn := newAnchorWriteLogger(&bytes.Buffer{}, path, anchorWriteLogMaxBytes)
		logAnchorLine(l, bead, "created")
		_ = closeFn()
	}
	b, _ := os.ReadFile(path)
	if got := strings.Count(string(b), "anchor write"); got != 2 {
		t.Fatalf("want 2 appended lines across two runs, got %d:\n%s", got, b)
	}
}

func TestAnchorWriteLogger_NoWriteCreatesNoFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never")
	path := filepath.Join(dir, anchorWriteLogName)
	_, closeFn := newAnchorWriteLogger(&bytes.Buffer{}, path, anchorWriteLogMaxBytes)
	_ = closeFn()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a run that logged nothing created %s (err=%v)", dir, err)
	}
}

func TestAnchorWriteLogger_RotatesWhenOversized(t *testing.T) {
	path := filepath.Join(t.TempDir(), anchorWriteLogName)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	l, closeFn := newAnchorWriteLogger(&bytes.Buffer{}, path, 100) // at the bound
	logAnchorLine(l, "zr-new", "created")
	_ = closeFn()

	old, err := os.ReadFile(path + ".1")
	if err != nil || string(old) != strings.Repeat("x", 100) {
		t.Fatalf("oversized file not rotated to .1: err=%v content=%q", err, old)
	}
	cur, _ := os.ReadFile(path)
	if !strings.Contains(string(cur), "zr-new") || strings.Contains(string(cur), "xxxx") {
		t.Fatalf("live file should hold only the new line: %q", cur)
	}
}

func TestAnchorWriteLogger_UnwritablePathNeverFails(t *testing.T) {
	// The parent is a regular file, so MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	l, closeFn := newAnchorWriteLogger(&stderr, filepath.Join(blocker, "sub", anchorWriteLogName), anchorWriteLogMaxBytes)
	logAnchorLine(l, "zr-1", "created")
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(stderr.String(), "cause=created") {
		t.Fatalf("stderr line lost when the file is unwritable: %q", stderr.String())
	}
}

func TestAnchorWriteLogPath_FollowsXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got, want := anchorWriteLogPath(), "/xdg/state/pg-desk/anchor-write.log"; got != want {
		t.Fatalf("anchorWriteLogPath() = %q, want %q", got, want)
	}
}
