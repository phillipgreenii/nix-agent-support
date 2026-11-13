package main

import (
	"encoding/binary"
	"os"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-router/internal/config"
)

// load1 reports the host's 1-minute load average, for loadBackoff and the
// producer-tick "source failed" log line: a source killed by its timeout on
// a loaded host reads very differently from one killed on an idle host. ok is
// false when the value cannot be read (never an error: this is diagnostic
// context, so it must not itself fail or block). A package var so a test can
// pin the value.
var load1 = readLoad1

// readLoad1 reads /proc/loadavg (Linux) and falls back to the in-process
// vm.loadavg sysctl (macOS, see loadavg_darwin.go). Neither launches a
// process: this runs on every tick via loadBackoff, and every process launch
// is inspected by endpoint security software.
func readLoad1() (float64, bool) {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if v, ok := parseProcLoadavg(string(b)); ok {
			return v, true
		}
	}
	return sysctlLoad1()
}

// kernLoadavgSize is the size of the kernel's struct loadavg: 3 x uint32
// ldavg, 4 bytes of padding, int64 fscale.
const kernLoadavgSize = 24

// parseKernLoadavg decodes the raw vm.loadavg sysctl value (native-endian
// struct loadavg) and returns load1 = ldavg[0] / fscale. ok is false for a
// short buffer or a non-positive fscale.
func parseKernLoadavg(b []byte) (float64, bool) {
	if len(b) < kernLoadavgSize {
		return 0, false
	}
	ldavg0 := binary.NativeEndian.Uint32(b[0:4])
	fscale := int64(binary.NativeEndian.Uint64(b[16:24]))
	if fscale <= 0 {
		return 0, false
	}
	return float64(ldavg0) / float64(fscale), true
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

// loadBackoff reports whether the producer pass should be skipped this tick:
// the host load1 is above cfg.LoadBackoffThreshold. A threshold of 0 or less
// disables it, and an unreadable load never backs off (fail open, so a missing
// sysctl cannot silence every source). The load comes from the load1 package var.
func loadBackoff(cfg config.Config) (float64, bool) {
	if cfg.LoadBackoffThreshold <= 0 {
		return 0, false
	}
	l, ok := load1()
	if !ok {
		return 0, false
	}
	return l, l > cfg.LoadBackoffThreshold
}
