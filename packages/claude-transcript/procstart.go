package claudetranscript

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// procStartLayout is the layout of the registry's procStart field and of
// `ps -o lstart=` output ("Wed Oct 14 07:51:50 2026"): ctime-style, second
// resolution, no zone. `_2` accepts the space-padded day ps emits for days 1-9.
const procStartLayout = "Mon Jan _2 15:04:05 2006"

// procStartTolerance bounds how far the registry procStart may differ from the
// live process start time before the row is rejected as a reused PID.
//
// Evidence (2026-10-14, 15 of 15 live registry files, Claude Code 2.1.296):
// procStart equals `TZ=UTC ps -o lstart= -p <pid>` EXACTLY (same ctime-style
// string, UTC even though the machine is EDT). Both are second-resolution
// renderings of the same kernel p_starttime, so the true difference is 0; the
// tolerance only absorbs a second-boundary rounding difference. A PID-reusing
// process starts after the original exited, which is far more than 2s later in
// practice (the registry file lingers for minutes to days), so 2s does not
// admit a realistic reuse.
const procStartTolerance = 2 * time.Second

// parseProcStart parses a registry procStart (or ps lstart) value as UTC. The
// zero time and false are returned for an empty or unparseable value.
func parseProcStart(s string) (time.Time, bool) {
	s = strings.Join(strings.Fields(s), " ") // collapse ps's day padding
	if s == "" {
		return time.Time{}, false
	}
	// Collapsing the padding turns "Oct  4" into "Oct 4"; _2 accepts both.
	t, err := time.ParseInLocation(procStartLayout, s, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ProcessStartTime returns the start time of the live process pid as reported
// by the OS (`ps -o lstart=`, forced to UTC and the C locale so it is
// comparable with the registry's procStart). An error means the process is gone
// or the start time could not be read.
func ProcessStartTime(pid int) (time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(cmd.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, err
	}
	t, ok := parseProcStart(string(out))
	if !ok {
		return time.Time{}, errUnparseableLstart
	}
	return t, nil
}

var errUnparseableLstart = errors.New("unparseable ps lstart output")

// ProcStartMatches reports whether the registry row's recorded procStart is
// consistent with the start time of the live process at reg.PID. It is the
// PID-reuse guard: a dead Claude session whose PID the OS handed to an
// unrelated process fails it.
//
// It FAILS OPEN: it returns true when the row has no (or an unparseable)
// procStart (older registries), or when the live start time cannot be read.
// Only a positive mismatch returns false. It does not check that the PID is
// alive; combine with PidAlive (see SessionAlive).
func ProcStartMatches(reg RegistrySession) bool {
	return procStartMatches(reg, ProcessStartTime)
}

// ProcStartMatchesWith is ProcStartMatches with the live-start-time reader
// injected, so a caller's tests do not depend on a real `ps` being runnable (it
// is absent or blocked inside the nix build sandbox, which makes the real reader
// fail open). A nil startTime uses ProcessStartTime.
func ProcStartMatchesWith(reg RegistrySession, startTime func(int) (time.Time, error)) bool {
	if startTime == nil {
		startTime = ProcessStartTime
	}
	return procStartMatches(reg, startTime)
}

func procStartMatches(reg RegistrySession, startTime func(int) (time.Time, error)) bool {
	if reg.ProcStart.IsZero() {
		return true
	}
	live, err := startTime(reg.PID)
	if err != nil {
		return true
	}
	d := live.Sub(reg.ProcStart)
	if d < 0 {
		d = -d
	}
	return d <= procStartTolerance
}

// SessionAlive reports whether the registry row still describes a live Claude
// process: its PID is alive (PidAlive) AND, when the row records procStart, the
// live process started when the row says (ProcStartMatches). Prefer this over
// PidAlive(reg.PID) so a recycled PID does not keep a dead session alive.
func SessionAlive(reg RegistrySession) bool {
	return PidAlive(reg.PID) && ProcStartMatches(reg)
}
