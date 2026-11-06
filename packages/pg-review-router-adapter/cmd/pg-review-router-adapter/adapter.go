package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	exitUsage = 2
	// exitBusyDecline is the code the router reads as "busy, decline": the
	// adapter never produces it.
	exitBusyDecline = 9
	// pgReview is looked up on PATH at run time.
	pgReview = "pg-review"
)

// parseWindow parses the window grammar: a positive integer followed by "h"
// or "d". A "d" is 24 elapsed hours (see the README: elapsed time, not
// calendar days), so the result never depends on a time zone database.
func parseWindow(w string) (time.Duration, error) {
	if len(w) < 2 {
		return 0, fmt.Errorf("window %q: want <positive integer>h or <positive integer>d", w)
	}
	unit := time.Hour
	switch w[len(w)-1] {
	case 'h':
	case 'd':
		unit = 24 * time.Hour
	default:
		return 0, fmt.Errorf("window %q: want <positive integer>h or <positive integer>d", w)
	}
	digits := w[:len(w)-1]
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("window %q: want <positive integer>h or <positive integer>d", w)
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("window %q: want a positive integer count", w)
	}
	// 10 years of hours bounds the product well inside int64 nanoseconds.
	const maxHours = 24 * 366 * 10
	if n > maxHours/int64(unit/time.Hour) {
		return 0, fmt.Errorf("window %q: too large", w)
	}
	return time.Duration(n) * unit, nil
}

// runArgs is a validated `run` dispatch.
type runArgs struct {
	to, from   time.Time
	toRaw      string // passed verbatim to --to (offset kept)
	scheduleID string
	lens       string
	scope      string
	window     string
}

// runKey is <lens>/<scope>/<window>/<window end as UTC RFC 3339, seconds, Z>.
func (a runArgs) runKey() string {
	return a.lens + "/" + a.scope + "/" + a.window + "/" + a.to.UTC().Format("2006-01-02T15:04:05Z")
}

func parseRun(args []string) (runArgs, error) {
	if len(args) != 5 {
		return runArgs{}, fmt.Errorf("run takes 5 arguments, got %d", len(args))
	}
	a := runArgs{toRaw: args[0], scheduleID: args[1], lens: args[2], scope: args[3], window: args[4]}
	for name, v := range map[string]string{"schedule_id": a.scheduleID, "lens": a.lens, "scope": a.scope} {
		if v == "" || strings.HasPrefix(v, "-") {
			return runArgs{}, fmt.Errorf("%s %q: must be non-empty and not start with '-'", name, v)
		}
	}
	to, err := time.Parse(time.RFC3339, a.toRaw)
	if err != nil {
		return runArgs{}, fmt.Errorf("item timestamp %q: %w", a.toRaw, err)
	}
	d, err := parseWindow(a.window)
	if err != nil {
		return runArgs{}, err
	}
	// time.Parse adopts the process's local zone when the offset matches it,
	// which would make `from` shift with DST and the TZ variable. Pin the
	// parsed offset instead so the arithmetic is pure elapsed time.
	_, off := to.Zone()
	to = to.In(time.FixedZone("", off))
	a.to = to
	a.from = to.Add(-d) // keeps to's offset, so from is printed in the same offset
	return a, nil
}

func cmdRun(args []string, stdout, stderr io.Writer, sigs <-chan os.Signal) int {
	a, err := parseRun(args)
	if err != nil {
		fmt.Fprintf(stderr, "pg-review-router-adapter: %v\n%s\n", err, usage)
		return exitUsage
	}
	// First line of the log: the computed window.
	fmt.Fprintf(stderr, "pg-review-router-adapter: run window from=%s to=%s schedule_id=%s run_key=%s\n",
		a.from.Format(time.RFC3339), a.to.Format(time.RFC3339), a.scheduleID, a.runKey())
	return runChild([]string{
		"job", "run", a.lens,
		"--scope", a.scope,
		"--from", a.from.Format(time.RFC3339),
		"--to", a.toRaw,
		"--run-key", a.runKey(),
		"--schedule-id", a.scheduleID,
	}, stdout, stderr, sigs)
}

func cmdMaintain(args []string, stdout, stderr io.Writer, sigs <-chan os.Signal) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "pg-review-router-adapter: maintain takes no arguments\n%s\n", usage)
		return exitUsage
	}
	return runChild([]string{"job", "maintain"}, stdout, stderr, sigs)
}

// mapExit maps a pg-review exit code to the router's semantics. reason is
// non-empty when the mapping hides a non-zero code, so the caller logs it.
func mapExit(code int) (mapped int, reason string) {
	switch code {
	case 0:
		return 0, ""
	case 3:
		return 0, "skipped or already done"
	case 5:
		return 0, "run lock busy"
	case exitBusyDecline:
		// 9 would read as a busy decline to the router.
		return 1, "pg-review exit 9 is never passed on"
	default:
		return code, ""
	}
}

// runChild runs pg-review as a child (it must map the exit code, so no exec),
// waits for it, and returns the mapped code. A SIGTERM arriving on sigs is
// only noted: pg-review receives the router's process-group SIGTERM itself, so
// the adapter forwards nothing and just keeps waiting.
func runChild(argv []string, stdout, stderr io.Writer, sigs <-chan os.Signal) int {
	path, err := exec.LookPath(pgReview)
	if err != nil {
		fmt.Fprintf(stderr, "pg-review-router-adapter: %v\n", err)
		return 1
	}
	cmd := exec.Command(path, argv...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "pg-review-router-adapter: start %s: %v\n", pgReview, err)
		return 1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
wait:
	for {
		select {
		case waitErr = <-done:
			break wait
		case s := <-sigs:
			fmt.Fprintf(stderr, "pg-review-router-adapter: received %v, waiting for %s to exit (no signal forwarded)\n", s, pgReview)
		}
	}
	code := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			code = ee.ExitCode() // -1 when killed by a signal
		} else {
			fmt.Fprintf(stderr, "pg-review-router-adapter: wait: %v\n", waitErr)
			return 1
		}
	}
	if code < 0 {
		fmt.Fprintf(stderr, "pg-review-router-adapter: %s terminated by a signal\n", pgReview)
		return 1
	}
	mapped, reason := mapExit(code)
	if reason != "" {
		fmt.Fprintf(stderr, "pg-review-router-adapter: %s exited %d, reporting %d: %s\n", pgReview, code, mapped, reason)
	}
	return mapped
}
