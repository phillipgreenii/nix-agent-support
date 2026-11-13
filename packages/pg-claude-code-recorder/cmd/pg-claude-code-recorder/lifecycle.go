package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// parseSince reads a duration such as 7d (back from now) or an RFC 3339 time.
func parseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		s = "7d"
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	d, err := config.ParseDuration(s)
	if err != nil || d < 0 {
		return time.Time{}, fmt.Errorf("--since %q is neither a duration such as 7d nor an RFC 3339 time", s)
	}
	return now.Add(-d), nil
}

func parseLabel(s string) (lifecycle.Label, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "crashed":
		return lifecycle.LabelCrashed, true
	case "exited":
		return lifecycle.LabelExited, true
	case "cleared":
		return lifecycle.LabelCleared, true
	case "closed", "closed normally":
		return lifecycle.LabelClosed, true
	case "unknown":
		return lifecycle.LabelUnknown, true
	}
	return "", false
}

func fmtTime(t *time.Time, approx bool) string {
	if t == nil {
		return "-"
	}
	s := t.UTC().Format(time.RFC3339)
	if approx {
		return "~" + s
	}
	return s
}

// conversations lists the Conversations that ended a given way: the crash query.
func (a *app) conversations(args []string) int {
	var c common
	var ended, since, cwd string
	var crashed, suspected bool
	var limit int
	fs := newFlagSet("conversations", a.errw)
	c.bind(fs)
	fs.StringVar(&ended, "ended", "", "list Conversations that ended this way: crashed, exited, cleared, closed or unknown")
	fs.BoolVar(&crashed, "crashed", false, "shortcut for --ended crashed")
	fs.BoolVar(&suspected, "suspected-crash", false, "crashed plus unknown rows with a stale registry file at discovery")
	fs.StringVar(&since, "since", "7d", "only Conversations that ended since: a duration such as 7d, or an RFC 3339 time")
	fs.StringVar(&cwd, "cwd", "", "only Conversations whose cwd is this path or below it")
	fs.IntVar(&limit, "limit", 50, "most rows to return")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	if fs.NArg() > 0 {
		return a.emitError(c.jsonOut, wire.ErrUsage, "conversations takes no arguments")
	}
	if ended != "" && (crashed || suspected) {
		return a.emitError(c.jsonOut, wire.ErrUsage, "--ended cannot be combined with --crashed or --suspected-crash")
	}
	if crashed && suspected {
		return a.emitError(c.jsonOut, wire.ErrUsage, "--crashed and --suspected-crash are alternatives")
	}
	q := lifecycle.EndQuery{Cwd: cwd, Limit: limit, Suspected: suspected}
	what := "ended"
	switch {
	case crashed:
		q.Label, what = lifecycle.LabelCrashed, "crashed"
	case suspected:
		what = "suspected crashes"
	case ended != "":
		l, ok := parseLabel(ended)
		if !ok {
			return a.emitError(c.jsonOut, wire.ErrUsage, fmt.Sprintf("--ended %q is not one of crashed, exited, cleared, closed or unknown", ended))
		}
		q.Label, what = l, string(l)
	}
	if limit < 0 {
		return a.emitError(c.jsonOut, wire.ErrUsage, "--limit must not be negative")
	}
	t, err := parseSince(since, time.Now())
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	q.Since = t
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrBadConfig, err.Error())
	}
	res, err := lifecycle.QueryEnded(a.ctx, cl, q)
	if err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	a.printEnded(res, what, c.jsonOut)
	if res.Degraded && !c.partialOK {
		return a.emitErrorFor(c.jsonOut, wire.ErrBehind, "the recorder is still behind (first backfill)", cfg.Socket)
	}
	return wire.ExitOK
}

func (a *app) printEnded(res lifecycle.EndedResult, what string, jsonOut bool) {
	if jsonOut {
		rows := res.Rows
		if rows == nil {
			rows = []lifecycle.Ended{}
		}
		b, _ := json.Marshal(rows)
		_, _ = fmt.Fprintln(a.out, string(b))
		return
	}
	if len(res.Rows) == 0 {
		line := "0 " + what
		if res.UnknownStale > 0 {
			line += fmt.Sprintf("; %d unknown with a stale registry file (run --suspected-crash)", res.UnknownStale)
		}
		_, _ = fmt.Fprintln(a.out, line)
		return
	}
	for _, r := range res.Rows {
		label := string(r.Label)
		if r.Label == lifecycle.LabelClosed {
			label = "closed normally"
		}
		cwd := r.Cwd
		if cwd == "" {
			cwd = "(cwd unknown)"
		}
		_, _ = fmt.Fprintf(a.out, "%s  %s  %s  %s\n", r.SessionID, fmtTime(r.EndTime, r.EndTimeApprox), label, cwd)
		orig := "originals present"
		if !r.OriginalsExist {
			orig = "originals removed"
		}
		_, _ = fmt.Fprintf(a.out, "    stage %s, %s; evidence: registry_present_at_exit=%t exit_observed=%t clear_entry=%t exit_entry=%t stale_at_discovery=%t\n",
			r.Stage, orig, r.RegistryPresentAtExit, r.ExitObserved, r.ClearEntry, r.ExitEntry, r.RegistryStaleAtDiscovery)
		_, _ = fmt.Fprintf(a.out, "    resume: %s\n", r.Resume)
	}
}

// processes lists the Processes the recorder has seen.
func (a *app) processes(args []string) int {
	var c common
	var live bool
	var session string
	var pid int64
	var limit int
	fs := newFlagSet("processes", a.errw)
	c.bind(fs)
	fs.BoolVar(&live, "live", false, "only live Processes")
	fs.StringVar(&session, "session", "", "only the Process that holds this session id")
	fs.Int64Var(&pid, "pid", 0, "show one Process with its registry versions and Conversations")
	fs.IntVar(&limit, "limit", 50, "most rows to return")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrBadConfig, err.Error())
	}
	if pid > 0 {
		var p lifecycle.Process
		if err := cl.Post(a.ctx, "process.show", lifecycle.ProcessShow{Pid: pid}, &p); err != nil {
			return a.emitWireError(c.jsonOut, err, cfg.Socket)
		}
		b, _ := json.Marshal(p)
		_, _ = fmt.Fprintln(a.out, string(b))
		return wire.ExitOK
	}
	f := lifecycle.ProcessFilter{SessionID: session, Limit: limit}
	if live {
		t := true
		f.Alive = &t
	}
	var list lifecycle.ProcessList
	if err := cl.Post(a.ctx, "process.list", f, &list); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	a.printProcesses(list, c.jsonOut)
	if list.Degraded && !c.partialOK {
		return a.emitErrorFor(c.jsonOut, wire.ErrBehind, "the recorder is still behind (first backfill)", cfg.Socket)
	}
	return wire.ExitOK
}

func (a *app) printProcesses(list lifecycle.ProcessList, jsonOut bool) {
	if jsonOut {
		ps := list.Processes
		if ps == nil {
			ps = []lifecycle.Process{}
		}
		b, _ := json.Marshal(ps)
		_, _ = fmt.Fprintln(a.out, string(b))
		return
	}
	if len(list.Processes) == 0 {
		_, _ = fmt.Fprintln(a.out, "0 processes")
		return
	}
	_, _ = fmt.Fprintln(a.out, "PID  PROC_START  ALIVE  SESSION  NAME  HOST  CONTROL_INBOX")
	for _, p := range list.Processes {
		host := "-"
		if p.Host != nil {
			host = p.Host.Kind
		}
		_, _ = fmt.Fprintf(a.out, "%d  %s  %t  %s  %s  %s  %t\n", p.Pid, p.ProcStart, p.Alive, p.SessionID, p.Name, host, p.ControlInbox)
	}
}
