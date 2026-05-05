package inputproc

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMockProcessor(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigured_Unset(t *testing.T) {
	t.Setenv(envKey, "")
	if Configured() {
		t.Error("Configured() = true, want false when env var is empty")
	}
}

func TestConfigured_Set(t *testing.T) {
	t.Setenv(envKey, "/usr/bin/true")
	if !Configured() {
		t.Error("Configured() = false, want true when env var is set")
	}
}

func TestProcess_Exit0_Rewrites(t *testing.T) {
	script := writeMockProcessor(t, "rewriter", `echo "wrapped $1"`)
	t.Setenv(envKey, script)

	rewritten, changed := Process("git status")
	if !changed {
		t.Fatal("Process() changed = false, want true")
	}
	if rewritten != "wrapped git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "wrapped git status")
	}
}

func TestProcess_Exit1_NoRewrite(t *testing.T) {
	script := writeMockProcessor(t, "noop", "exit 1")
	t.Setenv(envKey, script)

	rewritten, changed := Process("git status")
	if changed {
		t.Error("Process() changed = true, want false for exit 1")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_Exit2_NoRewrite(t *testing.T) {
	script := writeMockProcessor(t, "error", "exit 2")
	t.Setenv(envKey, script)

	rewritten, changed := Process("git status")
	if changed {
		t.Error("Process() changed = true, want false for exit 2")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_EmptyStdout_NoRewrite(t *testing.T) {
	script := writeMockProcessor(t, "empty", `echo ""`)
	t.Setenv(envKey, script)

	rewritten, changed := Process("git status")
	if changed {
		t.Error("Process() changed = true, want false for empty stdout")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_CommandNotFound_NoRewrite(t *testing.T) {
	t.Setenv(envKey, "/nonexistent/binary")

	rewritten, changed := Process("git status")
	if changed {
		t.Error("Process() changed = true, want false for missing command")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_MultiWordCommand(t *testing.T) {
	script := writeMockProcessor(t, "multi", `
if [ "$1" = "rewrite" ]; then
    echo "wrapped $2"
else
    exit 1
fi`)
	t.Setenv(envKey, script+" rewrite")

	rewritten, changed := Process("git status")
	if !changed {
		t.Fatal("Process() changed = false, want true for multi-word command")
	}
	if rewritten != "wrapped git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "wrapped git status")
	}
}

func TestProcess_NotConfigured_NoRewrite(t *testing.T) {
	t.Setenv(envKey, "")

	rewritten, changed := Process("git status")
	if changed {
		t.Error("Process() changed = true, want false when not configured")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}
