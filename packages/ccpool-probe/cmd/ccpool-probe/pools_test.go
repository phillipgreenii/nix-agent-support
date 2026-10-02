package main

import (
	"os"
	"path/filepath"
	"testing"
)

// mkRegistry builds a ccpool-style pool registry (a dir of symlinks, one
// per pool) under a temp dir and returns it plus the real pool dirs.
func mkRegistry(t *testing.T, poolNames ...string) (reg string, dirs map[string]string) {
	t.Helper()
	root := t.TempDir()
	reg = filepath.Join(root, "pools.d")
	if err := os.MkdirAll(reg, 0o700); err != nil {
		t.Fatal(err)
	}
	dirs = map[string]string{}
	for i, name := range poolNames {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		real, err := filepath.EvalSymlinks(d)
		if err != nil {
			t.Fatal(err)
		}
		dirs[name] = real
		if err := os.Symlink(d, filepath.Join(reg, "cc-"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	return reg, dirs
}

func TestDiscoverPoolsAmbientPlusEveryRegisteredPool(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	reg, dirs := mkRegistry(t, "pg-router-ccpool-worker", "pg-router-ccpool-review")
	got := discoverPools(reg, noopWarn)
	if len(got) != 3 {
		t.Fatalf("want ambient + 2 role pools, got %+v", got)
	}
	if got[0].Dir != "" || got[0].Label != ambientPoolLabel {
		t.Errorf("ambient pool must come first, got %+v", got[0])
	}
	// registered pools sorted by label: review < worker
	if got[1].Label != "pg-router-ccpool-review" || got[1].Dir != dirs["pg-router-ccpool-review"] {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[2].Label != "pg-router-ccpool-worker" || got[2].Dir != dirs["pg-router-ccpool-worker"] {
		t.Errorf("got[2] = %+v", got[2])
	}
}

func TestDiscoverPoolsSkipsStaleAndTempEntriesAndWarns(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	reg, _ := mkRegistry(t, "live-pool")
	if err := os.Symlink(filepath.Join(filepath.Dir(reg), "vanished"), filepath.Join(reg, "cc-stale")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(reg), filepath.Join(reg, "cc-x.tmp-123")); err != nil {
		t.Fatal(err)
	}
	warn, msgs := warnCollector()
	got := discoverPools(reg, warn)
	if len(got) != 2 || got[1].Label != "live-pool" {
		t.Fatalf("want ambient + live-pool only, got %+v", got)
	}
	if len(*msgs) != 1 {
		t.Errorf("want exactly one stale-entry warning, got %v", *msgs)
	}
}

func TestDiscoverPoolsDedupesAmbientRegisteredPool(t *testing.T) {
	reg, dirs := mkRegistry(t, "pg-router-ccpool-worker", "pg-router-ccpool-review")
	t.Setenv("CCPOOL_POOL", dirs["pg-router-ccpool-worker"])
	got := discoverPools(reg, noopWarn)
	if len(got) != 2 || got[1].Label != "pg-router-ccpool-review" {
		t.Fatalf("the ambient pool must not be scanned twice; got %+v", got)
	}
}

func TestDiscoverPoolsMissingRegistryIsAmbientOnly(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	warn, msgs := warnCollector()
	got := discoverPools(filepath.Join(t.TempDir(), "absent"), warn)
	if len(got) != 1 || got[0].Dir != "" {
		t.Fatalf("got %+v", got)
	}
	if len(*msgs) != 0 {
		t.Errorf("a missing registry is normal and must not warn: %v", *msgs)
	}
}

func TestRegistryDirResolution(t *testing.T) {
	t.Setenv("CCPOOL_REGISTRY_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got := registryDir(""); got != "/xdg/state/ccpool/pools.d" {
		t.Errorf("xdg: %q", got)
	}
	t.Setenv("CCPOOL_REGISTRY_DIR", "/env/reg")
	if got := registryDir(""); got != "/env/reg" {
		t.Errorf("env: %q", got)
	}
	if got := registryDir("/flag/reg"); got != "/flag/reg" {
		t.Errorf("flag override: %q", got)
	}
}

func TestPoolFingerprintScope(t *testing.T) {
	if (poolRef{Label: ambientPoolLabel}).fingerprintScope() != "" {
		t.Error("ambient pool must keep the legacy pool-less fingerprint")
	}
	if (poolRef{Label: "p", Dir: "/x/p"}).fingerprintScope() != "p" {
		t.Error("named pool must scope by label")
	}
}
