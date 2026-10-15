package main

import (
	"os"
	"testing"
)

func TestReleaseDefaultAndNamedKey(t *testing.T) {
	sd := useStateDir(t)
	for _, key := range []string{"default", "other"} {
		if code, _, stderr := runCLI(t, "acquire", "--owner", "t", "--key", key, "--for", "1m"); code != 0 {
			t.Fatalf("acquire %s: exit %d, stderr %q", key, code, stderr)
		}
	}
	if code, _, stderr := runCLI(t, "release", "--owner", "t"); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr)
	}
	if fileExists(leasePath(sd, "t.default")) {
		t.Error("release without --key left key default")
	}
	if !fileExists(leasePath(sd, "t.other")) {
		t.Error("release without --key removed another key")
	}
	if code, _, _ := runCLI(t, "release", "--owner", "t", "--key", "other"); code != 0 {
		t.Fatalf("release --key: exit %d", code)
	}
	if fileExists(leasePath(sd, "t.other")) {
		t.Error("release --key left the lease")
	}
}

func TestReleaseWhenNothingToRelease(t *testing.T) {
	useStateDir(t) // state directory does not exist yet
	if code, _, stderr := runCLI(t, "release", "--owner", "t"); code != 0 {
		t.Fatalf("no state dir: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, "release", "--owner", "t", "--all"); code != 0 {
		t.Fatalf("no state dir --all: exit %d, stderr %q", code, stderr)
	}
	if code, _, _ := runCLI(t, "acquire", "--owner", "t", "--for", "1m"); code != 0 {
		t.Fatal("acquire failed")
	}
	for i := 0; i < 2; i++ {
		if code, _, stderr := runCLI(t, "release", "--owner", "t"); code != 0 {
			t.Fatalf("release #%d: exit %d, stderr %q", i, code, stderr)
		}
	}
	if code, _, stderr := runCLI(t, "release", "--owner", "nobody", "--key", "none"); code != 0 {
		t.Fatalf("missing lease: exit %d, stderr %q", code, stderr)
	}
}

func TestReleaseAllRemovesOnlyThatOwner(t *testing.T) {
	sd := useStateDir(t)
	for _, a := range [][]string{{"t", "a"}, {"t", "b"}, {"t", "default"}, {"u", "a"}} {
		if code, _, stderr := runCLI(t, "acquire", "--owner", a[0], "--key", a[1], "--for", "1m"); code != 0 {
			t.Fatalf("acquire %v: exit %d, stderr %q", a, code, stderr)
		}
	}
	if code, _, stderr := runCLI(t, "release", "--owner", "t", "--all"); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr)
	}
	for _, id := range []string{"t.a", "t.b", "t.default"} {
		if fileExists(leasePath(sd, id)) {
			t.Errorf("--all left %s", id)
		}
	}
	if !fileExists(leasePath(sd, "u.a")) {
		t.Error("--all removed another owner's lease")
	}
}

func TestReleaseUsageErrors(t *testing.T) {
	useStateDir(t)
	cases := map[string][]string{
		"no owner":      {"release"},
		"key and all":   {"release", "--owner", "t", "--key", "k", "--all"},
		"bad owner":     {"release", "--owner", "a b"},
		"stray arg":     {"release", "--owner", "t", "extra"},
		"unknown flags": {"release", "--owner", "t", "--bogus"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code, _, stderr := runCLI(t, args...); code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr %q)", code, stderr)
			}
		})
	}
}

func TestReleaseDoesNotCreateStateDir(t *testing.T) {
	sd := useStateDir(t)
	runCLI(t, "release", "--owner", "t")
	if _, err := os.Stat(sd); err == nil {
		t.Error("release created the state directory")
	}
}
