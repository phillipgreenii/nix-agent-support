package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/tmux"
)

func TestExitRequestRoundTrip(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	if has, err := HasExitRequest(stateDir, "example-a"); err != nil || has {
		t.Fatalf("before any write: HasExitRequest = %v, %v; want false, nil", has, err)
	}
	if err := WriteExitRequest(stateDir, "example-a", at); err != nil {
		t.Fatal(err)
	}
	reqDir := filepath.Join(stateDir, "exit-requests")
	assertMode(t, reqDir, 0o700)
	assertMode(t, filepath.Join(reqDir, "example-a"), 0o600)
	body, err := os.ReadFile(filepath.Join(reqDir, "example-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "2026-11-17T10:00:00Z") {
		t.Errorf("request body %q does not name the time", body)
	}

	for i := range 2 { // it never deletes
		if has, err := HasExitRequest(stateDir, "example-a"); err != nil || !has {
			t.Fatalf("check %d: HasExitRequest = %v, %v; want true, nil", i+1, has, err)
		}
	}
	if has, err := HasExitRequest(stateDir, "example-b"); err != nil || has {
		t.Errorf("another id: HasExitRequest = %v, %v; want false, nil", has, err)
	}

	if err := ClearExitRequest(stateDir, "example-a"); err != nil {
		t.Fatal(err)
	}
	if has, err := HasExitRequest(stateDir, "example-a"); err != nil || has {
		t.Errorf("after clear: HasExitRequest = %v, %v; want false, nil", has, err)
	}
	if err := ClearExitRequest(stateDir, "example-a"); err != nil {
		t.Errorf("clear of a missing request = %v, want nil", err)
	}
	if err := ClearExitRequest(filepath.Join(t.TempDir(), "never-created"), "example-a"); err != nil {
		t.Errorf("clear with no request directory = %v, want nil", err)
	}

	if ValidID("../x") {
		t.Error(`ValidID("../x") = true`)
	}
}

// A second request for the same id replaces the first.
func TestWriteExitRequestTwice(t *testing.T) {
	stateDir := t.TempDir()
	t1 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{t1, t1.Add(time.Minute)} {
		if err := WriteExitRequest(stateDir, "example-a", at); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join(stateDir, "exit-requests", "example-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "2026-11-17T10:01:00Z") {
		t.Errorf("request body %q, want the second time", body)
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"example-a":   true,
		"a":           true,
		"0-9":         true,
		"":            false,
		"../x":        false,
		"..":          false,
		"a/b":         false,
		"Example":     false,
		"example_a":   false,
		"example a":   false,
		"example-a\n": false,
	} {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

// An empty or relative state dir is refused by every state-dir helper, so
// nothing is ever created under the process's working directory.
func TestStateHelpersRequireAbsoluteStateDir(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for _, dir := range []string{"", "state", "./state"} {
		errs := map[string]error{
			"WriteExitRequest": WriteExitRequest(dir, "example-a", at),
			"ClearExitRequest": ClearExitRequest(dir, "example-a"),
			"WriteLastExit":    WriteLastExit(dir, "example-a", tmux.DeadInfo{Status: 1}, time.Second, at),
			"Flush":            NewStatusBoard(filepath.Join(dir, "status.json"), DaemonStatus{PID: 1}).Flush(at),
		}
		_, errs["HasExitRequest"] = HasExitRequest(dir, "example-a")
		for name, err := range errs {
			if err == nil || !strings.Contains(err.Error(), "absolute") {
				t.Errorf("%s with state dir %q: err = %v, want one saying it is not absolute", name, dir, err)
			}
		}
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("working directory holds %v after refused calls", names(entries))
	}
}

// An id that is not [a-z0-9-]+ becomes no path: every call refuses it and
// writes nothing.
func TestExitRequestRefusesBadID(t *testing.T) {
	stateDir := t.TempDir()
	for _, id := range []string{"../x", "", "a/b"} {
		if err := WriteExitRequest(stateDir, id, time.Now()); err == nil {
			t.Errorf("WriteExitRequest(%q) succeeded", id)
		}
		if _, err := HasExitRequest(stateDir, id); err == nil {
			t.Errorf("HasExitRequest(%q) succeeded", id)
		}
		if err := ClearExitRequest(stateDir, id); err == nil {
			t.Errorf("ClearExitRequest(%q) succeeded", id)
		}
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("state dir holds %v after refused calls", names(entries))
	}
}

func TestWriteRemovesOnlyStaleTemps(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	reqDir := filepath.Join(stateDir, "exit-requests")
	if err := os.MkdirAll(reqDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(reqDir, ".example-a.tmp-old")
	fresh := filepath.Join(reqDir, ".example-a.tmp-new")
	other := filepath.Join(reqDir, ".example-b.tmp-old")
	for _, p := range []string{stale, fresh, other} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleTempAge)
	for _, p := range []string{stale, other} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteExitRequest(stateDir, "example-a", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp of the same file survived: %v", err)
	}
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
}
