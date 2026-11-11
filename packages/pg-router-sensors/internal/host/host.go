// Package host implements the disk, cpu and ram resources of the host-sensor
// subcommand: statfs Bavail (lowest over a configured list of paths) for disk,
// the 1-minute load per core for cpu, and the worst of swap in use and the
// kernel memory-pressure level for ram, with the rule that a high swap on a
// low disk drops ram one step.
//
// Exported names: Sensor, New, Statfs, Defaults, ParseLoadAvg, ParseSwapUsedGiB,
// ParsePressure, and the argv constants Sysctl*.
package host

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/netprobe"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

// Resource names answered by this package.
const (
	ResDisk = "disk"
	ResCPU  = "cpu"
	ResRAM  = "ram"
	// ResNetwork is the public-internet reachability resource.
	ResNetwork = "network"
)

// The only argv the sensor runs (command name and arguments).
const (
	SysctlName     = "sysctl"
	SysctlLoad     = "vm.loadavg"
	SysctlSwap     = "vm.swapusage"
	SysctlNCPU     = "hw.ncpu"
	SysctlPressure = "kern.memorystatus_vm_pressure_level"
)

const gib = float64(1 << 30)

// probeTimeout bounds each sysctl probe.
const probeTimeout = 5 * time.Second

// Statfs returns the bytes available to an unprivileged user (Bavail) at path.
type Statfs func(path string) (uint64, error)

// SystemStatfs is the production Statfs.
func SystemStatfs(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:unconvert // field widths differ per OS
}

// Thresholds are the configurable band edges (strawman, tune after deploy).
type Thresholds struct {
	// Disk free GiB lower bounds of PLENTY, OK and TIGHT.
	DiskPlenty, DiskOK, DiskTight float64
	// CPU load per core upper (exclusive) bounds of PLENTY, OK and TIGHT.
	CPUPlenty, CPUOK, CPUTight float64
	// Swap GiB upper (exclusive) bounds of PLENTY, OK and TIGHT.
	SwapPlenty, SwapOK, SwapTight float64
	// Step-down: engaged at swap >= StepSwap with lowest free disk < StepDisk.
	StepSwap, StepDisk float64
}

// Defaults returns the initial thresholds of the design.
func Defaults() Thresholds {
	return Thresholds{
		DiskPlenty: 40, DiskOK: 25, DiskTight: 12,
		CPUPlenty: 4, CPUOK: 8, CPUTight: 14,
		SwapPlenty: 2, SwapOK: 10, SwapTight: 20,
		StepSwap: 10, StepDisk: 25,
	}
}

// Sensor is the host-sensor probe set for disk, cpu and ram.
type Sensor struct {
	// Run is the command seam (production: cmdrun.Exec).
	Run cmdrun.Runner
	// Statfs is the filesystem seam.
	Statfs Statfs
	// Paths are the filesystems whose lowest free space is the disk reading.
	Paths []string
	T     Thresholds
	// Net probes the network resource's targets (nil Client: the default
	// verifying client; tests inject one pointed at httptest servers).
	Net *netprobe.Prober
	// NetTargets are the URLs probed for network (default: three public
	// example sites); NetThreshold is the consecutive all-fail passes before
	// UNAVAILABLE.
	NetTargets   []string
	NetThreshold int
}

// New returns a Sensor on the production seams with the default paths ("/")
// and thresholds.
func New() *Sensor {
	return &Sensor{
		Run: cmdrun.Exec, Statfs: SystemStatfs, Paths: []string{"/"}, T: Defaults(),
		Net: &netprobe.Prober{}, NetTargets: netprobe.DefaultTargets, NetThreshold: netprobe.DefaultThreshold,
	}
}

// Resources implements sensorcli.Sensor.
func (s *Sensor) Resources() []string { return []string{ResCPU, ResDisk, ResNetwork, ResRAM} }

func (s *Sensor) diskTable() levels.Table {
	return levels.Table{
		Resource: ResDisk, Unit: "GiB", Scale: levels.FourStep, Dir: levels.HigherBetter, Hyst: levels.HysteresisHigher,
		Bands: []levels.Band{
			{Level: "EXHAUSTED", Max: levels.Num(s.T.DiskTight)},
			{Level: "TIGHT", Min: levels.Num(s.T.DiskTight), Max: levels.Num(s.T.DiskOK)},
			{Level: "OK", Min: levels.Num(s.T.DiskOK), Max: levels.Num(s.T.DiskPlenty)},
			{Level: "PLENTY", Min: levels.Num(s.T.DiskPlenty)},
		},
	}
}

func (s *Sensor) cpuTable() levels.Table {
	return levels.Table{
		Resource: ResCPU, Unit: "load_per_core", Scale: levels.FourStep, Dir: levels.LowerBetter, Hyst: levels.HysteresisLower,
		Bands: []levels.Band{
			{Level: "EXHAUSTED", Min: levels.Num(s.T.CPUTight)},
			{Level: "TIGHT", Min: levels.Num(s.T.CPUOK), Max: levels.Num(s.T.CPUTight)},
			{Level: "OK", Min: levels.Num(s.T.CPUPlenty), Max: levels.Num(s.T.CPUOK)},
			{Level: "PLENTY", Max: levels.Num(s.T.CPUPlenty)},
		},
	}
}

func (s *Sensor) swapTable() levels.Table {
	return levels.Table{
		Resource: ResRAM, Unit: "GiB", Scale: levels.FourStep, Dir: levels.LowerBetter, Hyst: levels.HysteresisLower,
		Bands: []levels.Band{
			{Level: "EXHAUSTED", Min: levels.Num(s.T.SwapTight)},
			{Level: "TIGHT", Min: levels.Num(s.T.SwapOK), Max: levels.Num(s.T.SwapTight)},
			{Level: "OK", Min: levels.Num(s.T.SwapPlenty), Max: levels.Num(s.T.SwapOK)},
			{Level: "PLENTY", Max: levels.Num(s.T.SwapPlenty)},
		},
	}
}

// Bands implements sensorcli.Sensor: the ram bands are the swap bands (the
// reading is swap used in GiB).
func (s *Sensor) Bands() []levels.Info {
	return []levels.Info{s.cpuTable().Info(), s.diskTable().Info(), levels.NoBands(ResNetwork, "targets_reachable"), s.swapTable().Info()}
}

var configKeys = []string{
	"paths",
	"disk_plenty_min", "disk_ok_min", "disk_tight_min",
	"cpu_plenty_max", "cpu_ok_max", "cpu_tight_max",
	"swap_plenty_max", "swap_ok_max", "swap_tight_max",
	"stepdown_swap_gib", "stepdown_disk_gib",
	"network_targets", "network_timeout", "network_failures",
}

// Configure implements sensorcli.Sensor. Keys: paths (list of strings) and the
// numeric band edges named in configKeys; an absent key keeps its default.
func (s *Sensor) Configure(cfg sensorcli.Config) error {
	if err := cfg.CheckKeys(configKeys...); err != nil {
		return err
	}
	paths, err := cfg.StringList("paths")
	if err != nil {
		return err
	}
	if paths != nil {
		if len(paths) == 0 {
			return fmt.Errorf("config key paths: must not be empty")
		}
		s.Paths = paths
	}
	for key, dst := range map[string]*float64{
		"disk_plenty_min": &s.T.DiskPlenty, "disk_ok_min": &s.T.DiskOK, "disk_tight_min": &s.T.DiskTight,
		"cpu_plenty_max": &s.T.CPUPlenty, "cpu_ok_max": &s.T.CPUOK, "cpu_tight_max": &s.T.CPUTight,
		"swap_plenty_max": &s.T.SwapPlenty, "swap_ok_max": &s.T.SwapOK, "swap_tight_max": &s.T.SwapTight,
		"stepdown_swap_gib": &s.T.StepSwap, "stepdown_disk_gib": &s.T.StepDisk,
	} {
		v, ok := cfg.Raw[key]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case int64:
			*dst = float64(n)
		case float64:
			*dst = n
		default:
			return fmt.Errorf("config key %s: want a number, got %T", key, v)
		}
		if math.IsNaN(*dst) || *dst < 0 {
			return fmt.Errorf("config key %s: must be a non-negative number", key)
		}
	}
	if s.Net == nil {
		s.Net = &netprobe.Prober{}
	}
	if s.NetThreshold == 0 {
		s.NetThreshold = netprobe.DefaultThreshold
	}
	if len(s.NetTargets) == 0 {
		s.NetTargets = netprobe.DefaultTargets
	}
	targets, err := cfg.StringList("network_targets")
	if err != nil {
		return err
	}
	if targets != nil {
		if len(targets) == 0 {
			return fmt.Errorf("config key network_targets: must not be empty")
		}
		for _, u := range targets {
			if !strings.HasPrefix(u, "https://") {
				return fmt.Errorf("config key network_targets: %q is not an https:// URL", u)
			}
		}
		s.NetTargets = targets
	}
	if err := netprobe.ConfigureCommon(cfg, "network_timeout", "network_failures", s.Net, &s.NetThreshold); err != nil {
		return err
	}
	t := s.T
	if !(t.DiskTight < t.DiskOK && t.DiskOK < t.DiskPlenty) {
		return fmt.Errorf("disk bands: want disk_tight_min < disk_ok_min < disk_plenty_min")
	}
	if !(t.CPUPlenty < t.CPUOK && t.CPUOK < t.CPUTight) {
		return fmt.Errorf("cpu bands: want cpu_plenty_max < cpu_ok_max < cpu_tight_max")
	}
	if !(t.SwapPlenty < t.SwapOK && t.SwapOK < t.SwapTight) {
		return fmt.Errorf("swap bands: want swap_plenty_max < swap_ok_max < swap_tight_max")
	}
	return nil
}

// ParseLoadAvg returns the 1-minute load from `sysctl -n vm.loadavg` output
// ("{ 1.23 4.56 7.89 }").
func ParseLoadAvg(out string) (float64, error) {
	f := strings.Fields(strings.Trim(strings.TrimSpace(out), "{}"))
	if len(f) < 1 {
		return 0, fmt.Errorf("loadavg: no values in %q", out)
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("loadavg: bad value %q", f[0])
	}
	return v, nil
}

var swapUsed = regexp.MustCompile(`used\s*=\s*([0-9]+(?:\.[0-9]+)?)([KMGT])`)

// ParseSwapUsedGiB returns the swap in use, in GiB, from `sysctl -n
// vm.swapusage` output.
func ParseSwapUsedGiB(out string) (float64, error) {
	m := swapUsed.FindStringSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("swapusage: no used value in %q", strings.TrimSpace(out))
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("swapusage: %w", err)
	}
	switch m[2] {
	case "K":
		return v / (1 << 20), nil
	case "M":
		return v / (1 << 10), nil
	case "G":
		return v, nil
	default:
		return v * (1 << 10), nil
	}
}

// ParsePressure maps `sysctl -n kern.memorystatus_vm_pressure_level` output to
// a level: 1 PLENTY, 2 OK, 4 EXHAUSTED. Any other value is an error (no reading
// for the pressure component).
func ParsePressure(out string) (string, error) {
	switch strings.TrimSpace(out) {
	case "1":
		return "PLENTY", nil
	case "2":
		return "OK", nil
	case "4":
		return "EXHAUSTED", nil
	}
	return "", fmt.Errorf("pressure: unreadable value %q", strings.TrimSpace(out))
}

func (s *Sensor) sysctl(ctx context.Context, key string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	res := s.Run(ctx, cmdrun.Call{Name: SysctlName, Args: []string{"-n", key}})
	if res.StartErr != nil {
		return "", fmt.Errorf("sysctl %s: %w", key, res.StartErr)
	}
	if res.CtxErr != nil {
		return "", fmt.Errorf("sysctl %s: %w", key, res.CtxErr)
	}
	if !res.OK() {
		return "", fmt.Errorf("sysctl %s: exit %d: %s", key, res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	return string(res.Stdout), nil
}

// lowestFreeGiB returns the lowest Bavail in GiB over the configured paths,
// skipping an unreadable path; ok is false when no path is readable.
func (s *Sensor) lowestFreeGiB(env sensorcli.Env) (float64, bool) {
	lowest, ok := 0.0, false
	for _, p := range s.Paths {
		b, err := s.Statfs(p)
		if err != nil {
			fmt.Fprintf(env.Stderr, "host-sensor: disk path %s skipped: %v\n", p, err)
			continue
		}
		g := float64(b) / gib
		if !ok || g < lowest {
			lowest, ok = g, true
		}
	}
	return lowest, ok
}

// Measure implements sensorcli.Sensor.
func (s *Sensor) Measure(ctx context.Context, env sensorcli.Env) ([]levels.Observation, error) {
	var out []levels.Observation
	want := func(r string) bool { return env.Resource == "" || env.Resource == r }

	free, haveDisk := 0.0, false
	if want(ResDisk) || want(ResRAM) {
		free, haveDisk = s.lowestFreeGiB(env)
	}
	if want(ResDisk) {
		if haveDisk {
			o, err := s.diskTable().Observe(free)
			if err != nil {
				return nil, err
			}
			out = append(out, o)
		} else {
			fmt.Fprintln(env.Stderr, "host-sensor: disk: no configured path is readable")
		}
	}
	if want(ResCPU) {
		if o, err := s.measureCPU(ctx); err != nil {
			fmt.Fprintf(env.Stderr, "host-sensor: cpu: %v\n", err)
		} else {
			out = append(out, o)
		}
	}
	if want(ResNetwork) && s.Net != nil {
		out = append(out, netprobe.Observe(ctx, s.Net, env, ResNetwork, s.NetTargets, s.NetThreshold))
	}
	if want(ResRAM) {
		if o, ok := s.measureRAM(ctx, env, free, haveDisk); ok {
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		return nil, levels.ErrNoReading
	}
	return out, nil
}

func (s *Sensor) measureCPU(ctx context.Context) (levels.Observation, error) {
	l, err := s.sysctl(ctx, SysctlLoad)
	if err != nil {
		return levels.Observation{}, err
	}
	load, err := ParseLoadAvg(l)
	if err != nil {
		return levels.Observation{}, err
	}
	n, err := s.sysctl(ctx, SysctlNCPU)
	if err != nil {
		return levels.Observation{}, err
	}
	cores, err := strconv.Atoi(strings.TrimSpace(n))
	if err != nil || cores < 1 {
		return levels.Observation{}, fmt.Errorf("ncpu: bad value %q", strings.TrimSpace(n))
	}
	return s.cpuTable().Observe(load / float64(cores))
}

const stepdownKey = "stepdown"

// measureRAM returns the ram observation: the worst of the swap level and the
// pressure level, lowered one step while the step-down is engaged.
func (s *Sensor) measureRAM(ctx context.Context, env sensorcli.Env, free float64, haveDisk bool) (levels.Observation, bool) {
	scale := levels.FourStep
	swapT := s.swapTable()
	swapIdx, pressIdx := -1, -1
	var swap float64
	haveSwap := false
	if out, err := s.sysctl(ctx, SysctlSwap); err != nil {
		fmt.Fprintf(env.Stderr, "host-sensor: ram swap: %v\n", err)
	} else if v, err := ParseSwapUsedGiB(out); err != nil {
		fmt.Fprintf(env.Stderr, "host-sensor: ram swap: %v\n", err)
	} else if lvl, err := swapT.Classify(v); err != nil {
		fmt.Fprintf(env.Stderr, "host-sensor: ram swap: %v\n", err)
	} else {
		swap, haveSwap, swapIdx = v, true, scale.Index(lvl)
	}
	if out, err := s.sysctl(ctx, SysctlPressure); err != nil {
		fmt.Fprintf(env.Stderr, "host-sensor: ram pressure: %v\n", err)
	} else if lvl, err := ParsePressure(out); err != nil {
		fmt.Fprintf(env.Stderr, "host-sensor: ram pressure: %v\n", err)
	} else {
		pressIdx = scale.Index(lvl)
	}
	if !haveSwap && pressIdx < 0 {
		return levels.Observation{}, false
	}
	worst := len(scale) - 1
	for _, i := range []int{swapIdx, pressIdx} {
		if i >= 0 && i < worst {
			worst = i
		}
	}
	// The step-down rule needs the swap reading; with hysteresis of its own.
	engaged := env.Store.Extra(ResRAM)[stepdownKey] == "1"
	if haveSwap {
		switch {
		case engaged:
			if swap <= s.T.StepSwap*0.8 || (haveDisk && free >= s.T.StepDisk*1.25) {
				engaged = false
			}
		default:
			if swap >= s.T.StepSwap && haveDisk && free < s.T.StepDisk {
				engaged = true
			}
		}
	}
	if !env.Raw {
		ex := env.Store.Extra(ResRAM)
		if engaged {
			ex[stepdownKey] = "1"
		} else {
			delete(ex, stepdownKey)
		}
		env.Store.SetExtra(ResRAM, ex)
	}
	eff := worst
	if engaged && eff > 0 {
		eff--
	}
	obs := levels.Observation{
		Resource: ResRAM,
		Scale:    scale,
		Level:    scale[eff],
		CanRise: func(current string) bool {
			i := scale.Index(current)
			if i < 0 {
				return false
			}
			// Pressure carries no margin; the swap reading must clear its
			// boundary with margin.
			return !haveSwap || swapT.CanRise(current, swap)
		},
	}
	if haveSwap {
		// With only the pressure readable there is no numeric reading.
		obs.Reading = &levels.Reading{Value: swap, Unit: "GiB"}
	}
	return obs, true
}
