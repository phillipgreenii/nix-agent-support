package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func pressureConfig() config {
	c := testConfig()
	c.paths = []string{"/"}
	c.maxLoadPerCore = 4
	c.cores = 10
	c.swapMax = 10 * gib
	c.swapDiskFree = 20 * gib
	return c
}

type probes struct {
	load    float64
	loadErr error
	swap    uint64
	swapErr error
}

func runPressure(t *testing.T, cfg config, f *fakeRouter, freeB uint64, p probes) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := check(context.Background(), cfg, deps{
		freeBytes: free(map[string]uint64{"/": freeB}),
		loadAvg1:  func() (float64, error) { return p.load, p.loadErr },
		swapUsed:  func() (uint64, error) { return p.swap, p.swapErr },
		run:       f.run, stdout: &out, stderr: &errb,
	})
	return code, out.String(), errb.String()
}

func setDesc(f *fakeRouter) string {
	for _, c := range f.calls {
		if len(c) > 2 && c[2] == "set" {
			return strings.Join(c, " ")
		}
	}
	return ""
}

func TestPressure_highLoadPerCoreSetsGate(t *testing.T) {
	f := &fakeRouter{listing: `[]`}
	// 100 GiB free, so disk is not the cause; 45 load / 10 cores = 4.5 > 4.
	code, _, _ := runPressure(t, pressureConfig(), f, 100*gib, probes{load: 45})
	if code != exitOK || strings.Join(f.verbs(), ",") != "list,set" {
		t.Fatalf("code=%d verbs=%v, want list,set", code, f.verbs())
	}
	if d := setDesc(f); !strings.Contains(d, "high load") || !strings.Contains(d, "4.5 per core") {
		t.Errorf("set call %q lacks the load reason", d)
	}
}

func TestPressure_loadAtLimitDoesNotTrip(t *testing.T) {
	f := &fakeRouter{listing: `[]`}
	runPressure(t, pressureConfig(), f, 100*gib, probes{load: 40}) // exactly 4.0 per core
	if got := strings.Join(f.verbs(), ","); got != "list" {
		t.Fatalf("verbs = %s, want a read-only pass at the limit", got)
	}
}

func TestPressure_swapWithLowDiskSetsGate(t *testing.T) {
	f := &fakeRouter{listing: `[]`}
	// 30 GiB free is above the 20 GiB disk floor, so only the swap rule can
	// trip, and only because its disk ceiling is raised to 40 GiB here.
	cfg := pressureConfig()
	cfg.swapDiskFree = 40 * gib
	runPressure(t, cfg, f, 30*gib, probes{swap: 12 * gib})
	if d := setDesc(f); !strings.Contains(d, "swap pressure") || !strings.Contains(d, "12.0 GiB swap") {
		t.Fatalf("set call %q lacks the swap reason; verbs=%v", d, f.verbs())
	}
}

func TestPressure_swapAloneOrDiskAloneDoesNotTripSwapRule(t *testing.T) {
	cfg := pressureConfig() // swapDiskFree 20 GiB, disk floor 20 GiB
	t.Run("swap high, disk plentiful", func(t *testing.T) {
		f := &fakeRouter{listing: `[]`}
		runPressure(t, cfg, f, 100*gib, probes{swap: 30 * gib})
		if got := strings.Join(f.verbs(), ","); got != "list" {
			t.Fatalf("verbs = %s, want read-only", got)
		}
	})
	t.Run("disk tight (above floor), swap small", func(t *testing.T) {
		c := cfg
		c.swapDiskFree = 40 * gib
		f := &fakeRouter{listing: `[]`}
		runPressure(t, c, f, 30*gib, probes{swap: 1 * gib})
		if got := strings.Join(f.verbs(), ","); got != "list" {
			t.Fatalf("verbs = %s, want read-only", got)
		}
	})
}

func TestPressure_hardStopOnLowDiskIndependentOfLoadAndSwap(t *testing.T) {
	f := &fakeRouter{listing: `[]`}
	runPressure(t, pressureConfig(), f, 12*gib, probes{load: 0.1, swap: 0})
	if d := setDesc(f); !strings.Contains(d, "low disk space: 12.0 GiB free") {
		t.Fatalf("set call %q, want the disk reason", d)
	}
}

func TestPressure_reasonsAreCombined(t *testing.T) {
	f := &fakeRouter{listing: `[]`}
	runPressure(t, pressureConfig(), f, 12*gib, probes{load: 90, swap: 20 * gib})
	d := setDesc(f)
	for _, want := range []string{"low disk space", "high load", "swap pressure"} {
		if !strings.Contains(d, want) {
			t.Errorf("set call %q missing %q", d, want)
		}
	}
}

func TestPressure_loadRecoveryHysteresis(t *testing.T) {
	own := `[{"type":"LOW_DISK_USAGE","owner":"wd"}]`
	cfg := pressureConfig()
	// 3.5 per core: below the 4.0 trip but above the 3.2 recovery line -> hold.
	f := &fakeRouter{listing: own}
	runPressure(t, cfg, f, 100*gib, probes{load: 35})
	if got := strings.Join(f.verbs(), ","); got != "list,set" {
		t.Errorf("in band: verbs = %s, want list,set (held)", got)
	}
	// 3.0 per core: under the recovery line -> clear.
	f = &fakeRouter{listing: own}
	runPressure(t, cfg, f, 100*gib, probes{load: 30})
	if got := strings.Join(f.verbs(), ","); got != "list,clear" {
		t.Errorf("recovered: verbs = %s, want list,clear", got)
	}
	// In the band with no gate held: nothing is created.
	f = &fakeRouter{listing: `[]`}
	runPressure(t, cfg, f, 100*gib, probes{load: 35})
	if got := strings.Join(f.verbs(), ","); got != "list" {
		t.Errorf("in band, no gate: verbs = %s, want list only", got)
	}
}

func TestPressure_loadGateNotClearedWhileDiskStillRecovering(t *testing.T) {
	f := &fakeRouter{listing: `[{"type":"LOW_DISK_USAGE","owner":"wd"}]`}
	runPressure(t, pressureConfig(), f, 22*gib, probes{load: 1}) // disk in 20..25 band
	if got := strings.Join(f.verbs(), ","); got != "list,set" {
		t.Fatalf("verbs = %s, want held", got)
	}
}

func TestPressure_failedProbesAreSkippedNotFatal(t *testing.T) {
	f := &fakeRouter{listing: `[]`}
	code, _, errOut := runPressure(t, pressureConfig(), f, 100*gib,
		probes{loadErr: errors.New("sysctl boom"), swapErr: errors.New("swap boom")})
	if code != exitOK || strings.Join(f.verbs(), ",") != "list" {
		t.Fatalf("code=%d verbs=%v, want a healthy read-only pass", code, f.verbs())
	}
	if !strings.Contains(errOut, "load probe skipped") || !strings.Contains(errOut, "swap probe skipped") {
		t.Errorf("stderr = %q, want both skips noted", errOut)
	}
}

func TestPressure_disabledRulesAndNilProbes(t *testing.T) {
	cfg := pressureConfig()
	cfg.maxLoadPerCore, cfg.swapMax = 0, 0
	f := &fakeRouter{listing: `[]`}
	runPressure(t, cfg, f, 12*gib+100*gib, probes{load: 999, swap: 99 * gib})
	if got := strings.Join(f.verbs(), ","); got != "list" {
		t.Fatalf("disabled rules tripped: verbs = %s", got)
	}
	// nil probes (the pre-existing disk-only tests) never trip the new rules.
	f = &fakeRouter{listing: `[]`}
	runCheck(t, pressureConfig(), f, map[string]uint64{"/": 100 * gib})
	if got := strings.Join(f.verbs(), ","); got != "list" {
		t.Fatalf("nil probes tripped: verbs = %s", got)
	}
}

func TestPressure_anotherOwnersGateIsLeftAlone(t *testing.T) {
	f := &fakeRouter{listing: `[{"type":"LOW_DISK_USAGE","owner":"operator"}]`}
	_, out, _ := runPressure(t, pressureConfig(), f, 100*gib, probes{load: 90})
	if strings.Join(f.verbs(), ",") != "list" || !strings.Contains(out, "another owner") {
		t.Fatalf("verbs=%v out=%q", f.verbs(), out)
	}
}

func TestParseLoadAvg1AndSwapUsed(t *testing.T) {
	if v, err := parseLoadAvg1("{ 2.46 2.36 2.31 }\n"); err != nil || v != 2.46 {
		t.Errorf("parseLoadAvg1 = %v, %v", v, err)
	}
	if _, err := parseLoadAvg1("garbage"); err == nil {
		t.Error("parseLoadAvg1 accepted garbage")
	}
	if v, err := parseSwapUsed("total = 2048.00M  used = 1024.50M  free = 1023.50M  (encrypted)"); err != nil || v != uint64(1024.5*(1<<20)) {
		t.Errorf("parseSwapUsed M = %d, %v", v, err)
	}
	if v, err := parseSwapUsed("total = 12.00G  used = 10.50G  free = 1.50G"); err != nil || v != uint64(10.5*(1<<30)) {
		t.Errorf("parseSwapUsed G = %d, %v", v, err)
	}
	if _, err := parseSwapUsed("nope"); err == nil {
		t.Error("parseSwapUsed accepted garbage")
	}
}

func TestBuildConfig_pressureDefaults(t *testing.T) {
	cfg, err := buildConfig(nil, "30GiB", "", defaultMaxLoadPerCore, defaultSwapMax, defaultSwapDiskFree, defaultGate, defaultOwner, defaultTTL, "pg-router", time.Second)
	if err != nil || cfg.maxLoadPerCore != 4 || cfg.swapMax != 10*gib || cfg.swapDiskFree != 20*gib || cfg.cores < 1 {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}
