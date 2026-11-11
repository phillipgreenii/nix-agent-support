package levels

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

const dwell = 60 * time.Second

// diskTable is the 2.4 disk row: higher is better, bands include their lower
// bound and exclude their upper.
func diskTable() Table {
	return Table{
		Resource: "disk", Unit: "GiB", Scale: FourStep, Dir: HigherBetter, Hyst: HysteresisHigher,
		Bands: []Band{
			{Level: "EXHAUSTED", Max: Num(12)},
			{Level: "TIGHT", Min: Num(12), Max: Num(25)},
			{Level: "OK", Min: Num(25), Max: Num(40)},
			{Level: "PLENTY", Min: Num(40)},
		},
	}
}

// cpuTable is the 2.4 cpu row: lower is better.
func cpuTable() Table {
	return Table{
		Resource: "cpu", Unit: "load_per_core", Scale: FourStep, Dir: LowerBetter, Hyst: HysteresisLower,
		Bands: []Band{
			{Level: "EXHAUSTED", Min: Num(14)},
			{Level: "TIGHT", Min: Num(8), Max: Num(14)},
			{Level: "OK", Min: Num(4), Max: Num(8)},
			{Level: "PLENTY", Max: Num(4)},
		},
	}
}

// paceTable is the 2.5 pace row with the additive recovery band.
func paceTable() Table {
	return Table{
		Resource: "claude_pace", Unit: "ratio", Scale: FourStep, Dir: LowerBetter, Hyst: HysteresisPace,
		Bands: []Band{
			{Level: "EXHAUSTED", Min: Num(1.25)},
			{Level: "TIGHT", Min: Num(1.0), Max: Num(1.25)},
			{Level: "OK", Min: Num(0.8), Max: Num(1.0)},
			{Level: "PLENTY", Max: Num(0.8)},
		},
	}
}

func TestScale(t *testing.T) {
	if FourStep.Worst() != "EXHAUSTED" || FourStep.Best() != "PLENTY" {
		t.Fatalf("worst/best = %s/%s", FourStep.Worst(), FourStep.Best())
	}
	if FourStep.Index("OK") != 2 || FourStep.Index("nope") != -1 {
		t.Fatalf("index wrong")
	}
	var empty Scale
	if empty.Worst() != "" || empty.Best() != "" {
		t.Fatalf("empty scale must have no worst or best")
	}
}

// TestTable_boundaryTable is the boundary table at and above every edge: a
// value exactly on a boundary belongs to the band that states it as its lower
// bound.
func TestTable_boundaryTable(t *testing.T) {
	cases := []struct {
		tbl  Table
		v    float64
		want string
	}{
		{diskTable(), 11.99, "EXHAUSTED"},
		{diskTable(), 12, "TIGHT"},
		{diskTable(), 24.99, "TIGHT"},
		{diskTable(), 25, "OK"},
		{diskTable(), 39.99, "OK"},
		{diskTable(), 40, "PLENTY"},
		{diskTable(), 500, "PLENTY"},
		{cpuTable(), 3.99, "PLENTY"},
		{cpuTable(), 4, "OK"},
		{cpuTable(), 7.99, "OK"},
		{cpuTable(), 8, "TIGHT"},
		{cpuTable(), 13.99, "TIGHT"},
		{cpuTable(), 14, "EXHAUSTED"},
		{cpuTable(), 26, "EXHAUSTED"},
		{paceTable(), 0.79, "PLENTY"},
		{paceTable(), 0.8, "OK"},
		{paceTable(), 1.0, "TIGHT"},
		{paceTable(), 1.25, "EXHAUSTED"},
	}
	for _, c := range cases {
		got, err := c.tbl.Classify(c.v)
		if err != nil || got != c.want {
			t.Errorf("%s %v = %q, %v; want %q", c.tbl.Resource, c.v, got, err, c.want)
		}
	}
}

func TestTable_validateAndClassifyErrors(t *testing.T) {
	if err := diskTable().Validate(); err != nil {
		t.Fatal(err)
	}
	short := diskTable()
	short.Bands = short.Bands[:3]
	if short.Validate() == nil {
		t.Error("a table with a missing band must not validate")
	}
	swapped := diskTable()
	swapped.Bands[0].Level = "TIGHT"
	if swapped.Validate() == nil {
		t.Error("a band out of scale order must not validate")
	}
	gap := Table{Resource: "gap", Scale: Binary, Bands: []Band{{Level: "UNAVAILABLE", Max: Num(1)}, {Level: "AVAILABLE", Min: Num(2)}}}
	if _, err := gap.Classify(1.5); err == nil {
		t.Error("a value in no band must be an error")
	}
	if _, err := gap.Observe(1.5); err == nil {
		t.Error("Observe must surface the classify error")
	}
}

// TestTable_canRiseMargin: a rise needs the reading to clear the boundary with
// margin (disk * 1.25, cpu * 0.8, pace - 0.05).
func TestTable_canRiseMargin(t *testing.T) {
	cases := []struct {
		tbl  Table
		cur  string
		v    float64
		want bool
	}{
		{diskTable(), "OK", 49.99, false},
		{diskTable(), "OK", 50, true}, // 40 * 1.25
		{diskTable(), "TIGHT", 31.24, false},
		{diskTable(), "TIGHT", 31.25, true}, // 25 * 1.25
		{diskTable(), "EXHAUSTED", 14.99, false},
		{diskTable(), "EXHAUSTED", 15, true}, // 12 * 1.25
		{cpuTable(), "OK", 3.21, false},
		{cpuTable(), "OK", 3.2, true}, // 4 * 0.8
		{cpuTable(), "TIGHT", 6.41, false},
		{cpuTable(), "TIGHT", 6.4, true}, // 8 * 0.8
		{paceTable(), "OK", 0.76, false},
		{paceTable(), "OK", 0.75, true},
		{paceTable(), "TIGHT", 0.96, false},
		{paceTable(), "TIGHT", 0.95, true},
		{diskTable(), "PLENTY", 1000, false}, // best level cannot rise
		{diskTable(), "BOGUS", 1000, false},  // not in the scale
	}
	for _, c := range cases {
		if got := c.tbl.CanRise(c.cur, c.v); got != c.want {
			t.Errorf("%s CanRise(%s, %v) = %v, want %v", c.tbl.Resource, c.cur, c.v, got, c.want)
		}
	}
	open := Table{Scale: Binary, Dir: HigherBetter, Bands: []Band{{Level: "UNAVAILABLE"}, {Level: "AVAILABLE"}}}
	if open.CanRise("UNAVAILABLE", 1) {
		t.Error("a rise across an open boundary has no numeric edge and must be refused")
	}
	lower := Table{Scale: Binary, Dir: LowerBetter, Bands: []Band{{Level: "UNAVAILABLE"}, {Level: "AVAILABLE"}}}
	if lower.CanRise("UNAVAILABLE", 1) {
		t.Error("lower-is-better open boundary must be refused")
	}
}

func TestHysteresis_zeroFactorIsOne(t *testing.T) {
	if got := (Hysteresis{}).Required(5); got != 5 {
		t.Errorf("Required = %v, want 5", got)
	}
}

// TestPressure_loadRecoveryHysteresis is the moved load-recovery hysteresis
// test of the removed disk-watchdog: 3.5 per core is below the 4.0 boundary but
// above the 3.2 recovery line, so the level holds; 3.0 recovers; in the band
// with nothing held, nothing is raised.
func TestPressure_loadRecoveryHysteresis(t *testing.T) {
	tbl := cpuTable()
	held := State{Level: "OK", Since: t0}
	at := t0.Add(2 * dwell)

	obs, err := tbl.Observe(3.5)
	if err != nil {
		t.Fatal(err)
	}
	if got, changed := Step(held, obs, at, dwell, false); changed || got.Level != "OK" {
		t.Errorf("3.5 per core: level %s changed=%v, want OK held", got.Level, changed)
	}
	obs, _ = tbl.Observe(3.0)
	if got, changed := Step(held, obs, at, dwell, false); !changed || got.Level != "PLENTY" {
		t.Errorf("3.0 per core: level %s changed=%v, want PLENTY", got.Level, changed)
	}
	// A level already PLENTY with 3.5 (in the band) is not disturbed: the band
	// of 3.5 is PLENTY itself.
	obs, _ = tbl.Observe(3.5)
	if obs.Level != "PLENTY" {
		t.Errorf("3.5 classifies %s, want PLENTY", obs.Level)
	}
}

func TestInfo(t *testing.T) {
	in := diskTable().Info()
	if in.Resource != "disk" || in.Unit != "GiB" || len(in.Bands) != 4 {
		t.Fatalf("info = %+v", in)
	}
	if in.Bands[0].Min != nil || *in.Bands[0].Max != 12 || *in.Bands[3].Min != 40 || in.Bands[3].Max != nil {
		t.Errorf("open and closed bounds wrong: %+v", in.Bands)
	}
	nb := NoBands("git_auth_x", "")
	if nb.Bands == nil || len(nb.Bands) != 0 {
		t.Errorf("NoBands must be an empty, non-nil list: %+v", nb)
	}
}

func obsAt(level string) Observation {
	return Observation{Resource: "r", Scale: FourStep, Level: level}
}

func TestStep_fallIsImmediate(t *testing.T) {
	st := State{Level: "PLENTY", Since: t0}
	got, changed := Step(st, obsAt("EXHAUSTED"), t0.Add(time.Second), dwell, false)
	if !changed || got.Level != "EXHAUSTED" || !got.Since.Equal(t0.Add(time.Second)) {
		t.Fatalf("got %+v changed=%v; a fall skips levels and is immediate", got, changed)
	}
}

func TestStep_riseIsOneLevelPerDwell(t *testing.T) {
	st := State{Level: "EXHAUSTED", Since: t0}
	o := obsAt("PLENTY")
	if _, changed := Step(st, o, t0.Add(dwell-time.Second), dwell, false); changed {
		t.Fatal("a rise inside the dwell must hold")
	}
	st, changed := Step(st, o, t0.Add(dwell), dwell, false)
	if !changed || st.Level != "TIGHT" {
		t.Fatalf("after one dwell: %+v changed=%v, want TIGHT", st, changed)
	}
	if _, changed := Step(st, o, t0.Add(dwell+dwell-time.Second), dwell, false); changed {
		t.Fatal("the second step needs its own dwell")
	}
	st, _ = Step(st, o, t0.Add(2*dwell), dwell, false)
	st, _ = Step(st, o, t0.Add(3*dwell), dwell, false)
	if st.Level != "PLENTY" {
		t.Fatalf("after three dwells: %s, want PLENTY", st.Level)
	}
}

func TestStep_sameLevelAndUnknownObservedHold(t *testing.T) {
	st := State{Level: "OK", Since: t0}
	if got, changed := Step(st, obsAt("OK"), t0.Add(time.Hour), dwell, false); changed || got.Level != st.Level || !got.Since.Equal(st.Since) {
		t.Errorf("same level changed: %+v", got)
	}
	if got, changed := Step(st, obsAt("BOGUS"), t0.Add(time.Hour), dwell, false); changed || got.Level != st.Level || !got.Since.Equal(st.Since) {
		t.Errorf("an observed level outside the scale must change nothing: %+v", got)
	}
}

func TestStep_bypassJumpsToObservedLevel(t *testing.T) {
	st := State{Level: "EXHAUSTED", Since: t0}
	now := t0.Add(time.Second)
	got, changed := Step(st, obsAt("PLENTY"), now, dwell, true)
	if !changed || got.Level != "PLENTY" {
		t.Fatalf("bypass parameter: %+v", got)
	}
	o := obsAt("PLENTY")
	o.BypassDwell = true
	o.CanRise = func(string) bool { return false }
	got, changed = Step(st, o, now, dwell, false)
	if !changed || got.Level != "PLENTY" {
		t.Fatalf("BypassDwell field must skip dwell and margin: %+v", got)
	}
}

func TestStep_marginGateBlocksRise(t *testing.T) {
	st := State{Level: "OK", Since: t0}
	o := obsAt("PLENTY")
	o.CanRise = func(cur string) bool { return cur != "OK" }
	if _, changed := Step(st, o, t0.Add(time.Hour), dwell, false); changed {
		t.Fatal("a failing margin gate must block the rise")
	}
	o.CanRise = func(string) bool { return true }
	if got, changed := Step(st, o, t0.Add(time.Hour), dwell, false); !changed || got.Level != "PLENTY" {
		t.Fatalf("a passing gate allows the rise: %+v", got)
	}
}

func TestStep_unknownStateLevelRestartsAtWorstKeepingExtra(t *testing.T) {
	st := State{Level: "WEIRD", Since: t0, Extra: map[string]string{"n": "2"}}
	now := t0.Add(time.Second)
	got, changed := Step(st, obsAt("PLENTY"), now, dwell, false)
	if changed || got.Level != "EXHAUSTED" || got.Extra["n"] != "2" || !got.Since.Equal(now) {
		t.Fatalf("got %+v changed=%v; an unusable level restarts at the worst, held for one dwell", got, changed)
	}
}

// TestStep_flappingOneRiseTransitionPerDwell drives a reading that flaps
// between a worse and a better band every 10 s for 10 minutes: the machine
// announces at most one rise per dwell and a fall never waits.
func TestStep_flappingOneTransitionPerDwell(t *testing.T) {
	tbl := cpuTable()
	st := State{Level: "OK", Since: t0}
	rises := 0
	now := t0
	for i := 0; i < 60; i++ {
		now = now.Add(10 * time.Second)
		v := 2.0 // PLENTY with margin
		if i%2 == 0 {
			v = 5.0 // OK
		}
		obs, _ := tbl.Observe(v)
		prev := st
		var changed bool
		st, changed = Step(st, obs, now, dwell, false)
		if changed && tbl.Scale.Index(st.Level) > tbl.Scale.Index(prev.Level) {
			rises++
			if now.Sub(prev.Since) < dwell {
				t.Fatalf("rise at %v after only %v in the previous level", now, now.Sub(prev.Since))
			}
		}
	}
	// 600 s of flapping with a 60 s dwell: a rise is possible at most every dwell,
	// and each rise is followed by an immediate fall on the next odd step.
	if rises > 10 {
		t.Fatalf("%d rises in 10 minutes, want at most one per dwell (10)", rises)
	}
}

func TestStore_lostFileStartsAtWorstAndRisesOneLevelPerDwell(t *testing.T) {
	dir := t.TempDir()
	path := StatePath(dir, "host-sensor")
	if !strings.HasSuffix(path, "host-sensor.state.json") {
		t.Fatalf("path = %s", path)
	}
	s := OpenStore(path)
	if !s.Lost || s.LoadErr != nil {
		t.Fatalf("absent file: Lost=%v LoadErr=%v, want Lost without an error", s.Lost, s.LoadErr)
	}
	o := obsAt("PLENTY")
	level, changed := s.Apply(o, t0, dwell, false)
	if level != "EXHAUSTED" || changed {
		t.Fatalf("first run: %s changed=%v; a lost file must start at the worst level", level, changed)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	s2 := OpenStore(path)
	if s2.Lost {
		t.Fatal("a saved file must load")
	}
	level, _ = s2.Apply(o, t0.Add(dwell/2), dwell, false)
	if level != "EXHAUSTED" {
		t.Fatalf("inside the dwell: %s, want EXHAUSTED", level)
	}
	level, changed = s2.Apply(o, t0.Add(dwell), dwell, false)
	if level != "TIGHT" || !changed {
		t.Fatalf("after one dwell: %s changed=%v, want TIGHT", level, changed)
	}
	_ = s2.Save()

	// The file is destroyed: the herd is NOT released; the sensor restarts at the
	// worst level even though the reading is the best.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s3 := OpenStore(path)
	level, _ = s3.Apply(o, t0.Add(10*dwell), dwell, false)
	if level != "EXHAUSTED" {
		t.Fatalf("after a lost file: %s, want EXHAUSTED", level)
	}
}

func TestStore_corruptFileIsLostWithCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := OpenStore(path)
	if !s.Lost || s.LoadErr == nil {
		t.Fatalf("Lost=%v LoadErr=%v, want lost with a cause", s.Lost, s.LoadErr)
	}
	dirAsFile := OpenStore(t.TempDir())
	if !dirAsFile.Lost || dirAsFile.LoadErr == nil {
		t.Fatalf("an unreadable path must be lost with a cause")
	}
	empty := filepath.Join(t.TempDir(), "e.state.json")
	if err := os.WriteFile(empty, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := OpenStore(empty); s.Lost || len(s.data) != 0 {
		t.Fatalf("a file with no resources is a valid empty store")
	}
}

func TestStore_extraSlotRoundTripsAndKeepsLevel(t *testing.T) {
	path := StatePath(t.TempDir(), "vpn-sensor")
	s := OpenStore(path)
	s.SetExtra("network", map[string]string{"failures": "2"})
	if got := s.Extra("network")["failures"]; got != "2" {
		t.Fatalf("extra = %v", s.Extra("network"))
	}
	s.Extra("network")["failures"] = "99" // a copy: must not write through
	if got := s.Extra("network")["failures"]; got != "2" {
		t.Fatalf("Extra must return a copy, got %s", got)
	}
	// A level applied after the extra slot was set (no level yet) restarts at
	// the worst and keeps the counters.
	if level, _ := s.Apply(Observation{Resource: "network", Scale: Binary, Level: "AVAILABLE"}, t0, dwell, false); level != "UNAVAILABLE" {
		t.Fatalf("level = %s", level)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2 := OpenStore(path)
	if s2.Extra("network")["failures"] != "2" {
		t.Fatalf("extra did not survive: %v", s2.Extra("network"))
	}
	if s2.Extra("absent") == nil || len(s2.Extra("absent")) != 0 {
		t.Fatal("an unknown resource has an empty, non-nil slot")
	}
}

func TestStore_memoryStoreNeverWrites(t *testing.T) {
	s := NewMemoryStore()
	s.Put("r", State{Level: "OK", Since: t0})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("r", FourStep, t0); got.Level != "OK" {
		t.Fatalf("got %+v", got)
	}
	if got := s.Get("other", FourStep, t0); got.Level != "EXHAUSTED" {
		t.Fatalf("missing resource must be Initial, got %+v", got)
	}
}

func TestStore_saveErrors(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := OpenStore(filepath.Join(blocker, "sub", "s.state.json")) // parent is a file
	s.Put("r", State{Level: "OK", Since: t0})
	if err := s.Save(); err == nil {
		t.Fatal("Save under a file must fail")
	}
	// A rename target that is a directory fails after the temp file was written.
	dir := t.TempDir()
	target := filepath.Join(dir, "s.state.json")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	s2 := &Store{path: target, data: map[string]State{}}
	if err := s2.Save(); err == nil {
		t.Fatal("rename onto a directory must fail")
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".state-*.tmp"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
