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
