package config

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/internal/levels"
)

// Resource configuration: the resource registry ([[resource]]) and the
// router-side level settings ([levels]).
//
// Exported names: Resource, ResourceSet (Find, Registry), LevelsConfig,
// DefaultLevelsConfig, IgnoredAny, UnknownBest, UnknownWorst, RouterSensor,
// DefaultQueryDeadline is in internal/query.
//
// Choices made here (implementer-owned): a resource's `ignored` value is the
// text "ANY" (the resource does not care when a registration has no clause on
// it, the default) or "<op> <LEVEL>" (an unmentioned resource reads as that
// clause, for example "== RUNNING" for the router resource); the grace factor
// is a number of valid_for_s multiples (key grace_factor) that defaults to the
// [levels].grace_factor value; "sensors configured on this machine" are the
// sensors named by registered level-source queries, the router itself
// (RouterSensor), and Config.MachineSensors (a code-level seam, not a TOML key).

// IgnoredAny is the `ignored` value that means "does not care".
const IgnoredAny = "ANY"

// The two values of a resource's unknown key.
const (
	UnknownBest  = "best"
	UnknownWorst = "worst"
)

// RouterSensor is the sensor name of the router's own resource; the router is
// always present on the machine that runs it.
const RouterSensor = "router"

// Resource is one resource of the registry: its ordered scale (best first),
// what silence means for a registration (Ignored), the sensor that answers for
// it, the stale-grace factor (a multiple of valid_for_s) and what an absent
// level reads (Unknown: best or worst).
type Resource struct {
	Name        string
	Scale       []string
	Ignored     string
	Sensor      string
	GraceFactor float64
	Unknown     string
}

// ResourceSet is the ordered resource registry of a deployment.
type ResourceSet []Resource

// Find returns the resource called name.
func (rs ResourceSet) Find(name string) (Resource, bool) {
	for _, r := range rs {
		if r.Name == name {
			return r, true
		}
	}
	return Resource{}, false
}

// UnknownLevel is the level an absent reading of r takes: the best level for
// unknown = best, the worst for unknown = worst.
func (r Resource) UnknownLevel() string {
	if len(r.Scale) == 0 {
		return ""
	}
	if r.Unknown == UnknownWorst {
		return r.Scale[len(r.Scale)-1]
	}
	return r.Scale[0]
}

// Registry converts the set into the input type of the decision function, so
// callers of levels.Decide need not import this package.
func (rs ResourceSet) Registry() levels.Registry {
	out := make(levels.Registry, len(rs))
	for _, r := range rs {
		out[r.Name] = levels.Scale{Levels: slices.Clone(r.Scale), Unknown: r.UnknownLevel()}
	}
	return out
}

// resourceTOML is one [[resource]] entry.
type resourceTOML struct {
	Name        string   `toml:"name"`
	Scale       []string `toml:"scale"`
	Ignored     string   `toml:"ignored"`
	Sensor      string   `toml:"sensor"`
	GraceFactor *float64 `toml:"grace_factor"`
	Unknown     string   `toml:"unknown"`
}

// LevelsConfig holds the router-side level settings of the [levels] table.
// The packets that use a value read it from the parsed config.
type LevelsConfig struct {
	// ObservedAtSkew bounds how far observed_at may be ahead of now (5m).
	ObservedAtSkew time.Duration
	// GraceFactor is the default stale-grace factor, a multiple of valid_for_s
	// (2); a [[resource]] grace_factor overrides it.
	GraceFactor float64
	// StartWait is how long the router waits for sensors at start (10s).
	StartWait time.Duration
	// StartStagger spaces the first admissions after a start (30s).
	StartStagger time.Duration
	// WakeThreshold is the clock jump that counts as a wake from sleep (30s).
	WakeThreshold time.Duration
}

// DefaultLevelsConfig returns the strawman values.
func DefaultLevelsConfig() LevelsConfig {
	return LevelsConfig{
		ObservedAtSkew: 5 * time.Minute,
		GraceFactor:    2,
		StartWait:      10 * time.Second,
		StartStagger:   30 * time.Second,
		WakeThreshold:  30 * time.Second,
	}
}

// levelsTOML is the [levels] table: snake_case keys, duration strings, no env
// override. An unknown key or an unparsable value is a config error.
type levelsTOML struct {
	ObservedAtSkew *duration `toml:"observed_at_skew"`
	GraceFactor    *float64  `toml:"grace_factor"`
	StartWait      *duration `toml:"start_wait"`
	StartStagger   *duration `toml:"start_stagger"`
	WakeThreshold  *duration `toml:"wake_threshold"`
}

// buildLevelsConfig overlays t onto the defaults and validates every value.
func buildLevelsConfig(t *levelsTOML) (LevelsConfig, error) {
	lc := DefaultLevelsConfig()
	if t == nil {
		return lc, nil
	}
	var errs []string
	pos := func(key string, d *duration, dst *time.Duration, allowZero bool) {
		if d == nil {
			return
		}
		if d.D < 0 || (d.D == 0 && !allowZero) {
			errs = append(errs, fmt.Sprintf("levels.%s must be %s, got %s", key, map[bool]string{true: ">= 0", false: "> 0"}[allowZero], d.D))
			return
		}
		*dst = d.D
	}
	pos("observed_at_skew", t.ObservedAtSkew, &lc.ObservedAtSkew, false)
	pos("start_wait", t.StartWait, &lc.StartWait, true)
	pos("start_stagger", t.StartStagger, &lc.StartStagger, true)
	pos("wake_threshold", t.WakeThreshold, &lc.WakeThreshold, false)
	if t.GraceFactor != nil {
		if *t.GraceFactor <= 0 {
			errs = append(errs, fmt.Sprintf("levels.grace_factor must be > 0, got %v", *t.GraceFactor))
		} else {
			lc.GraceFactor = *t.GraceFactor
		}
	}
	if len(errs) > 0 {
		return LevelsConfig{}, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return lc, nil
}

// buildResources validates and builds the registry. Every problem is reported
// (aggregated), each naming the resource.
func buildResources(rts []resourceTOML, lc LevelsConfig) (ResourceSet, []error) {
	var out ResourceSet
	var errs []error
	seen := map[string]bool{}
	for i, rt := range rts {
		bad := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("resource[%d] %q: %s", i, rt.Name, fmt.Sprintf(format, a...)))
		}
		if rt.Name == "" {
			bad("name is required")
			continue
		}
		if seen[rt.Name] {
			bad("duplicate resource name")
			continue
		}
		seen[rt.Name] = true
		if len(rt.Scale) < 2 {
			bad("scale must list at least two levels, best first")
			continue
		}
		if dup := firstDuplicateOrEmpty(rt.Scale); dup != "" {
			bad("scale has an empty or repeated level %q", dup)
			continue
		}
		unknown := rt.Unknown
		if unknown == "" {
			unknown = UnknownBest
		}
		if unknown != UnknownBest && unknown != UnknownWorst {
			bad("unknown must be %q or %q, got %q", UnknownBest, UnknownWorst, unknown)
			continue
		}
		grace := lc.GraceFactor
		if rt.GraceFactor != nil {
			if *rt.GraceFactor <= 0 {
				bad("grace_factor must be > 0, got %v", *rt.GraceFactor)
				continue
			}
			grace = *rt.GraceFactor
		}
		ignored := strings.TrimSpace(rt.Ignored)
		if ignored == "" {
			ignored = IgnoredAny
		}
		res := Resource{Name: rt.Name, Scale: rt.Scale, Ignored: ignored, Sensor: rt.Sensor, GraceFactor: grace, Unknown: unknown}
		if ignored != IgnoredAny {
			if _, err := ignoredAllowed(res); err != nil {
				bad("ignored: %v", err)
				continue
			}
		}
		out = append(out, res)
	}
	return out, errs
}

func firstDuplicateOrEmpty(ss []string) string {
	seen := map[string]bool{}
	for _, s := range ss {
		if s == "" || seen[s] {
			return s
		}
		seen[s] = true
	}
	return ""
}

// ignoredAllowed parses a non-ANY ignored value ("<op> <LEVEL>") into the
// allowed set it stands for.
func ignoredAllowed(r Resource) ([]string, error) {
	f := strings.Fields(r.Ignored)
	if len(f) != 2 {
		return nil, fmt.Errorf("want %q or \"<op> <LEVEL>\", got %q", IgnoredAny, r.Ignored)
	}
	op, lvl := levels.Op(f[0]), f[1]
	if !validOp(op) {
		return nil, fmt.Errorf("unknown operator %q", f[0])
	}
	if !slices.Contains(r.Scale, lvl) {
		return nil, fmt.Errorf("level %q is not on the scale %s", lvl, strings.Join(r.Scale, " > "))
	}
	set := levels.AllowedLevels(levels.Scale{Levels: r.Scale}, op, lvl)
	if len(set) == 0 {
		return nil, fmt.Errorf("%q allows no level", r.Ignored)
	}
	return set, nil
}

func validOp(op levels.Op) bool {
	switch op {
	case levels.OpEq, levels.OpNe, levels.OpLt, levels.OpLe, levels.OpGt, levels.OpGe:
		return true
	}
	return false
}
