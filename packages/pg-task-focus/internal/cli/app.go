// Package cli is the pg-task-focus command line: `serve` runs the daemon, every
// other verb is a thin client of its HTTP API, and `check` verifies a log
// offline. Every client verb has --json, whose output is the daemon's own JSON
// and validates against a checked-in schema.
package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// The exit codes. 1 is the generic error and carries no branchable meaning;
// every specific condition has its own value of 2 or more.
const (
	ExitOK          = 0
	ExitError       = 1 // an unexpected error
	ExitUsage       = 2 // a bad flag or argument
	ExitUnreachable = 3 // the daemon could not be reached
	ExitRefused     = 4 // the daemon refused the request with a problem
	ExitStore       = 5 // store_unavailable: read-only mode, or an unknown outcome
	ExitNotReady    = 6 // the daemon is starting and answered not_ready
	ExitStartFailed = 7 // serve could not start
	ExitCheckFailed = 8 // check found a problem in the log or the configuration
)

// App is what a command needs from its surroundings; tests replace them.
type App struct {
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	Readlink       func(string) (string, error)
	Now            func() time.Time
	// Version is the build version.
	Version string
	// Serve runs the daemon until it is stopped; main supplies it (it owns
	// the signals). Nil makes `serve` fail with a usage message.
	Serve func(ctx context.Context, p ServeParams) error
}

// ServeParams is what `serve` hands the daemon.
type ServeParams struct {
	ConfigPath, DataDir string
}

func (a *App) defaults() {
	if a.Stdout == nil {
		a.Stdout = os.Stdout
	}
	if a.Stderr == nil {
		a.Stderr = os.Stderr
	}
	if a.Getenv == nil {
		a.Getenv = os.Getenv
	}
	if a.Readlink == nil {
		a.Readlink = os.Readlink
	}
	if a.Now == nil {
		a.Now = time.Now
	}
	if a.Version == "" {
		a.Version = "dev"
	}
}

// exitError carries an exit code out of a command.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &exitError{code: ExitUsage, msg: fmt.Sprintf(format, args...)}
}

// state is what the commands share: the flags of the root command.
type state struct {
	app    *App
	lastID string
	addr   string
	json   bool
	wait   time.Duration
}

func (s *state) client() *client.Client {
	addr := s.addr
	if addr == "" {
		addr = s.app.Getenv("PG_TASK_FOCUS_ADDR")
	}
	if addr == "" {
		addr = client.DefaultAddr
	}
	return &client.Client{Addr: addr, Name: "cli", Timeout: s.wait}
}

// Execute runs the command line and returns the exit code.
func Execute(ctx context.Context, args []string, app App) int {
	app.defaults()
	st := &state{app: &app}
	root := newRoot(st)
	root.SetArgs(args)
	root.SetOut(app.Stdout)
	root.SetErr(app.Stderr)
	root.SilenceUsage = true
	root.SilenceErrors = true
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	return st.report(err)
}

// report prints an error the way the contract says and returns its exit code.
func (s *state) report(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		fmt.Fprintln(s.app.Stderr, "pg-task-focus: "+ee.msg)
		return ee.code
	}
	var unreachable *client.UnreachableError
	if errors.As(err, &unreachable) {
		fmt.Fprintf(s.app.Stderr, "pg-task-focus: not reachable at %s: is the daemon running?\n", unreachable.Addr)
		return ExitUnreachable
	}
	var pe *client.ProblemError
	if errors.As(err, &pe) {
		return s.reportProblem(pe)
	}
	// A cobra usage error (an unknown flag or verb, a wrong argument count).
	if isUsageError(err) {
		fmt.Fprintln(s.app.Stderr, "pg-task-focus: "+err.Error())
		return ExitUsage
	}
	fmt.Fprintln(s.app.Stderr, "pg-task-focus: "+err.Error())
	return ExitError
}

func isUsageError(err error) bool {
	m := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "accepts ", "requires at least", "requires at most", "required flag", "invalid argument", "flag needs an argument"} {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

// reportProblem prints a refusal. With --json the problem document goes to
// stdout, unchanged.
func (s *state) reportProblem(pe *client.ProblemError) int {
	p := pe.Problem
	if s.json {
		b, _ := json.Marshal(p)
		fmt.Fprintln(s.app.Stdout, string(b))
	}
	w := s.app.Stderr
	switch {
	case pe.ReadOnly():
		fmt.Fprintln(w, p.Store.ReadOnlySentence())
		return ExitStore
	case p.Reason == "store_unavailable":
		fmt.Fprintln(w, "pg-task-focus: the store did not take the change and the outcome is unknown: "+p.Detail)
		if id := s.lastID; id != "" {
			fmt.Fprintf(w, "The verb can be retried: it carries the same id; add --id %s to send it again.\n", id)
		}
		return ExitStore
	case p.Reason == "not_ready":
		fmt.Fprintln(w, "pg-task-focus: the daemon is starting and is not ready: "+p.Detail)
		return ExitNotReady
	}
	fmt.Fprintf(w, "pg-task-focus: refused (%s): %s\n", p.Reason, p.Detail)
	if p.Details != nil && len(p.Details.Cycles) > 0 {
		label := "Candidates"
		if p.Reason == "cycle_active" {
			label = "Blocking cycles"
		}
		fmt.Fprintf(w, "%s:\n", label)
		for _, c := range p.Details.Cycles {
			fmt.Fprintf(w, "  %s  %s  (%s)\n", c.ID, c.Title, c.Status)
		}
	}
	return ExitRefused
}

// newID is a fresh client id for a mutation, or the one given with --id.
func (s *state) newID(given string) string {
	if given != "" {
		s.lastID = given
		return given
	}
	id := string(event.NewID(s.app.Now(), rand.Reader))
	s.lastID = id
	return id
}

// out prints a JSON document: as it is for --json, else through human.
func (s *state) out(raw []byte, human func(w io.Writer) error) error {
	if s.json {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return err
		}
		fmt.Fprintln(s.app.Stdout, buf.String())
		return nil
	}
	return human(s.app.Stdout)
}

// hostZone is the zone the machine runs in, or a failure that says how to name
// one: a client MUST NOT guess it.
func (s *state) hostZone() (zone.Zone, error) {
	z, err := zone.Host(s.app.Getenv, s.app.Readlink)
	if err != nil {
		return zone.Zone{}, &exitError{code: ExitError, msg: err.Error()}
	}
	return z, nil
}

func (s *state) printf(format string, args ...any) { fmt.Fprintf(s.app.Stdout, format, args...) }

// mutate posts a mutation and prints its result.
func (s *state) mutate(ctx context.Context, path string, body map[string]any) error {
	raw, err := s.client().Do(ctx, "POST", path, body)
	if err != nil {
		return err
	}
	return s.printResult(raw)
}

// printResult prints the result of a mutation or the preview of a dry run.
func (s *state) printResult(raw []byte) error {
	return s.out(raw, func(w io.Writer) error {
		var probe struct {
			DryRun bool `json:"dry_run"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.DryRun {
			var d wire.DryRunResult
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			renderPreview(w, d)
			return nil
		}
		var r wire.Result
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		switch {
		case !r.Changed && r.Note != "":
			fmt.Fprintln(w, "No change: "+r.Note)
		case !r.Changed:
			fmt.Fprintln(w, "No change.")
		default:
			fmt.Fprintf(w, "Done: %d event(s)", len(r.EventIDs))
			if r.BatchID != "" {
				fmt.Fprintf(w, " in batch %s", r.BatchID)
			}
			if r.Replayed {
				fmt.Fprint(w, " (the answer to an earlier request with the same id)")
			}
			fmt.Fprintln(w)
		}
		return nil
	})
}
