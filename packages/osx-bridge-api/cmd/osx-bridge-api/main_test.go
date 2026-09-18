package main

import (
	"path/filepath"
	"testing"
)

func TestDefaultSocketPathPrefersXDGStateHome(t *testing.T) {
	getenv := func(k string) string {
		switch k {
		case "XDG_STATE_HOME":
			return "/tmp/xdg-state"
		case "HOME":
			return "/tmp/home"
		}
		return ""
	}
	got, err := defaultSocketPath(getenv)
	if err != nil {
		t.Fatalf("defaultSocketPath: %v", err)
	}
	want := filepath.Join("/tmp/xdg-state", "osx-bridge-api", "osx-bridge-api.sock")
	if got != want {
		t.Errorf("defaultSocketPath = %q, want %q", got, want)
	}
}

func TestDefaultSocketPathFallsBackToHome(t *testing.T) {
	getenv := func(k string) string {
		if k == "HOME" {
			return "/tmp/home"
		}
		return ""
	}
	got, err := defaultSocketPath(getenv)
	if err != nil {
		t.Fatalf("defaultSocketPath: %v", err)
	}
	want := filepath.Join("/tmp/home", ".local", "state", "osx-bridge-api", "osx-bridge-api.sock")
	if got != want {
		t.Errorf("defaultSocketPath = %q, want %q", got, want)
	}
}

func TestDefaultSocketPathErrorsWithNoEnv(t *testing.T) {
	getenv := func(string) string { return "" }
	if _, err := defaultSocketPath(getenv); err == nil {
		t.Error("defaultSocketPath returned no error with no XDG_STATE_HOME/HOME set")
	}
}

func TestRunPrintsVersion(t *testing.T) {
	// --version must short-circuit before ever touching a socket or the
	// calendar provider, so this is safe to run without darwin/EventKit.
	getenv := func(string) string { return "" }
	if code := run([]string{"--version"}, getenv); code != 0 {
		t.Errorf("run([--version]) = %d, want 0", code)
	}
}
