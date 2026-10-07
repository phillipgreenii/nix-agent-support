package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// load1 reports the host's 1-minute load average, for the producer-tick
// "source failed" log line (bead pg2-zdowv): a source killed by its timeout on
// a loaded host reads very differently from one killed on an idle host. ok is
// false when the value cannot be read (never an error: this is diagnostic
// context on a failure path, so it must not itself fail or block). A package
// var so a test can pin the value.
var load1 = readLoad1

// readLoad1 reads /proc/loadavg (Linux) and falls back to `sysctl -n
// vm.loadavg` (macOS/BSD), which needs no cgo and is only reached on the rare
// source-failure path.
func readLoad1() (float64, bool) {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if v, ok := parseProcLoadavg(string(b)); ok {
			return v, true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return 0, false
	}
	return parseSysctlLoadavg(string(out))
}

// parseProcLoadavg parses "0.52 0.58 0.59 1/467 12345" (first field = load1).
func parseProcLoadavg(s string) (float64, bool) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

// parseSysctlLoadavg parses "{ 1.23 1.45 1.50 }" (first number = load1).
func parseSysctlLoadavg(s string) (float64, bool) {
	f := strings.Fields(strings.NewReplacer("{", " ", "}", " ").Replace(s))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}
