package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeFakeGH installs a fake `gh` binary at the front of PATH for this test.
func writeFakeGH(t *testing.T, stdout string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	var script string
	if exitCode == 0 {
		// Use printf to avoid issues with special chars in stdout
		script = fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nexit 0\n", stdout)
	} else {
		script = fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestLookupPRFound(t *testing.T) {
	writeFakeGH(t,
		`{"number":42,"title":"Add the thing","url":"https://github.com/owner/repo/pull/42"}`,
		0,
	)
	info, found, err := LookupPR(context.Background(), t.TempDir(), "feat/xyz")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if info.Number != 42 {
		t.Errorf("Number = %d, want 42", info.Number)
	}
	if info.Title != "Add the thing" {
		t.Errorf("Title = %q, want \"Add the thing\"", info.Title)
	}
	if info.URL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("URL = %q unexpected", info.URL)
	}
}

func TestLookupPRNotFound(t *testing.T) {
	writeFakeGH(t, "", 1)
	_, found, err := LookupPR(context.Background(), t.TempDir(), "feat/xyz")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected found=false when gh exits non-zero")
	}
}

func TestLookupPRExecError(t *testing.T) {
	t.Setenv("PATH", "") // no gh binary available
	_, found, err := LookupPR(context.Background(), t.TempDir(), "feat/xyz")
	if err == nil {
		t.Error("expected non-nil error when gh is not found on PATH")
	}
	if found {
		t.Error("expected found=false on exec error")
	}
}
