package config

import (
	"os"
	"path/filepath"
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
		d.FormatHoldRate != 0.01 || d.DoctorInterval != 24*time.Hour {
		t.Fatalf("defaults %+v", d)
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	body := "[remove]\nenable = true\nafter = \"10d\"\nmaxPerRun = 3\nmaxPerHour = 9\n[format]\nholdRate = 0.05\n[doctor]\ninterval = \"6h\"\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, true, env(map[string]string{"HOME": "/h"}))
	if err != nil || !c.RemoveEnable || c.RemoveAfter != 240*time.Hour || c.RemoveMaxPerRun != 3 || c.RemoveMaxPerHour != 9 ||
		c.FormatHoldRate != 0.05 || c.DoctorInterval != 6*time.Hour {
		t.Fatalf("%v %+v", err, c)
	}
	for _, bad := range []string{"[remove]\nmaxPerRun = 0\n", "[format]\nholdRate = 2\n", "[doctor]\ninterval = \"soon\"\n"} {
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
