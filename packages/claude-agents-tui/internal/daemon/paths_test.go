package daemon

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolvePaths_XDGStateHomeRespected(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/fake-state")
	// Force the macOS branch off so behaviour is testable on both OSes.
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/fake-runtime")

	p, err := ResolvePaths(PathOverrides{})
	if err != nil {
		t.Fatal(err)
	}

	wantDir := filepath.Join("/tmp/fake-runtime", "pa-monitor")
	if runtime.GOOS == "darwin" {
		wantDir = filepath.Join("/tmp/fake-state", "pa-monitor")
	}

	if p.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", p.Dir, wantDir)
	}
	if p.Socket != filepath.Join(wantDir, "daemon.sock") {
		t.Errorf("Socket = %q, want %q", p.Socket, filepath.Join(wantDir, "daemon.sock"))
	}
	if p.PIDFile != filepath.Join(wantDir, "daemon.pid") {
		t.Errorf("PIDFile = %q, want %q", p.PIDFile, filepath.Join(wantDir, "daemon.pid"))
	}
}

func TestResolvePaths_OverridesWin(t *testing.T) {
	p, err := ResolvePaths(PathOverrides{
		Socket:  "/custom/sock",
		PIDFile: "/custom/pid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Socket != "/custom/sock" {
		t.Errorf("Socket override ignored: got %q", p.Socket)
	}
	if p.PIDFile != "/custom/pid" {
		t.Errorf("PIDFile override ignored: got %q", p.PIDFile)
	}
}

func TestResolvePaths_MissingHomeIsAnError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", "")
	if _, err := ResolvePaths(PathOverrides{}); err == nil {
		t.Fatal("expected error when no HOME or XDG vars are set")
	}
}
