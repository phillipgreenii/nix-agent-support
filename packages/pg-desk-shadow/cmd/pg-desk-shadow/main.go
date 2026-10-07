// Command pg-desk-shadow runs the new pg-desk fingerprint change detection in
// parallel with the live change flow on a copy of the store, records what each
// side flags and produces a comparison report. See
// docs/behavior/pg-desk/shadow-compare.md and
// docs/runbooks/pg-desk-shadow-compare.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/collector"
	"github.com/phillipgreenii/pg-desk-shadow/internal/prepare"
	"github.com/phillipgreenii/pg-desk-shadow/internal/report"
	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
	"github.com/phillipgreenii/pg-desk-shadow/internal/shim"
)

// Exit codes.
const (
	exitOK     = 0
	exitRefuse = 2
	exitKill   = 4
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

const usage = `usage: pg-desk-shadow <command> [flags]

commands:
  prepare           build a scratch directory (store copy, configs, shims, warm-up)
  run               run the collector (outlives agent sessions; launch with bgrun + caffeinate)
  report            generate the comparison report (idempotent); --combine, --selftest
  adapter-exercise  run pg-router-source-pg-desk once under the scratch environment
  probe             run the startup safety self-test against a prepared scratch directory
  shim              (internal) the gh/bd logging shim the scratch bin directory calls
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitRefuse
	}
	switch args[0] {
	case "prepare":
		return cmdPrepare(args[1:], stdout, stderr)
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "report":
		return cmdReport(args[1:], stdout, stderr)
	case "adapter-exercise":
		return cmdAdapter(args[1:], stdout, stderr)
	case "probe":
		return cmdProbe(args[1:], stdout, stderr)
	case "shim":
		return cmdShim(args[1:], os.Stdin, stdout, stderr)
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(stdout, usage)
		return exitOK
	}
	_, _ = fmt.Fprintf(stderr, "pg-desk-shadow: unknown command %q\n%s", args[0], usage)
	return exitRefuse
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func cmdPrepare(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("prepare", stderr)
	var o prepare.Options
	var queries string
	fs.StringVar(&o.Root, "scratch", "", "scratch directory (must not exist as a prepared one)")
	fs.StringVar(&o.Phase, "phase", "A", "phase tag written on every row")
	fs.BoolVar(&o.NoSeed, "no-seed", false, "skip the warm-up seeding (phase B)")
	fs.StringVar(&queries, "queries", "mine,team", "watched pr queries, in config order")
	fs.StringVar(&o.BDMode, "bd-mode", "passthrough", "passthrough (read-only machine bd) or hermetic (bd answers from nothing)")
	fs.StringVar(&o.LiveStore, "live-store", "", "live store (default ~/.local/state/pg-desk/store.db)")
	fs.StringVar(&o.DeskConfig, "pg-desk-config", "", "live pg-desk config")
	fs.StringVar(&o.PRConfig, "pg-pr-config", "", "live pg-pr config")
	fs.IntVar(&o.ListAttempts, "list-attempts", 6, "tries per warm-up listing that exits 3 (the team listing sits near the connector's 25s backend deadline)")
	var toolDirs string
	fs.StringVar(&toolDirs, "tool-dir", "", "comma-separated extra directories searched for tools after PATH")
	if err := fs.Parse(args); err != nil {
		return exitRefuse
	}
	if o.Root == "" {
		_, _ = fmt.Fprintln(stderr, "pg-desk-shadow prepare: --scratch is required")
		return exitRefuse
	}
	o.Queries = strings.Split(queries, ",")
	if toolDirs != "" {
		o.ToolDirs = strings.Split(toolDirs, ",")
	}
	o.Log = stdout
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	l, m, err := prepare.Prepare(ctx, o)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow prepare: %v\n", collector.Scrub(err.Error(), m.Scrub))
		return exitRefuse
	}
	_, _ = fmt.Fprintf(stdout, "prepared %s (phase %s, seeded %v, bd mode %s)\n", l.Root, m.Phase, m.Seeded, m.BDMode)
	return exitOK
}

// load builds the runner for a prepared directory.
func load(root string) (scratch.Layout, scratch.Manifest, *runner.Runner, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return scratch.Layout{}, scratch.Manifest{}, nil, err
	}
	l := scratch.Layout{Root: safety.Resolve(abs)}
	m, err := l.Load()
	if err != nil {
		return l, m, nil, err
	}
	pol := l.Policy(m)
	r := &runner.Runner{Policy: pol, Env: safety.ChildEnv(pol, "/usr/bin:/bin"), SandboxExec: m.SandboxExec, WorkDir: l.WorkDir()}
	if err := safety.Verify(r.Env, pol); err != nil {
		return l, m, nil, err
	}
	return l, m, r, nil
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("run", stderr)
	var root string
	cfg := collector.Config{}
	fs.StringVar(&root, "scratch", "", "prepared scratch directory")
	fs.DurationVar(&cfg.Period, "period", time.Minute, "slot length")
	fs.DurationVar(&cfg.TickTimeout, "tick-timeout", 5*time.Minute, "deadline of one pg-desk call")
	fs.IntVar(&cfg.KillPointsPerHour, "kill-points-per-hour", 1500, "abort when shadow spend in a rolling hour exceeds this")
	fs.IntVar(&cfg.MaxFailedTicks, "max-failed-ticks", 10, "abort after more than this many consecutive failed ticks")
	fs.IntVar(&cfg.FloorMargin, "floor-margin", 200, "margin in the budget floor")
	fs.IntVar(&cfg.MaxTicks, "max-ticks", 0, "stop after this many executed ticks (0 = run until stopped)")
	noScan := fs.Bool("no-denial-log-scan", false, "do not read the system log for sandbox denials")
	if err := fs.Parse(args); err != nil {
		return exitRefuse
	}
	if root == "" {
		_, _ = fmt.Fprintln(stderr, "pg-desk-shadow run: --scratch is required")
		return exitRefuse
	}
	l, m, r, err := load(root)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow run: refusing to start: %v\n", err)
		return exitRefuse
	}
	unlock, err := collector.Lock(l)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow run: refusing to start: %v\n", err)
		return exitRefuse
	}
	defer unlock()
	logf, err := os.OpenFile(l.CollectorLog(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow run: %v\n", err)
		return exitRefuse
	}
	defer func() { _ = logf.Close() }()
	cfg.Layout, cfg.Manifest, cfg.Exec = l, m, r
	cfg.Log = io.MultiWriter(logf, stdout)
	cfg.WarmupExclusion = m.Seeded
	cfg.SandboxDenialScan = !*noScan
	cfg.Procs = []string{"pg-desk", ".pg-desk-wrapped", "pg-connector", ".pg-connector-pr-github-wrapped", "pg-connector-pr-github", "pg-connector-issue-beads", "gh", "bd", "pg-desk-shadow"}
	c, err := collector.New(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow run: %v\n", err)
		return exitRefuse
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := c.SelfCheck(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow run: refusing to start: %v\n", err)
		return exitRefuse
	}
	if err := c.Recover(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow run: resume: %v\n", err)
		return exitRefuse
	}
	err = c.Run(ctx, fileExists(l.StateFile()))
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, collector.ErrKill):
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow run: KILL: %v\n", collector.Scrub(err.Error(), m.Scrub))
		return exitKill
	}
	_, _ = fmt.Fprintf(stderr, "pg-desk-shadow run: %v\n", collector.Scrub(err.Error(), m.Scrub))
	return exitKill
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func cmdProbe(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("probe", stderr)
	root := fs.String("scratch", "", "prepared scratch directory")
	if err := fs.Parse(args); err != nil {
		return exitRefuse
	}
	l, m, _, err := load(*root)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "probe: %v\n", err)
		return exitRefuse
	}
	if err := safety.SelfTest(context.Background(), m.SandboxExec, safety.Resolve(l.Root), l.TmpDir()); err != nil {
		_, _ = fmt.Fprintf(stderr, "probe: %v\n", err)
		return exitRefuse
	}
	_, _ = fmt.Fprintln(stdout, "probe ok: sandbox denies a write outside the scratch directory and allows the permitted ones")
	return exitOK
}

func cmdAdapter(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("adapter-exercise", stderr)
	root := fs.String("scratch", "", "prepared scratch directory")
	if err := fs.Parse(args); err != nil {
		return exitRefuse
	}
	l, m, r, err := load(*root)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "adapter-exercise: %v\n", err)
		return exitRefuse
	}
	bin := filepath.Join(l.BinDir(), "pg-router-source-pg-desk")
	if !fileExists(bin) {
		_, _ = fmt.Fprintln(stderr, "adapter-exercise: pg-router-source-pg-desk was not found at prepare time")
		return exitRefuse
	}
	res, err := r.Run(context.Background(), 5*time.Minute, bin, "pr", "--consumer", "shadow-adapter", "--limit", "5")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "adapter-exercise: %v\n", collector.Scrub(err.Error(), m.Scrub))
		return exitRefuse
	}
	var items []map[string]any
	if jerr := json.Unmarshal([]byte(res.Stdout), &items); jerr != nil {
		_, _ = fmt.Fprintf(stderr, "adapter-exercise: exit %d, output is not a JSON array: %s\n", res.Exit, collector.Scrub(res.Stderr, m.Scrub))
		return exitRefuse
	}
	_, _ = fmt.Fprintf(stdout, "adapter-exercise ok: exit %d, %d item(s), denied=%v\n", res.Exit, len(items), res.Denied)
	if res.Denied {
		return exitKill
	}
	return exitOK
}

func cmdReport(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("report", stderr)
	root := fs.String("scratch", "", "scratch directory")
	combine := fs.String("combine", "", "comma-separated further scratch directories to merge (phase B)")
	out := fs.String("out", "", "output directory (default <scratch>/reports)")
	liveStore := fs.String("live-store", "", "live store for the optional head-SHA comparison")
	selftest := fs.Bool("selftest", false, "run the whole pipeline on synthetic logs")
	if err := fs.Parse(args); err != nil {
		return exitRefuse
	}
	if *selftest {
		if err := report.SelfTest(stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return exitRefuse
		}
		return exitOK
	}
	if *root == "" {
		_, _ = fmt.Fprintln(stderr, "pg-desk-shadow report: --scratch is required")
		return exitRefuse
	}
	dirs := []string{*root}
	if *combine != "" {
		dirs = append(dirs, strings.Split(*combine, ",")...)
	}
	p := report.DefaultParams()
	p.LiveStore = *liveStore
	rep, err := report.Build(dirs, p)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow report: %v\n", err)
		return exitRefuse
	}
	dir := *out
	if dir == "" {
		dir = filepath.Join(*root, "reports")
	}
	jp, mp, err := report.Write(rep, dir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-desk-shadow report: %v\n", err)
		return exitRefuse
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s and %s\n", mp, jp)
	return exitOK
}

func cmdShim(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var o shim.Options
	o.Stdin, o.Stdout, o.Stderr = stdin, stdout, stderr
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		switch a {
		case "--tool":
			i++
			o.Tool = args[i]
		case "--real":
			i++
			o.Real = args[i]
		case "--log":
			i++
			o.Log = args[i]
		case "--hermetic-bd":
			o.HermeticBD = true
		default:
			_, _ = fmt.Fprintf(stderr, "pg-desk-shadow shim: unknown flag %s\n", a)
			return shim.ExitRejected
		}
	}
	return shim.Run(o, args[i:])
}
