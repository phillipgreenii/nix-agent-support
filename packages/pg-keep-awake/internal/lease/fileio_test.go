package lease

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAtomicModeAndNoTemp(t *testing.T) {
	d := newDir(t)
	for _, name := range []string{FileStatus, FileLastGood, "leases/x.json"} {
		if err := d.WriteAtomic(name, []byte("v1")); err != nil {
			t.Fatal(err)
		}
		if err := d.WriteAtomic(name, []byte("v2")); err != nil { // replace via rename
			t.Fatal(err)
		}
		p := filepath.Join(d.Path(), filepath.FromSlash(name))
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", name, fi.Mode().Perm())
		}
		if b, _ := os.ReadFile(p); string(b) != "v2" {
			t.Errorf("%s = %q", name, b)
		}
	}
	for _, dir := range []string{d.Path(), d.LeasesPath()} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Errorf("temp left in %s: %s", dir, e.Name())
			}
		}
	}
}

func TestWriteAtomicModeUnderPermissiveUmask(t *testing.T) {
	old := setUmask(0)
	defer setUmask(old)
	d := newDir(t)
	if err := d.WriteAtomic(FilePause, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(d.Path(), FilePause))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode under umask 0 = %04o", fi.Mode().Perm())
	}
}

func TestWriteAtomicRefusesEscapes(t *testing.T) {
	d := newDir(t)
	for _, rel := range []string{"../evil", "/etc/evil", "", ".", "leases/../../evil", ".."} {
		if err := d.WriteAtomic(rel, []byte("x")); err == nil {
			t.Errorf("WriteAtomic(%q) accepted", rel)
		}
	}
}

func TestWriteAtomicTempIsInSameDirectoryAndRenamed(t *testing.T) {
	d := newDir(t)
	// Make the final path a directory so the rename fails: the temp must be
	// cleaned up and must have been created beside the target.
	if err := os.Mkdir(filepath.Join(d.Path(), FileStatus), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteAtomic(FileStatus, []byte("x")); err == nil {
		t.Fatal("expected rename failure")
	}
	entries, _ := os.ReadDir(d.Path())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("failed write left %s", e.Name())
		}
	}
}

func TestTypedFilesRoundTrip(t *testing.T) {
	d := newDir(t)
	at := time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC)

	a := &AssertionFile{
		Anchor:     ProcDesc{PID: 11, StartUS: 22, Argv: []string{"pg-keep-awake", "anchor"}},
		Caffeinate: ProcDesc{PID: 33, StartUS: 44, Argv: []string{"caffeinate", "-i"}},
		Flags:      []string{"i"}, StartedAt: at, KeeperVersion: "v1",
		Params: AssertionParams{ReleasePct: 20, BlindPct: 30, WatchdogSeconds: 600, HeartbeatSeconds: 10, StateDir: "/abs"},
	}
	if err := d.WriteAssertion(a); err != nil {
		t.Fatal(err)
	}
	ga, err := d.ReadAssertion()
	if err != nil || ga.SchemaVersion != SchemaVersion || ga.Anchor.PID != 11 || ga.Params.StateDir != "/abs" || !ga.StartedAt.Equal(at) || ga.Caffeinate.Argv[1] != "-i" {
		t.Errorf("assertion round trip: %v %+v", err, ga)
	}

	cfgErr := "bad toml"
	until := at.Add(time.Hour)
	s := &StatusFile{
		WrittenAt:          at,
		Keeper:             StatusKeeper{PID: 9, Version: "v1", HeartbeatAgeS: 1.5, ConfigError: &cfgErr},
		Assertion:          StatusAssertion{Held: true, Flags: []string{"i"}, PID: 33},
		Paused:             &PauseFile{SchemaVersion: 1, Until: &until, CreatedAt: at, ByPID: 4412},
		Battery:            StatusBattery{Percent: 64, Gate: "ok", ReadOK: true},
		Leases:             []StatusLease{{ID: "o.k", Owner: "o", Key: "k", Kind: "run", Live: true, Reason: "client"}},
		LastRelease:        &StatusRelease{At: at, Reason: ReasonWatchdog},
		External:           []StatusExternal{{PID: 7, Process: "Safari", Type: "PreventUserIdleDisplaySleep", AgeS: 3}},
		HeartbeatIntervalS: 10, UnexplainedTicks: 2, LingerUntil: &until,
	}
	if err := d.WriteStatus(s); err != nil {
		t.Fatal(err)
	}
	gs, err := d.ReadStatus()
	if err != nil || gs.Keeper.PID != 9 || *gs.Keeper.ConfigError != cfgErr || gs.Paused == nil || gs.Paused.ByPID != 4412 ||
		gs.Battery.Percent != 64 || len(gs.Leases) != 1 || gs.LastRelease.Reason != ReasonWatchdog ||
		gs.External[0].Process != "Safari" || gs.HeartbeatIntervalS != 10 || gs.UnexplainedTicks != 2 || !gs.LingerUntil.Equal(until) {
		t.Errorf("status round trip: %v %+v", err, gs)
	}

	p := &PauseFile{CreatedAt: at, ByPID: 5} // no --for: until stays null
	if err := d.WritePause(p); err != nil {
		t.Fatal(err)
	}
	raw, _ := d.ReadFile(FilePause)
	if !strings.Contains(string(raw), `"until": null`) {
		t.Errorf("pause without --for must write until null: %s", raw)
	}
	gp, err := d.ReadPause()
	if err != nil || gp.Until != nil || gp.ByPID != 5 {
		t.Errorf("pause round trip: %v %+v", err, gp)
	}
	if err := d.RemovePause(); err != nil {
		t.Fatal(err)
	}
	if err := d.RemovePause(); err != nil {
		t.Errorf("RemovePause on absent file: %v", err)
	}
	if _, err := d.ReadPause(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadPause after remove = %v", err)
	}

	ae := &AnchorExitFile{At: at, Reason: ReasonReleaseThreshold, Percent: 19, PID: 11}
	if err := d.WriteAnchorExit(ae); err != nil {
		t.Fatal(err)
	}
	gae, err := d.ReadAnchorExit()
	if err != nil || gae.Reason != ReasonReleaseThreshold || gae.Percent != 19 {
		t.Errorf("anchor-exit round trip: %v %+v", err, gae)
	}
	if err := d.RemoveAnchorExit(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadAnchorExit(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadAnchorExit after remove = %v", err)
	}
}

func TestStatusOmitsAdditiveFieldsWhenUnset(t *testing.T) {
	d := newDir(t)
	if err := d.WriteStatus(&StatusFile{WrittenAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	gs, err := d.ReadStatus()
	if err != nil || gs.HeartbeatIntervalS != 0 || gs.LingerUntil != nil {
		t.Errorf("absent additive fields must read as zero: %v %+v", err, gs)
	}
}

func TestTypedReadersIgnoreUnknownFields(t *testing.T) {
	d := newDir(t)
	body := `{"schema_version":1,"until":null,"created_at":"2026-10-14T20:00:00Z","by_pid":4,"unheard_of":{"a":1}}`
	if err := os.WriteFile(filepath.Join(d.Path(), FilePause), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := d.ReadPause()
	if err != nil || p.ByPID != 4 {
		t.Errorf("unknown field tolerance: %v %+v", err, p)
	}
}

func TestFutureSchemaFilesUnreadableNotDeleted(t *testing.T) {
	d := newDir(t)
	cases := map[string]func() error{
		FileAssertion:  func() error { _, err := d.ReadAssertion(); return err },
		FileStatus:     func() error { _, err := d.ReadStatus(); return err },
		FilePause:      func() error { _, err := d.ReadPause(); return err },
		FileAnchorExit: func() error { _, err := d.ReadAnchorExit(); return err },
	}
	for name, read := range cases {
		path := filepath.Join(d.Path(), name)
		if err := os.WriteFile(path, []byte(`{"schema_version":2,"whatever":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		err := read()
		var ue *UnreadableError
		if !errors.Is(err, ErrUnreadable) || !errors.As(err, &ue) || ue.Version != 2 {
			t.Errorf("%s: want UnreadableError{2}, got %v", name, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was deleted: %v", name, err)
		}
	}
}

func TestTypedReadMissingAndGarbage(t *testing.T) {
	d := newDir(t)
	if _, err := d.ReadStatus(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing status = %v", err)
	}
	if err := os.WriteFile(filepath.Join(d.Path(), FileStatus), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := d.ReadStatus()
	if err == nil || errors.Is(err, ErrUnreadable) || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("garbage status = %v", err)
	}
}

func TestStateFileSymlinkRefused(t *testing.T) {
	d := newDir(t)
	target := filepath.Join(t.TempDir(), "t")
	if err := os.WriteFile(target, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(d.Path(), FileStatus)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadStatus(); !errors.Is(err, ErrNotRegular) {
		t.Errorf("symlinked status.json = %v, want ErrNotRegular", err)
	}
}

// assertion.json params carry fractional seconds (a 200ms heartbeat is 0.2) and
// whole seconds still encode as integers, so files written before the fields
// became fractional read back unchanged.
func TestAssertionParamsFractionalSeconds(t *testing.T) {
	d := newDir(t)
	a := &AssertionFile{Params: AssertionParams{WatchdogSeconds: 1.5, HeartbeatSeconds: 0.2, StateDir: "/abs"}}
	if err := d.WriteAssertion(a); err != nil {
		t.Fatal(err)
	}
	got, err := d.ReadAssertion()
	if err != nil || got.Params.WatchdogSeconds != 1.5 || got.Params.HeartbeatSeconds != 0.2 {
		t.Fatalf("fractional round trip: %v %+v", err, got)
	}
	raw, err := json.Marshal(AssertionParams{WatchdogSeconds: 600, HeartbeatSeconds: 10})
	if err != nil || !strings.Contains(string(raw), `"watchdog_seconds":600,`) || !strings.Contains(string(raw), `"heartbeat_seconds":10,`) {
		t.Fatalf("whole seconds must encode as integers: %v %s", err, raw)
	}
}
