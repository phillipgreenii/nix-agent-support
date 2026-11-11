// Package claude implements claude-sensor: the smooth Claude pace of the
// resources claude_5h and claude_week, read from the light pa-monitor call
// `pa-monitor status --rate-limits` (key rate_limits: five_hour and seven_day,
// each with used_pct and resets_at, plus one shared captured_at; every field
// optional). The sensor never touches Claude; it reads only that status API.
//
// For a window of length W: u = used_pct/100; e = 1 - (resets_at-now)/W clamped
// to [0,1]; e_eff = max(e, c); pace = (u + k*p0) / (e_eff + k). The pace is
// classified on the four-step scale (PLENTY < 0.8, OK 0.8 to 1.0, TIGHT 1.0 to
// 1.25, EXHAUSTED >= 1.25) with an additive 0.05 recovery band on a rise. A
// floor holds the level at TIGHT or worse once u >= 0.95 (never EXHAUSTED from
// the floor alone). resets_at <= now is a new window: PLENTY at once with the
// rise dwell skipped.
//
// "No reading" is the ABSENCE of an answer line for a window (the router reads
// it as unknown, the best level): an absent window key, an absent used_pct, a
// missing, future, too old or too early captured_at. When resets_at is absent
// only the floor applies. A failed pa-monitor call writes the cause to stderr
// and exits 1 (the router keeps the last level); a successful call with no
// readable window exits 0 with no lines (EmptyOK).
//
// Config keys (all optional): command (default pa-monitor), timeout (default
// 15s), max_age (captured_at older than this is unknown, default 15m), p0
// (default 0.5), floor_pct (default 95), plenty_below, ok_below, tight_below
// (default 0.8, 1.0, 1.25), recovery_band (default 0.05), block_window,
// block_k, block_c, week_window, week_k, week_c.
//
// Exported names: Sensor, New, Window, Pace, Elapsed, Parse, RateLimits,
// Resource5h, ResourceWeek, DefaultBlock, DefaultWeek.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

// Resources, the pa-monitor command and defaults.
const (
	Resource5h   = "claude_5h"
	ResourceWeek = "claude_week"
	Unit         = "ratio"

	DefaultCommand = "pa-monitor"
	DefaultTimeout = 15 * time.Second
	DefaultMaxAge  = 15 * time.Minute
	DefaultP0      = 0.5
	DefaultFloor   = 95.0
	DefaultBand    = 0.05
)

// RateLimitsArgs is the argument list of the light pa-monitor call.
var RateLimitsArgs = []string{"status", "--rate-limits"}

// Window holds the pace constants of one window.
type Window struct {
	W time.Duration // window length
	K float64       // prior weight
	C float64       // early-window floor on e
}

// Strawman defaults (all configurable).
var (
	DefaultBlock = Window{W: 5 * time.Hour, K: 0.25, C: 0.15}
	DefaultWeek  = Window{W: 7 * 24 * time.Hour, K: 0.1, C: 0.05}
)

// Elapsed returns e: the elapsed fraction of the window, clamped to [0,1].
func Elapsed(w Window, resetsAt, now time.Time) float64 {
	e := 1 - float64(resetsAt.Sub(now))/float64(w.W)
	switch {
	case e < 0:
		return 0
	case e > 1:
		return 1
	}
	return e
}

// Pace is (u + k*p0) / (max(e,c) + k).
func Pace(w Window, p0, u, e float64) float64 {
	if e < w.C {
		e = w.C
	}
	// Rounded to 9 places so a value that is exactly on a boundary in the
	// design table does not fall to the wrong side by floating-point error.
	return math.Round((u+w.K*p0)/(e+w.K)*1e9) / 1e9
}

// WindowJSON is one window of the rate_limits document.
type WindowJSON struct {
	UsedPct  *float64 `json:"used_pct"`
	ResetsAt *string  `json:"resets_at"`
}

// RateLimits is the `status --rate-limits` document (rate_limits key).
type RateLimits struct {
	FiveHour   *WindowJSON `json:"five_hour"`
	SevenDay   *WindowJSON `json:"seven_day"`
	CapturedAt *string     `json:"captured_at"`
}

// Parse reads the document; an empty rate_limits (`{}`) is a valid document
// with no windows.
func Parse(b []byte) (RateLimits, error) {
	var doc struct {
		RateLimits *RateLimits `json:"rate_limits"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return RateLimits{}, fmt.Errorf("parse status --rate-limits output: %w", err)
	}
	if doc.RateLimits == nil {
		return RateLimits{}, nil
	}
	return *doc.RateLimits, nil
}

// Sensor is the claude-sensor probe set.
type Sensor struct {
	// Run is the command seam (production: cmdrun.Exec).
	Run     cmdrun.Runner
	Command string
	Timeout time.Duration
	MaxAge  time.Duration
	P0      float64
	FloorU  float64 // fraction, 0.95
	Plenty  float64 // PLENTY below this
	OK      float64 // OK below this
	Tight   float64 // TIGHT below this
	Band    float64
	Block   Window
	Week    Window
}

// New returns a Sensor with the strawman defaults.
func New() *Sensor {
	return &Sensor{
		Run: cmdrun.Exec, Command: DefaultCommand, Timeout: DefaultTimeout, MaxAge: DefaultMaxAge,
		P0: DefaultP0, FloorU: DefaultFloor / 100, Plenty: 0.8, OK: 1.0, Tight: 1.25, Band: DefaultBand,
		Block: DefaultBlock, Week: DefaultWeek,
	}
}

// Resources implements sensorcli.Sensor.
func (s *Sensor) Resources() []string { return []string{Resource5h, ResourceWeek} }

// EmptyOK implements sensorcli's optional empty-answer rule: a successful call
// with no readable window exits 0 with no lines.
func (s *Sensor) EmptyOK() bool { return true }

func (s *Sensor) table(resource string) levels.Table {
	return levels.Table{
		Resource: resource, Unit: Unit, Scale: levels.FourStep, Dir: levels.LowerBetter,
		Bands: []levels.Band{
			{Level: "EXHAUSTED", Min: levels.Num(s.Tight)},
			{Level: "TIGHT", Min: levels.Num(s.OK), Max: levels.Num(s.Tight)},
			{Level: "OK", Min: levels.Num(s.Plenty), Max: levels.Num(s.OK)},
			{Level: "PLENTY", Max: levels.Num(s.Plenty)},
		},
		Hyst: levels.Hysteresis{Factor: 1, Add: -s.Band},
	}
}

// Bands implements sensorcli.Sensor.
func (s *Sensor) Bands() []levels.Info {
	return []levels.Info{s.table(Resource5h).Info(), s.table(ResourceWeek).Info()}
}

// Configure implements sensorcli.Sensor.
func (s *Sensor) Configure(cfg sensorcli.Config) error {
	if err := cfg.CheckKeys("command", "timeout", "max_age", "p0", "floor_pct", "plenty_below", "ok_below",
		"tight_below", "recovery_band", "block_window", "block_k", "block_c", "week_window", "week_k", "week_c"); err != nil {
		return err
	}
	var err error
	if s.Command, err = cfg.String("command", s.Command); err != nil {
		return err
	}
	if s.Command == "" {
		return fmt.Errorf("config key command: must not be empty")
	}
	for _, d := range []struct {
		key string
		dst *time.Duration
	}{{"timeout", &s.Timeout}, {"max_age", &s.MaxAge}, {"block_window", &s.Block.W}, {"week_window", &s.Week.W}} {
		if *d.dst, err = cfg.Duration(d.key, *d.dst); err != nil {
			return err
		}
		if *d.dst <= 0 {
			return fmt.Errorf("config key %s: must be positive", d.key)
		}
	}
	floorPct := s.FloorU * 100
	for _, f := range []struct {
		key string
		dst *float64
	}{
		{"p0", &s.P0},
		{"floor_pct", &floorPct},
		{"plenty_below", &s.Plenty},
		{"ok_below", &s.OK},
		{"tight_below", &s.Tight},
		{"recovery_band", &s.Band},
		{"block_k", &s.Block.K},
		{"block_c", &s.Block.C},
		{"week_k", &s.Week.K},
		{"week_c", &s.Week.C},
	} {
		if *f.dst, err = float(cfg, f.key, *f.dst); err != nil {
			return err
		}
	}
	s.FloorU = floorPct / 100
	if !(s.Plenty < s.OK && s.OK < s.Tight) {
		return fmt.Errorf("config keys plenty_below, ok_below, tight_below: want plenty_below < ok_below < tight_below, got %v, %v, %v", s.Plenty, s.OK, s.Tight)
	}
	if s.Band < 0 || s.P0 < 0 || s.Block.K <= 0 || s.Week.K <= 0 || s.Block.C < 0 || s.Week.C < 0 || floorPct <= 0 || floorPct > 100 {
		return fmt.Errorf("config: recovery_band, p0, block_c, week_c must be >= 0, block_k and week_k > 0, floor_pct in (0,100]")
	}
	return nil
}

// float reads a number key (TOML float or integer).
func float(c sensorcli.Config, key string, def float64) (float64, error) {
	v, ok := c.Raw[key]
	if !ok {
		return def, nil
	}
	switch n := v.(type) {
	case float64:
		return n, nil
	case int64:
		return float64(n), nil
	}
	return 0, fmt.Errorf("config key %s: want a number, got %T", key, v)
}

// Measure implements sensorcli.Sensor.
func (s *Sensor) Measure(ctx context.Context, env sensorcli.Env) ([]levels.Observation, error) {
	cctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	res := s.Run(cctx, cmdrun.Call{Name: s.Command, Args: RateLimitsArgs, OwnProcGroup: true})
	switch {
	case res.StartErr != nil:
		return nil, fmt.Errorf("%s status --rate-limits: %v", s.Command, res.StartErr)
	case res.CtxErr != nil:
		return nil, fmt.Errorf("%s status --rate-limits: %v", s.Command, res.CtxErr)
	case !res.OK():
		return nil, fmt.Errorf("%s status --rate-limits: exit %d: %s", s.Command, res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	rl, err := Parse(res.Stdout)
	if err != nil {
		return nil, err
	}
	var obs []levels.Observation
	for _, w := range []struct {
		resource string
		cfg      Window
		win      *WindowJSON
	}{{Resource5h, s.Block, rl.FiveHour}, {ResourceWeek, s.Week, rl.SevenDay}} {
		if env.Resource != "" && env.Resource != w.resource {
			continue
		}
		o, why := s.observe(w.resource, w.cfg, w.win, rl.CapturedAt, env.Now)
		if why != "" {
			fmt.Fprintf(env.Stderr, "%s: no reading: %s\n", w.resource, why)
			continue
		}
		obs = append(obs, o)
	}
	return obs, nil
}

// observe turns one window into an observation; a non-empty reason means
// "no reading".
func (s *Sensor) observe(resource string, cfg Window, win *WindowJSON, capturedRaw *string, now time.Time) (levels.Observation, string) {
	if win == nil || win.UsedPct == nil {
		return levels.Observation{}, "window or used_pct absent"
	}
	if capturedRaw == nil {
		return levels.Observation{}, "captured_at absent"
	}
	captured, err := time.Parse(time.RFC3339, *capturedRaw)
	if err != nil {
		return levels.Observation{}, "captured_at unparseable"
	}
	var resets time.Time
	hasResets := false
	if win.ResetsAt != nil {
		if t, perr := time.Parse(time.RFC3339, *win.ResetsAt); perr == nil {
			resets, hasResets = t, true
		}
	}
	switch {
	case captured.After(now):
		return levels.Observation{}, "captured_at is in the future"
	case now.Sub(captured) > s.MaxAge:
		return levels.Observation{}, "captured_at is older than " + s.MaxAge.String()
	case hasResets && captured.Before(resets.Add(-cfg.W)):
		return levels.Observation{}, "captured_at is earlier than resets_at minus the window length"
	}
	tbl := s.table(resource)
	if hasResets && !resets.After(now) {
		return levels.Observation{Resource: resource, Scale: levels.FourStep, Level: levels.FourStep.Best(), BypassDwell: true}, ""
	}
	u := *win.UsedPct / 100
	if u < 0 {
		u = 0
	} else if u > 1 {
		u = 1
	}
	floored := u >= s.FloorU
	if !hasResets {
		if !floored {
			return levels.Observation{}, "resets_at absent and below the floor"
		}
		return levels.Observation{Resource: resource, Scale: levels.FourStep, Level: "TIGHT"}, ""
	}
	pace := Pace(cfg, s.P0, u, Elapsed(cfg, resets, now))
	o, err := tbl.Observe(pace)
	if err != nil {
		return levels.Observation{}, err.Error()
	}
	if floored && levels.FourStep.Index(o.Level) > levels.FourStep.Index("TIGHT") {
		o.Level = "TIGHT"
	}
	return o, ""
}
