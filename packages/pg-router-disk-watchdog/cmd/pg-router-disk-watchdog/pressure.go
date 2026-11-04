package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Recovery thresholds for the load and swap rules sit at this fraction of the
// trip thresholds (and the swap rule's disk gate at its inverse), so a host
// hovering at a limit does not flap the gate.
const (
	loadRecoverFactor = 0.8
	swapRecoverFactor = 0.8
)

// readings is one pass's measurements. A reading that could not be taken is
// simply absent (have* false): the load and swap rules skip it, because a
// pressure guard must not itself stop dispatching on a failed probe. Free disk
// space is different: it is mandatory, and an unmeasurable path fails the pass.
type readings struct {
	free     uint64
	where    string
	load1    float64
	haveLoad bool
	swapUsed uint64
	haveSwap bool
}

// pressure evaluates the three rules against r. With recovering false it uses
// the trip thresholds; with recovering true it uses the (more generous)
// recovery thresholds, which decide whether a gate this watchdog already holds
// may be cleared. It returns one human-readable reason per rule that holds.
//
//	disk: free <  minFree (recover: recoverFree)
//	load: load1 / cores > maxLoadPerCore (recover: * 0.8); disabled when maxLoadPerCore is 0
//	swap: swapUsed > swapMax AND free < swapDiskFree (recover: swapMax * 0.8 AND swapDiskFree * 1.25); disabled when swapMax is 0
func pressure(cfg config, r readings, recovering bool) []string {
	var out []string

	diskLimit := cfg.minFree
	if recovering {
		diskLimit = cfg.recoverFree
	}
	if r.free < diskLimit {
		if recovering {
			out = append(out, fmt.Sprintf("low disk space: %s free on %s (recovering; clears at %s)", formatSize(r.free), r.where, formatSize(cfg.recoverFree)))
		} else {
			out = append(out, fmt.Sprintf("low disk space: %s free on %s (minimum %s)", formatSize(r.free), r.where, formatSize(cfg.minFree)))
		}
	}

	if cfg.maxLoadPerCore > 0 && r.haveLoad {
		cores := float64(max(cfg.cores, 1))
		limit := cfg.maxLoadPerCore
		if recovering {
			limit *= loadRecoverFactor
		}
		if per := r.load1 / cores; per > limit {
			out = append(out, fmt.Sprintf("high load: 1-minute load %.1f on %d cores is %.1f per core (limit %.1f)", r.load1, max(cfg.cores, 1), per, limit))
		}
	}

	if cfg.swapMax > 0 && r.haveSwap {
		swapLimit, diskLimit := cfg.swapMax, cfg.swapDiskFree
		if recovering {
			swapLimit = uint64(float64(swapLimit) * swapRecoverFactor)
			diskLimit += diskLimit / 4
		}
		if r.swapUsed > swapLimit && r.free < diskLimit {
			out = append(out, fmt.Sprintf("swap pressure: %s swap in use (limit %s) with only %s free on %s (below %s)",
				formatSize(r.swapUsed), formatSize(swapLimit), formatSize(r.free), r.where, formatSize(diskLimit)))
		}
	}
	return out
}

// readPressure takes the load and swap readings through d, leaving them absent
// (and noting why on stderr) when a probe fails or is not wired.
func readPressure(d deps, r *readings) {
	if d.loadAvg1 != nil {
		if v, err := d.loadAvg1(); err != nil {
			fmt.Fprintln(d.stderr, "pg-router-disk-watchdog: load probe skipped:", err)
		} else {
			r.load1, r.haveLoad = v, true
		}
	}
	if d.swapUsed != nil {
		if v, err := d.swapUsed(); err != nil {
			fmt.Fprintln(d.stderr, "pg-router-disk-watchdog: swap probe skipped:", err)
		} else {
			r.swapUsed, r.haveSwap = v, true
		}
	}
}

var (
	loadavgRe  = regexp.MustCompile(`^\s*\{?\s*([0-9]+(?:\.[0-9]+)?)`)
	swapUsedRe = regexp.MustCompile(`used\s*=\s*([0-9.]+)([KMGT])`)
)

// parseLoadAvg1 reads the 1-minute figure from `sysctl -n vm.loadavg`
// ("{ 2.46 2.36 2.31 }").
func parseLoadAvg1(s string) (float64, error) {
	m := loadavgRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("unrecognised vm.loadavg %q", strings.TrimSpace(s))
	}
	return strconv.ParseFloat(m[1], 64)
}

// parseSwapUsed reads the used figure from `sysctl -n vm.swapusage`
// ("total = 2048.00M  used = 1024.50M  free = 1023.50M  (encrypted)").
func parseSwapUsed(s string) (uint64, error) {
	m := swapUsedRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("unrecognised vm.swapusage %q", strings.TrimSpace(s))
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	mult := map[string]float64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40}[m[2]]
	return uint64(f * mult), nil
}

// sysctlProbe runs `sysctl -n name` bounded by a short timeout, so a thrashing
// host cannot hang the check that exists to protect it.
func sysctlProbe(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysctl", "-n", name).Output()
	if err != nil {
		return "", fmt.Errorf("sysctl %s: %w", name, err)
	}
	return string(out), nil
}

func sysctlLoadAvg1() (float64, error) {
	s, err := sysctlProbe("vm.loadavg")
	if err != nil {
		return 0, err
	}
	return parseLoadAvg1(s)
}

func sysctlSwapUsed() (uint64, error) {
	s, err := sysctlProbe("vm.swapusage")
	if err != nil {
		return 0, err
	}
	return parseSwapUsed(s)
}

func numCPU() int { return runtime.NumCPU() }
