package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 0.006 }

// resetsFor returns the resets_at that gives elapsed fraction e for window w.
func resetsFor(w Window, e float64) time.Time {
	return now.Add(time.Duration((1 - e) * float64(w.W)))
}

func TestPaceTable(t *testing.T) {
	rows := []struct {
		name  string
		w     Window
		u, e  float64
		pace  float64
		level string
	}{
		{"start", DefaultBlock, 0, 0, 0.31, "PLENTY"},
		{"spike low", DefaultBlock, 0.08, 0.02, 0.51, "PLENTY"},
		{"spike high", DefaultBlock, 0.2, 0.05, 0.81, "OK"},
		{"midway", DefaultBlock, 0.5, 0.5, 0.83, "OK"},
		{"late", DefaultBlock, 0.95, 0.9, 0.93, "OK"},
		{"week", DefaultWeek, 0.10, 0.05, 1.0, "TIGHT"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			p := Pace(r.w, DefaultP0, r.u, r.e)
			if !near(p, r.pace) {
				t.Fatalf("pace %v, want %v", p, r.pace)
			}
			o, err := New().table("x").Observe(p)
			if err != nil || o.Level != r.level {
				t.Fatalf("level %q (%v), want %q", o.Level, err, r.level)
			}
		})
	}
}

func TestPaceProperties(t *testing.T) {
	for _, w := range []Window{DefaultBlock, DefaultWeek} {
		prevU := -1.0
		for u := 0.0; u <= 1.0; u += 0.05 {
			p := Pace(w, 0.5, u, 0.4)
			if p <= prevU {
				t.Errorf("not monotone in u at %v", u)
			}
			prevU = p
		}
		prevE := math.Inf(1)
		for e := 0.0; e <= 1.0; e += 0.05 {
			p := Pace(w, 0.5, 0.3, e)
			if p > prevE {
				t.Errorf("not antitone in e at %v", e)
			}
			prevE = p
		}
		if Pace(w, 0.5, 0.3, 0) != Pace(w, 0.5, 0.3, w.C) || Pace(w, 0.5, 0.3, w.C/2) != Pace(w, 0.5, 0.3, w.C) {
			t.Error("pace is not flat below c")
		}
		eps := 1e-9
		if math.Abs(Pace(w, 0.5, 0.3, w.C-eps)-Pace(w, 0.5, 0.3, w.C+eps)) > 1e-6 {
			t.Error("pace is discontinuous at e = c")
		}
	}
}

func TestElapsedClamp(t *testing.T) {
	if e := Elapsed(DefaultBlock, now.Add(10*time.Hour), now); e != 0 {
		t.Errorf("far reset: %v", e)
	}
	if e := Elapsed(DefaultBlock, now.Add(-time.Hour), now); e != 1 {
		t.Errorf("past reset: %v", e)
	}
	if e := Elapsed(DefaultBlock, now.Add(150*time.Minute), now); !near(e, 0.5) {
		t.Errorf("mid: %v", e)
	}
}

// doc builds a rate_limits document.
func doc(five, seven *WindowJSON, captured string) []byte {
	rl := map[string]any{}
	if five != nil {
		rl["five_hour"] = five
	}
	if seven != nil {
		rl["seven_day"] = seven
	}
	if captured != "" {
		rl["captured_at"] = captured
	}
	b, _ := json.Marshal(map[string]any{"rate_limits": rl})
	return b
}

func win(pct float64, resets time.Time) *WindowJSON {
	s := resets.Format(time.RFC3339)
	return &WindowJSON{UsedPct: &pct, ResetsAt: &s}
}

func ts(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

func measure(t *testing.T, s *Sensor, out cmdrun.Result, resource string) ([]levels.Observation, string, error) {
	t.Helper()
	f := cmdruntest.New(t)
	f.On(cmdruntest.Match("pa-monitor", "status", "--rate-limits")).Return(out)
	s.Run = f.Runner()
	var eb bytes.Buffer
	o, err := s.Measure(context.Background(), sensorcli.Env{Now: now, Resource: resource, Stderr: &eb})
	return o, eb.String(), err
}

func one(t *testing.T, obs []levels.Observation, res string) levels.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Resource == res {
			return o
		}
	}
	t.Fatalf("no observation for %s in %+v", res, obs)
	return levels.Observation{}
}

func TestMeasureBothWindows(t *testing.T) {
	b := doc(win(50, resetsFor(DefaultBlock, 0.5)), win(10, resetsFor(DefaultWeek, 0.05)), ts(-time.Minute))
	obs, _, err := measure(t, New(), cmdruntest.Out(string(b)), "")
	if err != nil || len(obs) != 2 {
		t.Fatalf("%v %+v", err, obs)
	}
	b5, wk := one(t, obs, Resource5h), one(t, obs, ResourceWeek)
	if b5.Level != "OK" || !near(b5.Reading.Value, 0.83) || b5.Reading.Unit != "ratio" {
		t.Errorf("block %+v %+v", b5, b5.Reading)
	}
	if wk.Level != "TIGHT" || !near(wk.Reading.Value, 1.0) {
		t.Errorf("week %+v %+v", wk, wk.Reading)
	}
	// --resource limit.
	obs, _, _ = measure(t, New(), cmdruntest.Out(string(b)), ResourceWeek)
	if len(obs) != 1 || obs[0].Resource != ResourceWeek {
		t.Errorf("resource filter: %+v", obs)
	}
}

func TestFloor(t *testing.T) {
	// u=0.95 at e=0.9: pace 0.93 (OK) raised to TIGHT by the floor.
	b := doc(win(95, resetsFor(DefaultBlock, 0.9)), nil, ts(-time.Minute))
	obs, _, _ := measure(t, New(), cmdruntest.Out(string(b)), "")
	if o := one(t, obs, Resource5h); o.Level != "TIGHT" {
		t.Errorf("floor: %q", o.Level)
	}
	// u=0.94: no floor.
	b = doc(win(94, resetsFor(DefaultBlock, 0.9)), nil, ts(-time.Minute))
	obs, _, _ = measure(t, New(), cmdruntest.Out(string(b)), "")
	if o := one(t, obs, Resource5h); o.Level != "OK" {
		t.Errorf("below floor: %q", o.Level)
	}
	// Full usage at e=1: pace at most 0.9-ish, the floor gives TIGHT, never EXHAUSTED.
	b = doc(win(100, resetsFor(DefaultBlock, 0.99)), nil, ts(-time.Minute))
	obs, _, _ = measure(t, New(), cmdruntest.Out(string(b)), "")
	if o := one(t, obs, Resource5h); o.Level != "TIGHT" {
		t.Errorf("full: %q", o.Level)
	}
	// Pace itself can exceed the floor: EXHAUSTED stays EXHAUSTED.
	b = doc(win(95, resetsFor(DefaultBlock, 0.2)), nil, ts(-time.Minute))
	obs, _, _ = measure(t, New(), cmdruntest.Out(string(b)), "")
	if o := one(t, obs, Resource5h); o.Level != "EXHAUSTED" {
		t.Errorf("fast: %q", o.Level)
	}
}

func TestMissingResetsAt(t *testing.T) {
	pct := 96.0
	b := doc(&WindowJSON{UsedPct: &pct}, nil, ts(-time.Minute))
	obs, _, err := measure(t, New(), cmdruntest.Out(string(b)), "")
	if err != nil || len(obs) != 1 || obs[0].Level != "TIGHT" || obs[0].Reading != nil {
		t.Errorf("floor only: %v %+v", err, obs)
	}
	pct = 50
	obs, se, err := measure(t, New(), cmdruntest.Out(string(b[:0])+string(doc(&WindowJSON{UsedPct: &pct}, nil, ts(-time.Minute)))), "")
	if err != nil || len(obs) != 0 || !strings.Contains(se, "no reading") {
		t.Errorf("below floor, no resets: %v %+v %q", err, obs, se)
	}
	// An unparseable resets_at counts as absent.
	bad := "garbage"
	pct = 97
	obs, _, _ = measure(t, New(), cmdruntest.Out(string(doc(&WindowJSON{UsedPct: &pct, ResetsAt: &bad}, nil, ts(-time.Minute)))), "")
	if len(obs) != 1 || obs[0].Level != "TIGHT" {
		t.Errorf("bad resets: %+v", obs)
	}
}

func TestUnknownCases(t *testing.T) {
	r := resetsFor(DefaultBlock, 0.5)
	cases := map[string][]byte{
		"window key absent":   doc(nil, win(10, resetsFor(DefaultWeek, 0.5)), ts(-time.Minute)),
		"stale captured_at":   doc(win(50, r), nil, ts(-16*time.Minute)),
		"future captured_at":  doc(win(50, r), nil, ts(time.Minute)),
		"too-early captured":  doc(win(50, r), nil, ts(-6*time.Hour)),
		"captured_at absent":  doc(win(50, r), nil, ""),
		"captured unparsable": doc(win(50, r), nil, "yesterday"),
		"used_pct absent":     doc(&WindowJSON{}, nil, ts(-time.Minute)),
		"empty rate_limits":   []byte(`{"rate_limits":{}}`),
		"no rate_limits key":  []byte(`{}`),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			obs, _, err := measure(t, New(), cmdruntest.Out(string(b)), Resource5h)
			if err != nil || len(obs) != 0 {
				t.Errorf("want no line and no error, got %v %+v", err, obs)
			}
		})
	}
	// max_age is configurable.
	s := New()
	s.MaxAge = time.Hour
	obs, _, _ := measure(t, s, cmdruntest.Out(string(doc(win(50, r), nil, ts(-30*time.Minute)))), "")
	if len(obs) != 1 {
		t.Errorf("max_age: %+v", obs)
	}
}

func TestResetBypassesDwell(t *testing.T) {
	b := doc(win(99, now), nil, ts(-time.Minute))
	obs, _, _ := measure(t, New(), cmdruntest.Out(string(b)), "")
	o := one(t, obs, Resource5h)
	if o.Level != "PLENTY" || !o.BypassDwell {
		t.Errorf("%+v", o)
	}
	// Through the state machine: from EXHAUSTED straight to PLENTY.
	st := levels.State{Level: "EXHAUSTED", Since: now}
	next, _ := levels.Step(st, o, now, time.Hour, false)
	if next.Level != "PLENTY" {
		t.Errorf("step: %+v", next)
	}
}

func TestRecoveryBand(t *testing.T) {
	s := New()
	tbl := s.table("x")
	// Pace 0.97 is OK; rising to PLENTY needs <= 0.75.
	if tbl.CanRise("OK", 0.78) || !tbl.CanRise("OK", 0.75) {
		t.Error("OK to PLENTY band")
	}
	if tbl.CanRise("TIGHT", 0.97) || !tbl.CanRise("TIGHT", 0.95) {
		t.Error("TIGHT to OK band")
	}
	if tbl.CanRise("EXHAUSTED", 1.22) || !tbl.CanRise("EXHAUSTED", 1.2) {
		t.Error("EXHAUSTED to TIGHT band")
	}
	// A floored TIGHT observation carries the gate.
	b := doc(win(95, resetsFor(DefaultBlock, 0.9)), nil, ts(-time.Minute))
	obs, _, _ := measure(t, s, cmdruntest.Out(string(b)), "")
	if o := one(t, obs, Resource5h); o.CanRise == nil {
		t.Error("no CanRise gate")
	}
}

func TestBandsBoundaries(t *testing.T) {
	tbl := New().table("x")
	for v, want := range map[float64]string{0.79: "PLENTY", 0.8: "OK", 0.99: "OK", 1.0: "TIGHT", 1.24: "TIGHT", 1.25: "EXHAUSTED"} {
		if got, _ := tbl.Classify(v); got != want {
			t.Errorf("%v: %q want %q", v, got, want)
		}
	}
	if got := New().Bands(); len(got) != 2 || got[0].Resource != Resource5h || got[1].Resource != ResourceWeek || len(got[0].Bands) != 4 {
		t.Errorf("bands %+v", got)
	}
}

func TestFailedCall(t *testing.T) {
	cases := map[string]cmdrun.Result{
		"exit":     cmdruntest.Fail(3, "daemon down"),
		"start":    {Code: -1, StartErr: fmt.Errorf("not found")},
		"timeout":  {Code: -1, Signal: "killed", CtxErr: context.DeadlineExceeded},
		"not json": cmdruntest.Out("garbage"),
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			obs, _, err := measure(t, New(), res, "")
			if err == nil || len(obs) != 0 {
				t.Errorf("want an error, got %v %+v", err, obs)
			}
		})
	}
	_, _, err := measure(t, New(), cmdruntest.Fail(3, "daemon down"), "")
	if !strings.Contains(err.Error(), "daemon down") {
		t.Errorf("cause lost: %v", err)
	}
}

func TestParse(t *testing.T) {
	if _, err := Parse([]byte(`{"rate_limits":`)); err == nil {
		t.Error("want error")
	}
	rl, err := Parse([]byte(`{}`))
	if err != nil || rl.FiveHour != nil {
		t.Errorf("%v %+v", err, rl)
	}
}

func TestRealSampleFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "rate-limits-real.json"))
	if err != nil {
		t.Fatal(err)
	}
	rl, err := Parse(b)
	if err != nil || rl.FiveHour == nil || rl.FiveHour.UsedPct == nil || rl.CapturedAt == nil || rl.SevenDay != nil {
		t.Fatalf("%v %+v", err, rl)
	}
	captured, _ := time.Parse(time.RFC3339, *rl.CapturedAt)
	s := New()
	f := cmdruntest.New(t)
	f.On(cmdruntest.MatchName("pa-monitor")).Return(cmdruntest.Out(string(b)))
	s.Run = f.Runner()
	obs, err := s.Measure(context.Background(), sensorcli.Env{Now: captured.Add(time.Minute), Stderr: &bytes.Buffer{}})
	if err != nil || len(obs) != 1 || obs[0].Resource != Resource5h {
		t.Fatalf("%v %+v", err, obs)
	}
}

// run drives the real dispatch with a fake runner.
func run(t *testing.T, conf string, res cmdrun.Result, args ...string) (int, string, string) {
	t.Helper()
	f := cmdruntest.New(t)
	f.On(cmdruntest.MatchName("pa-monitor")).Return(res)
	s := New()
	s.Run = f.Runner()
	var out, errb bytes.Buffer
	a := append([]string{"claude-sensor", "--state-dir", t.TempDir()}, args...)
	if conf != "" {
		p := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(p, []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
		a = append(a, "--config", p)
	}
	code := sensorcli.Run(a, sensorcli.Registry{"claude-sensor": s}, sensorcli.IO{
		Stdout: &out, Stderr: &errb, Now: func() time.Time { return now }, Getenv: func(string) string { return "" },
	})
	return code, out.String(), errb.String()
}

func TestDispatch(t *testing.T) {
	good := doc(win(20, resetsFor(DefaultBlock, 0.05)), nil, ts(-time.Minute))
	// First run starts at the worst level (lost state); the answer line is valid JSON for the resource.
	code, out, _ := run(t, "", cmdruntest.Out(string(good)))
	if code != 0 || !strings.Contains(out, `"resource":"claude_5h"`) || strings.Count(out, "\n") != 1 {
		t.Fatalf("%d %q", code, out)
	}
	// A call with no readable window: exit 0, no lines.
	code, out, _ = run(t, "", cmdruntest.Out(`{"rate_limits":{}}`))
	if code != 0 || out != "" {
		t.Errorf("empty: %d %q", code, out)
	}
	// A failed call: exit 3 with the cause on stderr.
	code, out, se := run(t, "", cmdruntest.Fail(2, "boom"))
	if code != 3 || out != "" || !strings.Contains(se, "boom") {
		t.Errorf("failed: %d %q %q", code, out, se)
	}
	// --bands prints both resources.
	code, out, _ = run(t, "", cmdrun.Result{}, "--bands")
	if code != 0 || strings.Count(out, "\n") != 2 {
		t.Errorf("bands: %d %q", code, out)
	}
	// --raw: no state, level from the reading.
	code, out, _ = run(t, "", cmdruntest.Out(string(good)), "--raw")
	if code != 0 || !strings.Contains(out, `"level":"PLENTY"`) && !strings.Contains(out, `"level":"OK"`) {
		t.Errorf("raw: %d %q", code, out)
	}
}

func TestConfigure(t *testing.T) {
	good := `command = "pa"
timeout = "5s"
max_age = "1h"
p0 = 0.4
floor_pct = 90
plenty_below = 0.7
ok_below = 1
tight_below = 1.5
recovery_band = 0.1
block_window = "4h"
block_k = 0.3
block_c = 0.2
week_window = "168h"
week_k = 0.2
week_c = 0.1
`
	s := New()
	cfg := loadCfg(t, good)
	if err := s.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if s.Command != "pa" || s.Timeout != 5*time.Second || s.MaxAge != time.Hour || s.P0 != 0.4 || s.FloorU != 0.9 ||
		s.Plenty != 0.7 || s.OK != 1 || s.Tight != 1.5 || s.Band != 0.1 || s.Block != (Window{4 * time.Hour, 0.3, 0.2}) ||
		s.Week != (Window{168 * time.Hour, 0.2, 0.1}) {
		t.Errorf("%+v", s)
	}
	for name, conf := range map[string]string{
		"unknown key": `bogus = 1`,
		"bad number":  `p0 = "x"`,
		"bad dur":     `timeout = 5`,
		"zero dur":    `max_age = "0s"`,
		"order":       "plenty_below = 2\nok_below = 1",
		"bad k":       `block_k = 0`,
		"bad floor":   `floor_pct = 101`,
		"empty cmd":   `command = ""`,
		"bad cmd":     `command = 3`,
	} {
		if err := New().Configure(loadCfg(t, conf)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// Through the CLI an invalid key exits 2.
	if code, _, se := run(t, "bogus = 1", cmdrun.Result{}); code != 2 || !strings.Contains(se, "bogus") {
		t.Errorf("cli: %d %q", code, se)
	}
}

func loadCfg(t *testing.T, conf string) sensorcli.Config {
	t.Helper()
	var cfg sensorcli.Config
	var raw map[string]any
	if err := tomlDecode(conf, &raw); err != nil {
		t.Fatal(err)
	}
	cfg.Raw = raw
	return cfg
}
