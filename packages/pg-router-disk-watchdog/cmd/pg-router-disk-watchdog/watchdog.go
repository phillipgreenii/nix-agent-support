package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Exit codes: 0 the check ran and the gate is now in the right state; 1 the
// check could not be completed (a path could not be measured, or pg-router
// could not be reached) and the gate was NOT changed; 2 usage error.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// config is one watchdog invocation's fully resolved parameters.
type config struct {
	// paths are measured with statfs; the LOWEST free space wins. Several paths
	// on one APFS container read (nearly) the same pool, so listing both / and
	// a data volume costs nothing and survives a layout change.
	paths []string
	// minFree: free space strictly below this sets the gate.
	minFree uint64
	// recoverFree: the gate is only cleared once free space is at or above
	// this. Greater than minFree gives hysteresis, so a disk hovering around
	// the threshold does not flap the gate (and drop events) every 5 minutes.
	recoverFree uint64
	gateType    string
	// owner is recorded on the gate (debug only in pg-router) and is how this
	// watchdog recognises its OWN gate, so a LOW_DISK_USAGE an operator set by
	// hand (a different owner) is never cleared out from under them.
	owner string
	// ttl is the lease on a set gate. It MUST outlast the check cadence so the
	// gate holds between checks, and is short enough that a dead watchdog does
	// not leave the pool gated forever.
	ttl          time.Duration
	pgRouterPath string
	execTimeout  time.Duration
}

// deps are the watchdog's side effects, injected so tests need neither a real
// disk nor a real pg-router.
type deps struct {
	// freeBytes reports the space available to unprivileged writers at path.
	freeBytes func(path string) (uint64, error)
	// run executes bin with args and returns its stdout; a non-zero exit is an
	// error carrying the child's stderr.
	run    func(ctx context.Context, bin string, args ...string) ([]byte, error)
	stdout io.Writer
	stderr io.Writer
}

// gateEntry is one element of `pg-router gate list --json`.
type gateEntry struct {
	Type  string `json:"type"`
	Owner string `json:"owner"`
}

// check performs ONE watchdog pass:
//
//	free <  minFree                -> set the gate (renewing its lease if held)
//	free >= recoverFree            -> clear the gate if THIS watchdog owns it
//	minFree <= free < recoverFree  -> hold: renew our gate if we own one, else nothing
//
// While low the gate is re-set on every pass: that is the lease renewal.
func check(ctx context.Context, cfg config, d deps) int {
	free, where, err := lowestFree(cfg.paths, d.freeBytes)
	if err != nil {
		fmt.Fprintln(d.stderr, "pg-router-disk-watchdog:", err)
		return exitError
	}

	if free < cfg.minFree {
		desc := fmt.Sprintf("low disk space: %s free on %s (minimum %s)", formatSize(free), where, formatSize(cfg.minFree))
		if err := setGate(ctx, cfg, d, desc); err != nil {
			fmt.Fprintln(d.stderr, "pg-router-disk-watchdog:", err)
			return exitError
		}
		fmt.Fprintf(d.stdout, "pg-router-disk-watchdog: %s -> %s set\n", desc, cfg.gateType)
		return exitOK
	}

	// Space is adequate. Whether there is anything to do depends on whether the
	// gate is ours, so ask pg-router; this is also what keeps a healthy pass
	// from writing a "clear" record to the event log every 5 minutes.
	ours, other, err := ownGate(ctx, cfg, d)
	if err != nil {
		fmt.Fprintln(d.stderr, "pg-router-disk-watchdog:", err)
		return exitError
	}
	switch {
	case ours && free >= cfg.recoverFree:
		if _, err := pgr(ctx, cfg, d, "gate", "clear", cfg.gateType, "--by", cfg.owner); err != nil {
			fmt.Fprintln(d.stderr, "pg-router-disk-watchdog: gate clear:", err)
			return exitError
		}
		fmt.Fprintf(d.stdout, "pg-router-disk-watchdog: %s free on %s (recover at %s) -> %s cleared\n", formatSize(free), where, formatSize(cfg.recoverFree), cfg.gateType)
	case ours:
		desc := fmt.Sprintf("low disk space: %s free on %s (recovering; clears at %s)", formatSize(free), where, formatSize(cfg.recoverFree))
		if err := setGate(ctx, cfg, d, desc); err != nil {
			fmt.Fprintln(d.stderr, "pg-router-disk-watchdog:", err)
			return exitError
		}
		fmt.Fprintf(d.stdout, "pg-router-disk-watchdog: %s -> %s held\n", desc, cfg.gateType)
	case other:
		fmt.Fprintf(d.stdout, "pg-router-disk-watchdog: %s free on %s; %s is held by another owner, leaving it\n", formatSize(free), where, cfg.gateType)
	default:
		fmt.Fprintf(d.stdout, "pg-router-disk-watchdog: %s free on %s; ok\n", formatSize(free), where)
	}
	return exitOK
}

// lowestFree measures every path and returns the smallest free-byte count and
// the path it came from. Any unmeasurable path fails the whole pass: an
// unreadable disk must not be reported as a healthy one.
func lowestFree(paths []string, freeBytes func(string) (uint64, error)) (uint64, string, error) {
	var (
		floor uint64
		where string
	)
	for i, p := range paths {
		f, err := freeBytes(p)
		if err != nil {
			return 0, "", fmt.Errorf("measure %s: %w", p, err)
		}
		if i == 0 || f < floor {
			floor, where = f, p
		}
	}
	return floor, where, nil
}

func setGate(ctx context.Context, cfg config, d deps, description string) error {
	_, err := pgr(ctx, cfg, d, "gate", "set", cfg.gateType,
		"--description", description, "--owner", cfg.owner, "--ttl", cfg.ttl.String())
	if err != nil {
		return fmt.Errorf("gate set: %w", err)
	}
	return nil
}

// ownGate reports whether the gate is active and owned by this watchdog
// (ours), or active and owned by someone else (other).
func ownGate(ctx context.Context, cfg config, d deps) (ours, other bool, err error) {
	out, err := pgr(ctx, cfg, d, "gate", "list", "--json")
	if err != nil {
		return false, false, fmt.Errorf("gate list: %w", err)
	}
	var gates []gateEntry
	if err := json.Unmarshal(out, &gates); err != nil {
		return false, false, fmt.Errorf("gate list: parse json: %w", err)
	}
	for _, g := range gates {
		if g.Type != cfg.gateType {
			continue
		}
		if g.Owner == cfg.owner {
			return true, false, nil
		}
		return false, true, nil
	}
	return false, false, nil
}

// pgr runs one pg-router CLI call under the configured per-call timeout, so a
// wedged socket cannot hang the dispatch (and the role) indefinitely.
func pgr(ctx context.Context, cfg config, d deps, args ...string) ([]byte, error) {
	if cfg.execTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.execTimeout)
		defer cancel()
	}
	return d.run(ctx, cfg.pgRouterPath, args...)
}
