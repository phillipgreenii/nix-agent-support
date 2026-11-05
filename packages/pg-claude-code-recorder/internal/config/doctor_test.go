package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorRollingKeys(t *testing.T) {
	e := env(map[string]string{"HOME": "/h"})
	d := Defaults(e)
	if d.DoctorBytesPerSlice != 256<<20 || d.DoctorSliceEvery != time.Minute || d.DoctorAllowSlowCycle {
		t.Fatalf("defaults %d %v %v", d.DoctorBytesPerSlice, d.DoctorSliceEvery, d.DoctorAllowSlowCycle)
	}
	load := func(body string) (Config, error) {
		p := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(p, true, e)
	}
	c, err := load("[doctor]\nbytes_per_slice = 1048576\nslice_every = \"30s\"\nallow_slow_cycle = true\n")
	if err != nil || c.DoctorBytesPerSlice != 1<<20 || c.DoctorSliceEvery != 30*time.Second || !c.DoctorAllowSlowCycle {
		t.Fatalf("%v %+v", err, c)
	}
	// 0 is the documented escape to the old synchronous doctor
	c, err = load("[doctor]\nbytes_per_slice = 0\n")
	if err != nil || c.DoctorBytesPerSlice != 0 {
		t.Fatalf("%v %d", err, c.DoctorBytesPerSlice)
	}
	for _, bad := range []struct{ body, key string }{
		{"[doctor]\nbytes_per_slice = -1\n", "doctor.bytes_per_slice"},
		{"[doctor]\nslice_every = \"500ms\"\n", "doctor.slice_every"},
	} {
		if _, err := load(bad.body); err == nil || !strings.Contains(err.Error(), bad.key) {
			t.Fatalf("%q: err %v, want one naming %s", bad.body, err, bad.key)
		}
	}
}
