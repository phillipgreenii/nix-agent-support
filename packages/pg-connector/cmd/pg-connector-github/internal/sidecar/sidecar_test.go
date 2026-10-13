package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func noEnv(string) string { return "" }

// cfgWith returns the default config with pr.posted_state_dir set to dir (or
// left unset for "").
func cfgWith(dir string) *config.Config {
	cfg := config.Defaults()
	cfg.PR.PostedStateDir = dir
	return cfg
}

func TestOpenDerivesTheStateHomeInOrder(t *testing.T) {
	all := map[string]string{posted.EnvStateDir: "/e/state", "XDG_STATE_HOME": "/e/xdg", "HOME": "/e/home"}
	for _, tc := range []struct {
		name    string
		setting string
		env     map[string]string
		home    string
	}{
		{"the setting wins over every variable", "/s/home", all, "/s/home"},
		{"the setting alone", "/s/home", nil, "/s/home"},
		{"the override variable comes next", "", all, "/e/state"},
		{"then XDG_STATE_HOME", "", map[string]string{"XDG_STATE_HOME": "/e/xdg", "HOME": "/e/home"}, "/e/xdg/pg-connector-pr-github"},
		{"then HOME", "", map[string]string{"HOME": "/e/home"}, "/e/home/.local/state/pg-connector-pr-github"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, locker, err := Open(cfgWith(tc.setting), envOf(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			if want := tc.home + "/posted"; store.Dir != want {
				t.Errorf("store dir = %q, want %q", store.Dir, want)
			}
			if want := tc.home + "/locks"; locker.Dir != want {
				t.Errorf("locker dir = %q, want %q", locker.Dir, want)
			}
			// The old backend's bounded wait, with the default retry interval.
			if locker.Wait != posted.LockWait || locker.Poll != 0 {
				t.Errorf("locker wait/poll = %v/%v, want %v/0", locker.Wait, locker.Poll, posted.LockWait)
			}
		})
	}
}

func TestOpenWithNoStateHomeNamesTheSetting(t *testing.T) {
	for name, open := range map[string]func() (posted.Store, posted.Locker, error){
		"empty environment": func() (posted.Store, posted.Locker, error) { return Open(cfgWith(""), noEnv) },
		"nil environment":   func() (posted.Store, posted.Locker, error) { return Open(cfgWith(""), nil) },
		"nil config":        func() (posted.Store, posted.Locker, error) { return Open(nil, noEnv) },
		"only unrelated variables": func() (posted.Store, posted.Locker, error) {
			return Open(cfgWith(""), envOf(map[string]string{"PATH": "/usr/bin", "XDG_CONFIG_HOME": "/c", config.EnvStateDir: "/d"}))
		},
	} {
		store, locker, err := open()
		if err == nil {
			t.Errorf("%s: want an error, got store %+v locker %+v", name, store, locker)
			continue
		}
		if !errors.Is(err, ErrNoStateHome) {
			t.Errorf("%s: error %v must wrap ErrNoStateHome", name, err)
		}
		for _, want := range []string{"pr.posted_state_dir", posted.EnvStateDir, "XDG_STATE_HOME", "HOME"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q must name %q", name, err, want)
			}
		}
		if store != (posted.Store{}) || locker != (posted.Locker{}) {
			t.Errorf("%s: a failed Open must return zero values, got %+v %+v", name, store, locker)
		}
	}
}

func TestOpenToleratesNilConfigAndNilGetenv(t *testing.T) {
	// A nil config is an unset setting: the environment decides.
	store, _, err := Open(nil, envOf(map[string]string{"HOME": "/e/home"}))
	if err != nil || store.Dir != "/e/home/.local/state/pg-connector-pr-github/posted" {
		t.Fatalf("nil config: %v %+v", err, store)
	}
	// A nil getenv is an empty environment: the setting still resolves.
	store, _, err = Open(cfgWith("/s/home"), nil)
	if err != nil || store.Dir != "/s/home/posted" {
		t.Fatalf("nil getenv: %v %+v", err, store)
	}
}

// Open reads the environment only through the getenv it is given, which is the
// daemon's own: with every variable it could derive a home from pointed at a
// decoy in the PROCESS environment, a resolution that is handed nothing still
// fails, one that is handed a home uses that home, and nothing lands in the
// decoy. This stands for "the daemon never reads a client's per-call
// environment": the injected function is the only door.
func TestOpenNeverReadsTheProcessEnvironment(t *testing.T) {
	decoy := t.TempDir()
	for _, k := range []string{posted.EnvStateDir, "XDG_STATE_HOME", "HOME"} {
		t.Setenv(k, decoy)
	}
	if _, _, err := Open(cfgWith(""), noEnv); !errors.Is(err, ErrNoStateHome) {
		t.Fatalf("an empty injected environment must not fall back to the process one: %v", err)
	}
	if _, _, err := Open(nil, nil); !errors.Is(err, ErrNoStateHome) {
		t.Fatalf("nil config and nil getenv must not fall back to the process environment: %v", err)
	}

	injected := t.TempDir()
	store, locker, err := Open(cfgWith(""), envOf(map[string]string{"HOME": injected}))
	if err != nil {
		t.Fatal(err)
	}
	wantHome := filepath.Join(injected, ".local", "state", "pg-connector-pr-github")
	if store.Dir != filepath.Join(wantHome, "posted") || locker.Dir != filepath.Join(wantHome, "locks") {
		t.Fatalf("dirs = %q %q, want under %q", store.Dir, locker.Dir, wantHome)
	}
	if err := store.Save("owner", "repo", 1, posted.State{Fingerprints: []string{"aaaaaaaaaaaaaaaa"}}); err != nil {
		t.Fatal(err)
	}
	lock, err := locker.Acquire("owner", "repo", 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Release()
	entries, err := os.ReadDir(decoy)
	if err != nil || len(entries) != 0 {
		t.Fatalf("nothing may be written to the process environment's directories: %v %v", entries, err)
	}
}

// The files and the lock keep the old backend's names and locations, so the old
// binary finds every record the daemon wrote and the operator can still delete
// one by hand.
func TestOpenedStoreAndLockerKeepTheOldNamesAndLocations(t *testing.T) {
	home := t.TempDir()
	store, locker, err := Open(cfgWith(home), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	var st posted.State
	st.AddFingerprints("aaaaaaaaaaaaaaaa")
	st.AddBodyHead("abc1234")
	if err := store.Save("owner", "repo", 7, st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "posted", "owner__repo__7.json")); err != nil {
		t.Fatalf("sidecar file is not at <home>/posted/<owner>__<repo>__<n>.json: %v", err)
	}
	lock, err := locker.Acquire("owner", "repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if _, err := os.Stat(filepath.Join(home, "locks", "owner__repo__7.lock")); err != nil {
		t.Fatalf("lock file is not at <home>/locks/<owner>__<repo>__<n>.lock: %v", err)
	}
	got, err := store.Load("owner", "repo", 7)
	if err != nil || !got.Has("aaaaaaaaaaaaaaaa") {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

// A sidecar file the old backend wrote (format version 1, as it writes it) is
// read as is: this is what a daemon started after cutover finds, and what a
// rolled-back old binary finds of the daemon's writes.
func TestOpenedStoreReadsAFileInTheOldBackendsFormat(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "posted")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const written = `{
  "version": 1,
  "fingerprints": [
    "aaaaaaaaaaaaaaaa",
    "bbbbbbbbbbbbbbbb"
  ],
  "body_heads": [
    "abc1234"
  ],
  "last_append": {
    "at": "2026-10-06T00:00:00Z",
    "added": 2,
    "head": "abc1234"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "owner__repo__3.json"), []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _, err := Open(cfgWith(home), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("owner", "repo", 3)
	if err != nil {
		t.Fatal(err)
	}
	want := posted.State{
		Version:      posted.StateVersion,
		Fingerprints: []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"},
		BodyHeads:    []string{"abc1234"},
		LastAppend:   &posted.LastAppend{At: "2026-10-06T00:00:00Z", Added: 2, Head: "abc1234"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	// And a write of the same state reproduces that file byte for byte, so a
	// rollback to the old binary reads the daemon's records without a migration.
	if err := store.Save("owner", "repo", 3, got); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(dir, "owner__repo__3.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != written {
		t.Fatalf("rewritten sidecar differs from the old backend's shape:\n%s\nwant:\n%s", again, written)
	}
}

// A corrupt sidecar stays a refusal through the opened store: Load reports
// ErrCorrupt and leaves the file for the operator to delete.
func TestOpenedStoreRefusesACorruptSidecar(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "posted")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "owner__repo__9.json")
	if err := os.WriteFile(path, []byte(`{"version": 1, "fingerprints": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _, err := Open(cfgWith(home), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("owner", "repo", 9); !errors.Is(err, posted.ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != `{"version": 1, "fingerprints": [` {
		t.Fatalf("a corrupt file must be left in place: %q %v", b, err)
	}
	// Deleting it by hand, the operator's lever, makes the PR readable again.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if st, err := store.Load("owner", "repo", 9); err != nil || len(st.Fingerprints) != 0 {
		t.Fatalf("after delete: %+v %v", st, err)
	}
}
