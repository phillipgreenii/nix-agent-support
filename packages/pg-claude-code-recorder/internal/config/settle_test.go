package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSettleDefaultsAndKeys(t *testing.T) {
	d := Defaults(env(map[string]string{"HOME": "/h"}))
	if d.SettleAfter != 24*time.Hour || d.HeldRecheck != time.Hour || d.MinFreeBytes != 20<<30 {
		t.Fatalf("defaults %+v", d)
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte("[settle]\nafter = \"2h\"\n[held]\nrecheck = \"5m\"\nalertAfter = \"4d\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, true, env(map[string]string{"HOME": "/h"}))
	if err != nil || c.SettleAfter != 2*time.Hour || c.HeldRecheck != 5*time.Minute || c.HeldMaxAge != 96*time.Hour {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestRemovalAndDoctorKeys(t *testing.T) {
	d := Defaults(env(map[string]string{"HOME": "/h"}))
	if d.RemoveEnable || d.RemoveAfter != 30*24*time.Hour || d.RemoveMaxPerRun != 50 || d.RemoveMaxPerHour != 200 ||
		d.DoctorInterval != 24*time.Hour {
		t.Fatalf("defaults %+v", d)
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	body := "[remove]\nenable = true\nafter = \"10d\"\nmaxPerRun = 3\nmaxPerHour = 9\n[doctor]\ninterval = \"6h\"\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, true, env(map[string]string{"HOME": "/h"}))
	if err != nil || !c.RemoveEnable || c.RemoveAfter != 240*time.Hour || c.RemoveMaxPerRun != 3 || c.RemoveMaxPerHour != 9 ||
		c.DoctorInterval != 6*time.Hour {
		t.Fatalf("%v %+v", err, c)
	}
	for _, bad := range []string{"[remove]\nmaxPerRun = 0\n", "[doctor]\ninterval = \"soon\"\n"} {
		if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, true, env(map[string]string{"HOME": "/h"})); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestQueryTimeoutDefaultsTo30sAndIsConfigurable(t *testing.T) {
	d := Defaults(env(map[string]string{"HOME": "/h"}))
	if d.QueryTimeout != 30*time.Second {
		t.Fatalf("default %v", d.QueryTimeout)
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte("[query]\ntimeout = \"5s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, true, env(map[string]string{"HOME": "/h"}))
	if err != nil || c.QueryTimeout != 5*time.Second {
		t.Fatalf("%v %v", err, c.QueryTimeout)
	}
}

func TestSettleLoopKeys(t *testing.T) {
	e := env(map[string]string{"HOME": "/h"})
	d := Defaults(e)
	if d.SettleMode != "scan" || d.SettleReconcile != time.Hour || d.SettleMinWake != 5*time.Second || d.SettleTick != 5*time.Minute {
		t.Fatalf("defaults %q %v %v", d.SettleMode, d.SettleReconcile, d.SettleMinWake)
	}
	load := func(body string) (Config, error) {
		p := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(p, true, e)
	}
	c, err := load("[settle]\nmode = \"event\"\nreconcile = \"2h\"\nmin_wake = \"10s\"\n")
	if err != nil || c.SettleMode != "event" || c.SettleReconcile != 2*time.Hour || c.SettleMinWake != 10*time.Second {
		t.Fatalf("%v %+v", err, c)
	}
	// the floors are inclusive
	if _, err := load("[settle]\nreconcile = \"1m\"\nmin_wake = \"1s\"\ntick = \"1s\"\n"); err != nil {
		t.Fatalf("floor values rejected: %v", err)
	}
	for _, bad := range []struct{ body, key string }{
		{"[settle]\nmode = \"auto\"\n", "settle.mode"},
		{"[settle]\nreconcile = \"59s\"\n", "settle.reconcile"},
		{"[settle]\nmin_wake = \"999ms\"\n", "settle.min_wake"},
		{"[settle]\ntick = \"999ms\"\n", "settle.tick"},
		{"[settle]\nreconcile = \"soon\"\n", "settle.reconcile"},
	} {
		if _, err := load(bad.body); err == nil || !strings.Contains(err.Error(), bad.key) {
			t.Fatalf("%q: err %v, want one naming %s", bad.body, err, bad.key)
		}
	}
}

func TestRemovalDueKeys(t *testing.T) {
	d := Defaults(env(map[string]string{"HOME": "/h"}))
	if d.RemoveDueShare != 0.10 || d.RemoveDueEvery != 30*24*time.Hour {
		t.Fatalf("defaults %v %v", d.RemoveDueShare, d.RemoveDueEvery)
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte("[remove]\ndueShare = 0.25\ndueEvery = \"14d\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, true, env(map[string]string{"HOME": "/h"}))
	if err != nil || c.RemoveDueShare != 0.25 || c.RemoveDueEvery != 14*24*time.Hour {
		t.Fatalf("%v %+v", err, c)
	}
	for _, bad := range []string{"[remove]\ndueShare = 0\n", "[remove]\ndueShare = 1.5\n", "[remove]\ndueEvery = \"soon\"\n"} {
		if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, true, env(map[string]string{"HOME": "/h"})); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestEndsSweepEveryKey(t *testing.T) {
	e := env(map[string]string{"HOME": "/h"})
	if d := Defaults(e); d.EndsSweepEvery != 10*time.Minute {
		t.Fatalf("default %v", d.EndsSweepEvery)
	}
	load := func(body string) (Config, error) {
		p := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(p, true, e)
	}
	if c, err := load("[ends]\nsweep_every = \"30m\"\n"); err != nil || c.EndsSweepEvery != 30*time.Minute {
		t.Fatalf("%v %v", err, c.EndsSweepEvery)
	}
	if _, err := load("[ends]\nsweep_every = \"1s\"\n"); err != nil {
		t.Fatalf("floor rejected: %v", err)
	}
	if _, err := load("[ends]\nsweep_every = \"999ms\"\n"); err == nil || !strings.Contains(err.Error(), "ends.sweep_every") {
		t.Fatalf("err %v", err)
	}
}
