package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/supervisor"
	"github.com/phillipgreenii/x/exitcode"
	"github.com/phillipgreenii/x/osdirs"
)

const statusHelp = `pg-assistants status: show each assistant's state.

usage: pg-assistants status [--json] [--check] [--config PATH]

Reads <state dir>/status.json, which the daemon writes every 15 seconds and
at the end of every tick. It prints a header (the daemon's pid, uptime and
config hash, any config problems, the account's usage blocks), then one row
per assistant: ID ENABLED RUNNING CONTEXT LADDER WAIT NOTE, and a row with the
note "not in config" for each session nothing supervises. NOTE is the first
of: the config error, "stalled" (the loop has not ticked for tick + grace.kill
+ 45s), the hint, the last error, the last exit.

  --json    print the status file as it is
  --check   the health probe: print one line and exit 3 when the file is
            missing or over a minute old (the daemon is down), 5 when degraded
            (a config error or problem, a hint, a stalled loop, a crash
            backoff, or an orphan session), 0 otherwise

Without --check it exits 0; a missing or stale file prints a warning on
stderr naming the launchd job and its stderr log. 7 when the configuration
cannot be loaded, 2 on a usage error.
`

const (
	// staleAfter is how old the status file may be before the daemon counts
	// as down: it is written at least every 15 seconds.
	staleAfter = time.Minute
	// stalledMargin is added to an assistant's tick and grace.kill (the
	// longest a tick may hold a graceful stop) before its loop counts as stalled.
	stalledMargin = 45 * time.Second
	// launchdLabel is the daemon's launchd job (the home-manager module).
	launchdLabel = "com.phillipg.pg-assistants-daemon"
	// hashPrefix is how much of the config hash the header shows.
	hashPrefix = 12
)

// status is `pg-assistants status`.
func (c *cli) status(args []string) int {
	flags := c.newFlagSet("status")
	asJSON := flags.Bool("json", false, "print the status file as it is")
	check := flags.Bool("check", false, "health probe: exit 3 when down, 5 when degraded")
	cfgPath := configFlag(flags)
	pos, code, done := c.parse(flags, args, statusHelp)
	if done {
		return code
	}
	if len(pos) != 0 {
		fmt.Fprintln(c.stderr, "usage: pg-assistants status [--json] [--check] [--config PATH]")
		return exitcode.Usage
	}
	cfg, code, ok := c.loadConfig("status", *cfgPath)
	if !ok {
		return code
	}
	now := time.Now()
	path := statusPath(cfg.Paths.StateDir)
	data, err := os.ReadFile(path)
	var st supervisor.Status
	if err == nil {
		st, err = supervisor.ParseStatus(path, data)
	}
	var down string // why the daemon looks down; empty when the file is fresh
	switch {
	case errors.Is(err, fs.ErrNotExist):
		down = path + " is missing"
	case err != nil:
		down = err.Error()
	case now.Sub(st.WrittenAt) > staleAfter:
		down = fmt.Sprintf("%s is %s old", path, age(now.Sub(st.WrittenAt)))
	}
	if *check {
		if *asJSON && err == nil {
			_, _ = c.stdout.Write(data)
		}
		return c.checkStatus(cfg, st, down, now, !*asJSON)
	}
	if down != "" {
		fmt.Fprintf(c.stderr, "pg-assistants status: warning: %s; is the daemon running? %s\n", down, launchdHint())
	}
	if err != nil {
		return exitcode.OK
	}
	if *asJSON {
		_, _ = c.stdout.Write(data)
		return exitcode.OK
	}
	renderStatus(c.stdout, cfg, st, now)
	return exitcode.OK
}

// launchdHint names the launchd job and the stderr log the home-manager
// module gives it ($XDG_STATE_HOME/pg-assistants-launchd/launchd-stderr.log).
func launchdHint() string {
	log := filepath.Join("$XDG_STATE_HOME", "pg-assistants-launchd", "launchd-stderr.log")
	if dir, err := osdirs.StateDir("pg-assistants-launchd"); err == nil {
		log = filepath.Join(dir, "launchd-stderr.log")
	}
	return fmt.Sprintf("see launchctl print gui/%d/%s and %s", os.Getuid(), launchdLabel, log)
}

// checkStatus is `status --check`: the exit code, and one line when line is set.
func (c *cli) checkStatus(cfg config.Config, st supervisor.Status, down string, now time.Time, line bool) int {
	say := func(format string, a ...any) {
		if line {
			fmt.Fprintf(c.stdout, format+"\n", a...)
		}
	}
	if down != "" {
		say("down: %s; %s", down, launchdHint())
		return exitcode.Unavailable
	}
	if reasons := degraded(cfg, st); len(reasons) > 0 {
		say("degraded: %s", strings.Join(reasons, "; "))
		return exitcode.Degraded
	}
	running := 0
	for _, a := range st.Assistants {
		if a.Running {
			running++
		}
	}
	say("ok: %d assistants, %d running", len(st.Assistants), running)
	return exitcode.OK
}

// degraded lists what makes the daemon degraded: a global config problem, an
// assistant's config error, hint, stalled loop or crash backoff, an orphan.
func degraded(cfg config.Config, st supervisor.Status) []string {
	var out []string
	if n := len(st.Daemon.ConfigProblems); n > 0 {
		out = append(out, fmt.Sprintf("%d config problems", n))
	}
	for _, a := range st.Assistants {
		switch {
		case a.ConfigError != "":
			out = append(out, a.ID+" config error")
		case stalledAt(cfg, st, a):
			out = append(out, a.ID+" stalled")
		case a.Hint != "":
			out = append(out, a.ID+": "+a.Hint)
		case a.WaitReason == "backoff":
			out = append(out, a.ID+" in crash backoff")
		}
	}
	for _, o := range orphanRows(st) {
		out = append(out, "orphan session "+o)
	}
	return out
}

// stalledAt reports whether a's loop had stalled when the file was written.
func stalledAt(cfg config.Config, st supervisor.Status, a supervisor.AssistantStatus) bool {
	return isStalled(a, tickOf(cfg, a.ID), cfg.Grace.Kill, st.Daemon.StartedAt, st.WrittenAt)
}

// isStalled reports whether a's loop has not ticked for longer than tick +
// graceKill + stalledMargin at time at. An assistant with a config error has
// no loop and is never stalled; a loop that has not ticked yet counts from the
// daemon's start. The time is the file's: a daemon that stopped writing is
// down, which the file's age reports, not a stalled loop.
func isStalled(a supervisor.AssistantStatus, tick, graceKill time.Duration, daemonStart, at time.Time) bool {
	if a.ConfigError != "" {
		return false
	}
	last := a.LastTick
	if last.IsZero() {
		last = daemonStart
	}
	return at.Sub(last) > tick+graceKill+stalledMargin
}

// tickOf is the assistant's tick in cfg, or the global tick for one not in it.
func tickOf(cfg config.Config, id string) time.Duration {
	for _, a := range cfg.Assistants {
		if a.ID == id && a.Tick > 0 {
			return a.Tick
		}
	}
	return cfg.Tick
}

// orphanRows are the orphan sessions without an assistant row of their own:
// an assistant with a config error has a row, whose session is an orphan too.
func orphanRows(st supervisor.Status) []string {
	listed := map[string]bool{}
	for _, a := range st.Assistants {
		listed[a.ID] = true
	}
	var out []string
	for _, o := range st.Orphans {
		if !listed[o] {
			out = append(out, o)
		}
	}
	return out
}

// renderStatus prints the header and the table.
func renderStatus(w io.Writer, cfg config.Config, st supervisor.Status, now time.Time) {
	d := st.Daemon
	fmt.Fprintf(w, "daemon pid %d, up %s, config %s\n", d.PID, age(now.Sub(d.StartedAt)), prefix(d.ConfigHash, hashPrefix))
	for _, p := range d.ConfigProblems {
		fmt.Fprintf(w, "config problem: %s\n", p)
	}
	for _, l := range st.Blocks {
		if line := blockLine(cfg.Blocks, l, now); line != "" {
			fmt.Fprintln(w, line)
		}
	}
	fmt.Fprintln(w)

	orphan := map[string]bool{}
	for _, o := range st.Orphans {
		orphan[o] = true
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tENABLED\tRUNNING\tCONTEXT\tLADDER\tWAIT\tNOTE")
	for _, a := range st.Assistants {
		tick := tickOf(cfg, a.ID)
		// A session of an assistant with a config error runs as an orphan.
		running := a.Running || orphan[a.ID]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.ID, yesNo(a.Enabled), yesNo(running),
			contextCell(a, tick, now), ladderCell(a), waitCell(a, now), note(a, stalledAt(cfg, st, a), now))
	}
	for _, o := range orphanRows(st) {
		fmt.Fprintf(tw, "%s\t-\tyes\t-\t-\t-\tnot in config\n", o)
	}
	_ = tw.Flush()
}

// blockLine is one block reading, or "" for one no longer current: "five_hour
// 96% (starts held until 20:00)" at or above the nudge point, else when it
// resets.
func blockLine(b config.Blocks, l control.Limit, now time.Time) string {
	end := l.ResetsAt
	if end.IsZero() {
		end = l.ObservedAt.Add(b.UnknownResetHold)
	}
	if !end.After(now) {
		return ""
	}
	pct := fmt.Sprintf("%s %.0f%%", l.Kind, l.Fraction*100)
	switch {
	case l.Fraction >= b.Nudge:
		return fmt.Sprintf("%s (starts held until %s)", pct, clock(end, now))
	case l.ResetsAt.IsZero():
		return pct
	default:
		return fmt.Sprintf("%s (resets %s)", pct, clock(end, now))
	}
}

// contextCell is "81% (162k)", with the reading's age when it is older than
// two ticks: "81% (162k, 12m ago)".
func contextCell(a supervisor.AssistantStatus, tick time.Duration, now time.Time) string {
	if a.ContextAt.IsZero() {
		return "-"
	}
	detail := tokens(a.ContextTokens)
	if old := now.Sub(a.ContextAt); old > 2*tick {
		detail += ", " + age(old) + " ago"
	}
	return fmt.Sprintf("%.0f%% (%s)", a.ContextFraction*100, detail)
}

func ladderCell(a supervisor.AssistantStatus) string {
	switch {
	case a.Ladder == "":
		return "-"
	case a.Rung == "":
		return a.Ladder
	}
	return a.Ladder + "/" + a.Rung
}

// waitCell is why a not-running assistant was not started, and until when.
func waitCell(a supervisor.AssistantStatus, now time.Time) string {
	switch {
	case a.WaitReason == "":
		return "-"
	case a.WaitUntil.IsZero():
		return a.WaitReason
	}
	return a.WaitReason + " until " + clock(a.WaitUntil, now)
}

// note is the first of: the config error, stalled, the hint, the last error,
// the last exit. A config error comes first: such an assistant has no loop,
// so it never ticks. Stalled comes next: a stalled loop's other fields are
// old, except its last error, which is kept after it, since it is often the
// reason (a loop that panicked shows the panic there). A loop that has not
// finished a tick, such as one that panicked on its first, has a zero last
// tick: "no tick yet".
func note(a supervisor.AssistantStatus, stalled bool, now time.Time) string {
	switch {
	case a.ConfigError != "":
		return "config error: " + a.ConfigError
	case stalled:
		s := "stalled (no tick yet)"
		if !a.LastTick.IsZero() {
			s = fmt.Sprintf("stalled (last tick %s ago)", age(now.Sub(a.LastTick)))
		}
		if a.LastError != "" {
			s += ": " + a.LastError
		}
		return s
	case a.Hint != "":
		return a.Hint
	case a.LastError != "":
		return a.LastError
	case a.LastExit != nil:
		e := a.LastExit
		how := fmt.Sprintf("status %d", e.Status)
		if e.Signal != 0 {
			how = fmt.Sprintf("signal %d", e.Signal)
		}
		return fmt.Sprintf("last exit: %s after %s", how, age(time.Duration(e.UptimeS*float64(time.Second))))
	}
	return "-"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func prefix(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// tokens is a token count in thousands: "162k".
func tokens(n int64) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%dk", (n+500)/1000)
}

// age is a duration as the status shows it: "45s", "12m", "5h03m", "3d4h".
func age(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

// clock is t in local time: "20:00" on now's day, else "Nov 18 20:00".
func clock(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if t.Format(time.DateOnly) == now.Format(time.DateOnly) {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
