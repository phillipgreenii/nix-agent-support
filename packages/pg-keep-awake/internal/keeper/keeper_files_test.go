package keeper

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

func TestSingletonLock(t *testing.T) {
	d := newDir(t)
	first, err := TryLockInstance(d)
	if err != nil {
		t.Fatalf("first TryLockInstance: %v", err)
	}
	if _, err := TryLockInstance(d); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second TryLockInstance = %v, want ErrAlreadyRunning", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("second Release = %v, want nil", err)
	}
	again, err := TryLockInstance(d)
	if err != nil {
		t.Fatalf("TryLockInstance after Release: %v", err)
	}
	_ = again.Release()
}

func TestSingletonLockIsPerStateDirectory(t *testing.T) {
	a, b := newDir(t), newDir(t)
	la, err := TryLockInstance(a)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = la.Release() }()
	lb, err := TryLockInstance(b)
	if err != nil {
		t.Fatalf("a keeper in another state directory must not be blocked: %v", err)
	}
	_ = lb.Release()
}

func TestTouchHeartbeatCreatesAndUpdates(t *testing.T) {
	d := newDir(t)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := TouchHeartbeat(d, at); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d.Path(), FileHeartbeat)
	fi, err := os.Stat(path)
	if err != nil || !fi.ModTime().Equal(at) || fi.Size() != 0 {
		t.Fatalf("heartbeat = %v, %v; want an empty file at %v", fi, err, at)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	later := at.Add(time.Hour)
	if err := TouchHeartbeat(d, later); err != nil {
		t.Fatal(err)
	}
	if got := heartbeatMTime(d); !got.Equal(later) {
		t.Fatalf("mtime = %v, want %v", got, later)
	}
}

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigAbsentFileIsDefaultsWithoutError(t *testing.T) {
	d := newDir(t)
	cfg, errText, warn := LoadConfig(d, filepath.Join(t.TempDir(), "config.toml"))
	if errText != "" || len(warn) != 0 {
		t.Fatalf("absent file: err=%q warn=%v", errText, warn)
	}
	if cfg.HeartbeatInterval != config.DefaultHeartbeatInterval {
		t.Fatalf("not the defaults: %+v", cfg)
	}
	if exists(t, filepath.Join(d.Path(), lease.FileLastGood)) {
		t.Fatal("config.last-good.toml written although there was no file to record")
	}
}

func TestLoadConfigGoodParseRecordsLastGood(t *testing.T) {
	d := newDir(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	const body = "heartbeat_interval = \"7s\"\nlinger = \"45s\"\n"
	writeConfig(t, path, body)
	cfg, errText, warn := LoadConfig(d, path)
	if errText != "" || len(warn) != 0 {
		t.Fatalf("good file: err=%q warn=%v", errText, warn)
	}
	if cfg.HeartbeatInterval != 7*time.Second || cfg.Linger != 45*time.Second {
		t.Fatalf("cfg = %+v", cfg)
	}
	lg := filepath.Join(d.Path(), lease.FileLastGood)
	got, err := os.ReadFile(lg)
	if err != nil || string(got) != body {
		t.Fatalf("config.last-good.toml = %q, %v; want %q", got, err, body)
	}
	if fi, _ := os.Stat(lg); fi.Mode().Perm() != 0o600 {
		t.Fatalf("last-good mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestLoadConfigBadFileFallsBackToLastGood(t *testing.T) {
	d := newDir(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "heartbeat_interval = \"7s\"\n")
	if _, errText, _ := LoadConfig(d, path); errText != "" {
		t.Fatalf("good load: %q", errText)
	}
	writeConfig(t, path, "heartbeat_interval = \"not a duration\"\n")
	cfg, errText, _ := LoadConfig(d, path)
	if errText == "" {
		t.Fatal("bad file reported no error")
	}
	if cfg.HeartbeatInterval != 7*time.Second {
		t.Fatalf("did not fall back to the last-good values: %+v", cfg)
	}
	// A bad file never overwrites the last-good copy.
	got, _ := os.ReadFile(filepath.Join(d.Path(), lease.FileLastGood))
	if string(got) != "heartbeat_interval = \"7s\"\n" {
		t.Fatalf("last-good overwritten by a bad file: %q", got)
	}
}

func TestLoadConfigBadFileWithoutLastGoodFallsBackToDefaults(t *testing.T) {
	d := newDir(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "this is = = not toml\n")
	cfg, errText, _ := LoadConfig(d, path)
	if errText == "" {
		t.Fatal("bad file reported no error")
	}
	if cfg.HeartbeatInterval != config.DefaultHeartbeatInterval || cfg.Linger != config.DefaultLinger {
		t.Fatalf("not the defaults: %+v", cfg)
	}
	if exists(t, filepath.Join(d.Path(), lease.FileLastGood)) {
		t.Fatal("last-good written from a bad file")
	}
}

func TestLoadConfigBadLastGoodAndBadFileFallsBackToDefaults(t *testing.T) {
	d := newDir(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "linger = \"-5s\"\n")
	writeConfig(t, filepath.Join(d.Path(), lease.FileLastGood), "garbage = = =\n")
	cfg, errText, _ := LoadConfig(d, path)
	if errText == "" || cfg.Linger != config.DefaultLinger {
		t.Fatalf("cfg=%+v err=%q; want defaults and an error", cfg, errText)
	}
}
