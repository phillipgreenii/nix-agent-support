package caffeinate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Owner is the lease owner pa-monitor uses with pg-keep-awake. One owner, one
// lease (the CLI's default key), so acquire and release are idempotent.
const Owner = "pa-monitor"

// KeeperBinary is the executable name looked up on PATH when no absolute path
// is configured.
const KeeperBinary = "pg-keep-awake"

// Exit codes of `pg-keep-awake status --check`.
const (
	exitKeeperDown = 5
	exitDegraded   = 6
)

// DefaultCallTimeout bounds one pg-keep-awake invocation. Every command only
// reads or writes small files, so a call that takes this long is stuck.
const DefaultCallTimeout = 10 * time.Second

// ErrKeeperNotFound means no pg-keep-awake executable could be located.
var ErrKeeperNotFound = errors.New("pg-keep-awake executable not found")

// ErrKeeperDegraded is the error behind StateError when `status --check` says
// the keeper is running but leases are live and nothing is held.
var ErrKeeperDegraded = errors.New("pg-keep-awake is degraded: live leases but no assertion held")

// Health is the verdict of `pg-keep-awake status --check`.
type Health int

const (
	// HealthUnknown: the check could not be run or exited with another code.
	HealthUnknown Health = iota
	// HealthOK: exit 0, the keeper is healthy.
	HealthOK
	// HealthDown: exit 5, the keeper is not running.
	HealthDown
	// HealthDegraded: exit 6, the keeper runs but is not holding for live leases.
	HealthDegraded
)

// Up reports whether the keeper process is running: healthy or degraded.
func (h Health) Up() bool { return h == HealthOK || h == HealthDegraded }

// Observation is one reading of the keeper: the held state and battery gate
// from `status --json`, and the health from the exit code of `--check`.
type Observation struct {
	Health Health
	// StatusKnown is true when status --json was read and parsed. Held, Gate,
	// OnBattery, Percent and BatteryReadOK mean nothing otherwise.
	StatusKnown   bool
	Held          bool
	Gate          BatteryGate
	OnBattery     bool
	Percent       int
	BatteryReadOK bool
	// Err is why the status could not be read (the call failed or its output
	// did not parse); nil when StatusKnown.
	Err error
}

// Keeper is the pg-keep-awake client the Manager drives. CLI is the real one.
type Keeper interface {
	// Acquire creates or refreshes pa-monitor's lease, bound to pid.
	Acquire(pid int) error
	// Release removes pa-monitor's lease. A missing lease is not an error.
	Release() error
	// Observe reads the keeper's status and health in one call.
	Observe() Observation
}

// CLI is a Keeper that runs the pg-keep-awake command line.
//
// The executable is found by Locate each call, so a binary installed after the
// daemon started is picked up and a missing one surfaces as a visible error
// (process=error) rather than a silent no-op.
type CLI struct {
	// Configured is the absolute path to pg-keep-awake from configuration. When
	// empty the executable is looked up on PATH, which is NOT reliable under
	// launchd.
	Configured string
	// Timeout bounds one invocation; zero means DefaultCallTimeout.
	Timeout time.Duration
}

// Locate resolves the pg-keep-awake executable. A configured path MUST be
// absolute and name an executable regular file; it is never searched on PATH,
// so the lookup does not depend on a login shell's PATH. With no configured
// path it falls back to PATH.
func Locate(configured string) (string, error) {
	if configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("%w: keep_awake_bin %q is not an absolute path", ErrKeeperNotFound, configured)
		}
		fi, err := os.Stat(configured)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrKeeperNotFound, err)
		}
		if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("%w: %s is not an executable file", ErrKeeperNotFound, configured)
		}
		return configured, nil
	}
	p, err := exec.LookPath(KeeperBinary)
	if err != nil {
		return "", fmt.Errorf("%w: %s is not on PATH and keep_awake_bin is not set", ErrKeeperNotFound, KeeperBinary)
	}
	return p, nil
}

func (c *CLI) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultCallTimeout
}

// run executes pg-keep-awake with args and returns stdout, the exit code (-1
// when the process could not run or was killed) and stderr.
func (c *CLI) run(args ...string) (stdout []byte, exit int, stderr string, err error) {
	path, err := Locate(c.Configured)
	if err != nil {
		return nil, -1, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	stderr = strings.TrimSpace(errb.String())
	if runErr == nil {
		return out.Bytes(), 0, stderr, nil
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) && ctx.Err() == nil {
		return out.Bytes(), ee.ExitCode(), stderr, nil
	}
	if ctx.Err() != nil {
		return out.Bytes(), -1, stderr, fmt.Errorf("pg-keep-awake %s: timed out after %s", args[0], c.timeout())
	}
	return out.Bytes(), -1, stderr, fmt.Errorf("pg-keep-awake %s: %w", args[0], runErr)
}

// call runs a state-changing command and turns a nonzero exit into an error.
func (c *CLI) call(args ...string) error {
	_, exit, stderr, err := c.run(args...)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("pg-keep-awake %s: exit %d: %s", args[0], exit, stderr)
	}
	return nil
}

// Acquire runs `pg-keep-awake acquire --owner pa-monitor --pid <pid>`.
func (c *CLI) Acquire(pid int) error {
	return c.call("acquire", "--owner", Owner, "--pid", strconv.Itoa(pid))
}

// Release runs `pg-keep-awake release --owner pa-monitor`.
func (c *CLI) Release() error {
	return c.call("release", "--owner", Owner)
}

// statusJSON is the part of `status --json` pa-monitor reads; the CLI prints
// more, and unknown fields are ignored.
type statusJSON struct {
	Assertion struct {
		Held bool `json:"held"`
	} `json:"assertion"`
	Battery struct {
		Percent   int    `json:"percent"`
		OnBattery bool   `json:"on_battery"`
		Gate      string `json:"gate"`
		ReadOK    bool   `json:"read_ok"`
	} `json:"battery"`
}

// Observe runs `pg-keep-awake status --json --check` once: stdout carries the
// status object and the exit code carries the health, so both come from one
// process and cannot disagree.
func (c *CLI) Observe() Observation {
	out, exit, stderr, err := c.run("status", "--json", "--check")
	if err != nil {
		return Observation{Health: HealthUnknown, Err: err}
	}
	obs := Observation{}
	switch exit {
	case 0:
		obs.Health = HealthOK
	case exitKeeperDown:
		obs.Health = HealthDown
	case exitDegraded:
		obs.Health = HealthDegraded
	default:
		obs.Health = HealthUnknown
		obs.Err = fmt.Errorf("pg-keep-awake status: exit %d: %s", exit, stderr)
		return obs
	}
	var s statusJSON
	if err := json.Unmarshal(out, &s); err != nil {
		obs.Err = fmt.Errorf("pg-keep-awake status --json: %w", err)
		return obs
	}
	obs.StatusKnown = true
	obs.Held = s.Assertion.Held
	obs.Gate = ParseBatteryGate(s.Battery.Gate)
	obs.OnBattery = s.Battery.OnBattery
	obs.Percent = s.Battery.Percent
	obs.BatteryReadOK = s.Battery.ReadOK
	return obs
}
