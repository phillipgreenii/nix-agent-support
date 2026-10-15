package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

func anchorArgs(sd, hb string, extra ...string) []string {
	base := []string{
		"anchor", "--release-pct", "20", "--blind-pct", "30", "--watchdog", "300ms",
		"--heartbeat", hb, "--interval", "20ms", "--state-dir", sd,
	}
	return append(base, extra...)
}

func TestAnchorUsageErrorsWriteNothing(t *testing.T) {
	d, err := lease.EnsureDir(filepath.Join(t.TempDir(), "state", lease.DirName))
	if err != nil {
		t.Fatal(err)
	}
	sd, hb := d.Path(), filepath.Join(t.TempDir(), "hb")
	full := anchorArgs(sd, hb)[1:]
	cases := map[string][]string{
		"no flags":          {"anchor"},
		"unknown flag":      anchorArgs(sd, hb, "--bogus", "1"),
		"positional arg":    anchorArgs(sd, hb, "extra"),
		"bad duration":      append([]string{"anchor", "--watchdog", "soon"}, full[2:]...),
		"zero interval":     append([]string{"anchor"}, replaceFlag(full, "--interval", "0s")...),
		"release too large": append([]string{"anchor"}, replaceFlag(full, "--release-pct", "101")...),
		"blind negative":    append([]string{"anchor"}, replaceFlag(full, "--blind-pct", "-1")...),
		"empty state dir":   append([]string{"anchor"}, replaceFlag(full, "--state-dir", "")...),
	}
	for _, drop := range []string{"--release-pct", "--blind-pct", "--watchdog", "--heartbeat", "--interval", "--state-dir"} {
		cases["missing "+drop] = append([]string{"anchor"}, dropFlag(full, drop)...)
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code, stdout, stderr := runCLI(t, args...); code != exitUsage || stdout != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit 2", code, stdout, stderr)
			}
			if _, err := os.Stat(filepath.Join(sd, lease.FileAnchorExit)); err == nil {
				t.Fatal("anchor-exit.json written on a usage error")
			}
		})
	}
}

func replaceFlag(args []string, name, val string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == name {
			out[i+1] = val
		}
	}
	return out
}

func dropFlag(args []string, name string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == name {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func TestAnchorWatchdogExit(t *testing.T) {
	d, err := lease.EnsureDir(filepath.Join(t.TempDir(), "state", lease.DirName))
	if err != nil {
		t.Fatal(err)
	}
	bat := filepath.Join(t.TempDir(), "battery")
	if err := os.WriteFile(bat, []byte("64 ac\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(policy.BatterySourceEnv, "file:"+bat)
	hb := filepath.Join(t.TempDir(), "hb")
	if err := os.WriteFile(hb, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if code, stdout, stderr := runCLI(t, anchorArgs(d.Path(), hb)...); code != 0 || stdout != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if el := time.Since(start); el < 300*time.Millisecond {
		t.Errorf("exited after %s, before the 300ms watchdog", el)
	}
	got, err := d.ReadAnchorExit()
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "watchdog" || got.Percent != 64 || got.PID != os.Getpid() || got.SchemaVersion != lease.SchemaVersion {
		t.Fatalf("anchor-exit.json = %+v", got)
	}
}

func TestAnchorReleaseExit(t *testing.T) {
	d, err := lease.EnsureDir(filepath.Join(t.TempDir(), "state", lease.DirName))
	if err != nil {
		t.Fatal(err)
	}
	bat := filepath.Join(t.TempDir(), "battery")
	if err := os.WriteFile(bat, []byte("19 battery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(policy.BatterySourceEnv, "file:"+bat)
	if code, _, stderr := runCLI(t, anchorArgs(d.Path(), filepath.Join(t.TempDir(), "hb"))...); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	got, err := d.ReadAnchorExit()
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "release_threshold" || got.Percent != 19 {
		t.Fatalf("anchor-exit.json = %+v", got)
	}
}

func TestAnchorMissingStateDirIsNotCreated(t *testing.T) {
	sd := filepath.Join(t.TempDir(), "absent")
	if code, _, _ := runCLI(t, anchorArgs(sd, filepath.Join(t.TempDir(), "hb"))...); code == 0 {
		t.Fatal("exit 0 for a missing state directory")
	}
	if _, err := os.Stat(sd); err == nil {
		t.Fatal("state directory was created")
	}
}
