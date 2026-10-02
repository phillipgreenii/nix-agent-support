// pg-router-disk-watchdog is the external disk-space listener for pg-router
// (bead pg2-zwdwf, gate design pg2-h63eu). pg-router has no disk-space logic of
// its own: gates are generic TYPE-keyed records, and this binary is an ordinary
// pg-router role bound to the timer emitter that sets LOW_DISK_USAGE when free
// space is low and clears it on recovery, by shelling out to `pg-router gate`.
//
// The role MUST be registered with non_blocking_gates = ["LOW_DISK_USAGE"] (and
// is driven by a type = "timer" query, which no gate ever blocks), otherwise the
// gate it sets would stop it from ever running again to clear it.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Version is injected at build time by mkGoApp.
var Version = "dev"

// Defaults. defaultMinFree is a judgement call, recorded in bead pg2-zwdwf: the
// ZR worker pool checks out multi-GB worktrees and runs nix builds, so below
// 20 GiB a single dispatch can plausibly fill the disk; 20 GiB is also small
// enough not to trip on a healthy 1 TB laptop. defaultTTL is the 5 minute check
// cadence plus a 2 minute margin, so one missed or slow check does not drop the
// gate. Recovery needs min * 5/4 (25 GiB by default) to avoid flapping.
const (
	defaultMinFree = "20GiB"
	defaultGate    = "LOW_DISK_USAGE"
	defaultOwner   = "pg-router-disk-watchdog"
	defaultTTL     = 7 * time.Minute
)

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, realDeps))
}

func realDeps(stdout, stderr io.Writer) deps {
	return deps{freeBytes: statfsFree, run: execRun, stdout: stdout, stderr: stderr}
}

func run(args []string, stdout, stderr io.Writer, mkDeps func(io.Writer, io.Writer) deps) int {
	fs := flag.NewFlagSet("pg-router-disk-watchdog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var paths multiFlag
	fs.Var(&paths, "path", "path whose filesystem is measured; repeatable, the lowest free space wins (default \"/\")")
	minFree := fs.String("min-free", defaultMinFree, "set the gate when free space is below this (e.g. 20GiB, 512MiB; IEC units or bytes)")
	recoverFree := fs.String("recover-free", "", "clear the gate only at or above this free space (default: --min-free * 1.25)")
	gateType := fs.String("gate", defaultGate, "gate TYPE to set and clear")
	owner := fs.String("owner", defaultOwner, "owner recorded on the gate; only a gate with this owner is ever cleared")
	ttl := fs.Duration("ttl", defaultTTL, "lease on the gate; must be longer than the check interval")
	pgRouter := fs.String("pg-router-path", "pg-router", "pg-router binary used for `gate set|clear|list`")
	execTimeout := fs.Duration("exec-timeout", 30*time.Second, "timeout for each pg-router call")
	version := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if *version {
		fmt.Fprintln(stdout, Version)
		return exitOK
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pg-router-disk-watchdog: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}

	cfg, err := buildConfig(paths, *minFree, *recoverFree, *gateType, *owner, *ttl, *pgRouter, *execTimeout)
	if err != nil {
		fmt.Fprintln(stderr, "pg-router-disk-watchdog:", err)
		return exitUsage
	}
	return check(context.Background(), cfg, mkDeps(stdout, stderr))
}

func buildConfig(paths []string, minFree, recoverFree, gateType, owner string, ttl time.Duration, pgRouter string, execTimeout time.Duration) (config, error) {
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	floor, err := parseSize(minFree)
	if err != nil {
		return config{}, fmt.Errorf("--min-free: %w", err)
	}
	if floor == 0 {
		return config{}, errors.New("--min-free must be greater than zero")
	}
	rec := floor + floor/4
	if recoverFree != "" {
		if rec, err = parseSize(recoverFree); err != nil {
			return config{}, fmt.Errorf("--recover-free: %w", err)
		}
		if rec < floor {
			return config{}, errors.New("--recover-free must not be below --min-free")
		}
	}
	if gateType == "" || strings.ToUpper(gateType) != gateType {
		return config{}, fmt.Errorf("--gate %q must be an ALL-CAPS gate TYPE", gateType)
	}
	if owner == "" {
		return config{}, errors.New("--owner must not be empty")
	}
	if ttl <= 0 {
		return config{}, errors.New("--ttl must be positive")
	}
	return config{
		paths: paths, minFree: floor, recoverFree: rec, gateType: gateType, owner: owner,
		ttl: ttl, pgRouterPath: pgRouter, execTimeout: execTimeout,
	}, nil
}

// statfsFree is the space available to unprivileged writers on path's
// filesystem (f_bavail * f_bsize). On an APFS volume this is the shared
// container's free pool, which is what a build on / or on a data volume
// actually competes for.
func statfsFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// execRun runs bin with args under ctx (bounded by the caller) and returns stdout; a
// failure carries the child's stderr, since pg-router's diagnostics
// ("no running core", a refused verb) are the only useful signal.
func execRun(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}
