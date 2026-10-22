package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
	"github.com/phillipgreenii/pg-router/schemas"
)

// doctorValidateTimeout bounds each handler's `validate` probe. It is a package
// var, not a const, so a test can lower it.
var doctorValidateTimeout = 5 * time.Second

// Probe verdicts, the first word of every doctor line.
const (
	verdictOK   = "ok"
	verdictFail = "FAIL"
	verdictInfo = "info"
)

// runDoctor implements `pg-router doctor [--socket <path>] [--token <tok>]`: the
// operator's one-shot health check. It runs `config check` plus quick probes (the
// state dir, the socket, the active gates, the unavailable roles, per-role
// progress, and each handler's validate), printing one verdict line per finding.
//
// EXIT CODES: 0 when no probe is FAIL, 1 otherwise, 2 usage. It never starts a
// core: the probes that read the running core report info when none is running.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves
	socket := fs.String("socket", "", "path to the running core's socket (overrides discovery)")
	token := fs.String("token", "", "auth token for the running core (with --socket)")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println(helpText)
		return exitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "doctor:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "doctor: unexpected argument:", fs.Arg(0))
		return conformance.ExitUsage
	}
	return doctor(os.Stdout, *socket, *token)
}

// doctorReport collects the verdict lines and whether any is a FAIL.
type doctorReport struct {
	w      io.Writer
	failed bool
}

func (r *doctorReport) line(verdict, probe, format string, a ...any) {
	if verdict == verdictFail {
		r.failed = true
	}
	_, _ = fmt.Fprintf(r.w, "%-4s %s: %s\n", verdict, probe, fmt.Sprintf(format, a...))
}

// doctor is runDoctor's testable body.
func doctor(stdout io.Writer, socket, token string) int {
	rep := &doctorReport{w: stdout}

	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		for _, line := range errorLines(cfgErr) {
			rep.line(verdictFail, "config", "%s", textsafe.Sanitize(line))
		}
	} else {
		rep.line(verdictOK, "config", "config ok")
	}

	doctorStateDir(rep, config.LogDir())

	st, ok := doctorSocket(rep, socket, token)
	if ok {
		doctorGates(rep, st)
		doctorUnavailable(rep, st)
		doctorProgress(rep, st)
	} else {
		for _, probe := range []string{"gates", "unavailable roles", "progress"} {
			rep.line(verdictInfo, probe, "daemon not reachable")
		}
	}

	if cfgErr != nil {
		rep.line(verdictInfo, "validate", "skipped: the config did not load, so the roles are unknown")
	} else {
		doctorValidate(rep, cfg)
	}

	if rep.failed {
		return exitGeneric
	}
	return exitOK
}

// doctorStateDir probes that dir exists and is writable, by creating and removing
// a temporary file in it.
func doctorStateDir(rep *doctorReport, dir string) {
	f, err := os.CreateTemp(dir, ".pg-router-doctor-*")
	if err != nil {
		rep.line(verdictFail, "state dir", "%s is missing or not writable: %v", dir, err)
		return
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		rep.line(verdictFail, "state dir", "%s is writable but its probe file could not be removed: %v", dir, err)
		return
	}
	rep.line(verdictOK, "state dir", "%s is writable", dir)
}

// doctorStatus is the part of the status reply doctor reads.
type doctorStatus struct {
	Gates     []statusGate   `json:"gates"`
	Listeners []listenerView `json:"listeners"`
}

// doctorSocket probes that a core's socket is reachable and, when it is, reads one
// status reply from it. The bool is false when the socket probe FAILed.
func doctorSocket(rep *doctorReport, socket, token string) (doctorStatus, bool) {
	ref, err := locateCore(socket, token)
	if err != nil {
		rep.line(verdictFail, "socket", "%v", err)
		return doctorStatus{}, false
	}
	client, err := core.Dial(ref, core.DefaultProbeTimeout)
	if err != nil {
		rep.line(verdictFail, "socket", "%s: %v", ref.Socket, err)
		return doctorStatus{}, false
	}
	_ = client.Close()

	var st doctorStatus
	var diag bytes.Buffer
	req := map[string]any{"schemaVersion": schemas.SchemaVersion}
	if code := callGateVerb(&diag, ref, core.SubcommandStatus, core.StatusReplySchema, req, &st); code != exitOK {
		rep.line(verdictFail, "socket", "%s is reachable, but its status call failed: %s", ref.Socket, strings.TrimSpace(diag.String()))
		return doctorStatus{}, false
	}
	rep.line(verdictOK, "socket", "%s is reachable", ref.Socket)
	return st, true
}

// doctorGates reports the active gates (info only).
func doctorGates(rep *doctorReport, st doctorStatus) {
	if len(st.Gates) == 0 {
		rep.line(verdictInfo, "gates", "none active")
		return
	}
	var b bytes.Buffer
	renderGates(&b, st.Gates)
	rep.line(verdictInfo, "gates", "%d active", len(st.Gates))
	_, _ = rep.w.Write(b.Bytes())
}

// doctorUnavailable FAILs once per role the core marked unavailable, with the reason.
func doctorUnavailable(rep *doctorReport, st doctorStatus) {
	n := 0
	for _, l := range st.Listeners {
		if l.UnavailableReason == "" {
			continue
		}
		n++
		rep.line(verdictFail, "unavailable roles", "%s: %s", textsafe.Sanitize(l.Role), textsafe.Sanitize(l.UnavailableReason))
	}
	if n == 0 {
		rep.line(verdictOK, "unavailable roles", "none")
	}
}

// doctorProgress reports each role's progress, as the progress gauges read it
// (info only).
func doctorProgress(rep *doctorReport, st doctorStatus) {
	n := 0
	for _, l := range st.Listeners {
		if l.Progress == nil {
			continue
		}
		n++
		age := time.Duration(l.Progress.AgeSeconds * float64(time.Second)).Round(time.Second)
		rep.line(verdictInfo, "progress", "%s: last progress %s ago, %d unsuccessful and %d busy since",
			textsafe.Sanitize(l.Role), age, l.Progress.Unsuccessful, l.Progress.Busy)
	}
	if n == 0 {
		rep.line(verdictInfo, "progress", "no role has a progress series")
	}
}

// doctorValidate runs every enabled role's handler `validate` concurrently, each
// under its own doctorValidateTimeout, and reports in config order.
func doctorValidate(rep *doctorReport, cfg config.Config) {
	var enabled []roles.Role
	for _, r := range cfg.Roles {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	if len(enabled) == 0 {
		rep.line(verdictInfo, "validate", "no enabled role")
		return
	}
	client := wireclient.New(handlerCommandFor(cfg))
	type outcome struct {
		res      wireclient.ValidateResult
		err      error
		timedOut bool
	}
	outcomes := make([]outcome, len(enabled))
	var wg sync.WaitGroup
	for i, role := range enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), doctorValidateTimeout)
			defer cancel()
			// The deadline is reported the moment it passes, whether or not the
			// runner has finished winding the handler down (a child that keeps
			// the handler's stdout open delays it by the runner's wait delay).
			done := make(chan outcome, 1)
			go func() {
				res, err := client.Validate(ctx, role)
				done <- outcome{res: res, err: err, timedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
			}()
			select {
			case outcomes[i] = <-done:
			case <-ctx.Done():
				outcomes[i] = outcome{timedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
			}
		}()
	}
	wg.Wait()
	for i, role := range enabled {
		probe := "validate " + textsafe.Sanitize(role.Name)
		o := outcomes[i]
		switch {
		case o.timedOut:
			rep.line(verdictFail, probe, "timed out after %s", doctorValidateTimeout)
		case o.err != nil:
			rep.line(verdictFail, probe, "%s", textsafe.Sanitize(o.err.Error()))
		case !o.res.Supported:
			rep.line(verdictInfo, probe, "not supported")
		default:
			for _, p := range o.res.Problems {
				rep.line(verdictFail, probe, "%s", textsafe.Sanitize(p))
			}
			for _, w := range o.res.Warnings {
				rep.line(verdictInfo, probe, "warning: %s", textsafe.Sanitize(w))
			}
			if len(o.res.Problems) == 0 {
				rep.line(verdictOK, probe, "valid")
			}
		}
	}
}
