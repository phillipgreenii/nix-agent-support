package originguard

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"
)

// Deps are the seams of the CLI. RealDeps wires the production ones.
type Deps struct {
	Env              Env
	Stdout, Stderr   io.Writer
	Clock            Clock
	Dialer           AgentDialer
	Spawner          Spawner
	Procs            Procs
	Timers           Timers
	HasTerminal      IsTTY
	StderrIsTerminal IsTTY
	Resolve          RemoteResolver
	// Presence overrides the presence file reader when non-nil.
	Presence PresenceSource
	// Signals is forwarded to the proxy child. RealDeps subscribes.
	Signals <-chan os.Signal
	// ConfigPath overrides the config file location when non-empty.
	ConfigPath string
}

// RealDeps returns the production dependencies and starts forwarding
// SIGINT, SIGTERM and SIGHUP to the proxy child.
func RealDeps() Deps {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return Deps{
		Env: OSEnv(), Stdout: os.Stdout, Stderr: os.Stderr,
		Clock: SystemClock{}, Dialer: SSHAgentDialer{}, Spawner: ExecSpawner{},
		Procs: OSProcs{}, Timers: RealTimers{},
		HasTerminal: CallerHasTerminal, StderrIsTerminal: StderrIsTerminal,
		Signals: ch,
	}
}

const usageText = `usage: pg-origin-guard <command>

  check (--realm R | --remote URL | --repo PATH) [--json]
  status [--json]
  ignore REALM
  unignore REALM
  proxy --realm R -- CMD ARGS...

exit: 0 available, 3 unavailable or refused, 2 usage, 7 config, 1 internal
`

// RunCLI runs one command line (args without the program name) and returns
// the exit status.
func RunCLI(ctx context.Context, args []string, d Deps) int {
	if len(args) == 0 {
		fmt.Fprint(d.Stderr, usageText)
		return ExitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(d.Stdout, usageText)
		return ExitOK
	case "proxy":
		return runProxy(ctx, rest, d)
	case "check", "status", "ignore", "unignore":
	default:
		fmt.Fprintf(d.Stderr, "pg-origin-guard: unknown command %q\n%s", cmd, usageText)
		return ExitUsage
	}

	cfg, err := loadConfig(d)
	if err != nil {
		return reportErr(d, err)
	}
	checker := NewChecker(cfg, d.Env, d.Dialer, d.Clock)
	checker.Resolve = d.Resolve
	checker.OnRecordError = func(error) {}
	switch cmd {
	case "check":
		return runCheck(ctx, rest, d, checker)
	case "status":
		return runStatus(ctx, rest, d, cfg, checker)
	default:
		return runIgnore(rest, d, cfg, checker.Store, cmd == "ignore")
	}
}

func loadConfig(d Deps) (Config, error) {
	p := d.ConfigPath
	if p == "" {
		p = d.Env.ConfigPath()
	}
	return Load(p, d.Env)
}

func reportErr(d Deps, err error) int {
	var ue *UsageError
	var ce *ConfigError
	switch {
	case errors.As(err, &ue):
		fmt.Fprintf(d.Stderr, "pg-origin-guard: %v\n", err)
		return ExitUsage
	case errors.As(err, &ce):
		fmt.Fprintf(d.Stderr, "pg-origin-guard: %v\n", err)
		return ExitConfig
	default:
		fmt.Fprintf(d.Stderr, "pg-origin-guard: internal fault: %v\n", err)
		return ExitInternal
	}
}

func newFlags(name string, d Deps) *flag.FlagSet {
	fs := flag.NewFlagSet("pg-origin-guard "+name, flag.ContinueOnError)
	fs.SetOutput(d.Stderr)
	return fs
}

type resultJSON struct {
	Realm     string    `json:"realm"`
	Available bool      `json:"available"`
	Reason    Reason    `json:"reason"`
	Since     time.Time `json:"since,omitzero"`
	LastError string    `json:"last_error,omitempty"`
}

func runCheck(ctx context.Context, args []string, d Deps, g Guard) int {
	fs := newFlags("check", d)
	realm := fs.String("realm", "", "realm name")
	remote := fs.String("remote", "", "git remote URL")
	repo := fs.String("repo", "", "repository path")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(d.Stderr, "pg-origin-guard: check takes no positional arguments")
		return ExitUsage
	}
	res, err := g.Check(ctx, Request{Realm: *realm, Remote: *remote, RepoPath: *repo})
	if err != nil {
		return reportErr(d, err)
	}
	if *asJSON {
		_ = json.NewEncoder(d.Stdout).Encode(resultJSON(res))
	} else if res.Available {
		fmt.Fprintf(d.Stdout, "available realm=%s reason=%s\n", res.Realm, res.Reason)
	} else {
		fmt.Fprintf(d.Stdout, "unavailable realm=%s reason=%s\n", res.Realm, res.Reason)
		if res.LastError != "" {
			fmt.Fprintf(d.Stdout, "last error: %s\n", res.LastError)
		}
	}
	if res.Available {
		return ExitOK
	}
	return ExitUnavailable
}

type statusRealm struct {
	Realm      string    `json:"realm"`
	Ignored    bool      `json:"ignored"`
	Available  bool      `json:"available"`
	Reason     Reason    `json:"reason"`
	Since      time.Time `json:"since,omitzero"`
	Failures   int       `json:"consecutive_failures"`
	OpenUntil  time.Time `json:"open_until,omitzero"`
	Attempts   int       `json:"attempts"`
	NextTry    time.Time `json:"next_attempt_at,omitzero"`
	Attempting bool      `json:"attempting"`
	LastError  string    `json:"last_error,omitempty"`
}

func runStatus(ctx context.Context, args []string, d Deps, cfg Config, c *Checker) int {
	fs := newFlags("status", d)
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	names := sortedRealms(cfg)
	out := make([]statusRealm, 0, len(names))
	for _, n := range names {
		res, err := c.Check(ctx, Request{Realm: n})
		if err != nil {
			return reportErr(d, err)
		}
		st, _ := c.Store.Load(n)
		att, _, _ := c.Store.Attempting(n)
		out = append(out, statusRealm{
			Realm: n, Ignored: c.Store.Ignored(n), Available: res.Available, Reason: res.Reason,
			Since: res.Since, Failures: st.ConsecutiveFailures, OpenUntil: st.OpenUntil,
			Attempts: st.Attempts, NextTry: st.NextAttemptAt, Attempting: att, LastError: res.LastError,
		})
	}
	if *asJSON {
		_ = json.NewEncoder(d.Stdout).Encode(out)
		return ExitOK
	}
	if len(out) == 0 {
		fmt.Fprintln(d.Stdout, "no realms configured")
	}
	for _, r := range out {
		state := "available"
		if !r.Available {
			state = "unavailable"
		}
		fmt.Fprintf(d.Stdout, "%s: %s (%s)", r.Realm, state, r.Reason)
		if r.Ignored {
			fmt.Fprint(d.Stdout, " ignored")
		}
		if r.Attempting {
			fmt.Fprint(d.Stdout, " attempting")
		}
		if !r.Since.IsZero() {
			fmt.Fprintf(d.Stdout, " since %s", r.Since.UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(d.Stdout)
	}
	return ExitOK
}

func sortedRealms(cfg Config) []string {
	names := make([]string, 0, len(cfg.Realms))
	for n := range cfg.Realms {
		names = append(names, n)
	}
	sortStrings(names)
	return names
}

func runIgnore(args []string, d Deps, cfg Config, s *Store, ignore bool) int {
	if len(args) != 1 {
		fmt.Fprintln(d.Stderr, "pg-origin-guard: ignore and unignore take exactly one REALM")
		return ExitUsage
	}
	if _, ok := cfg.Realms[args[0]]; !ok {
		fmt.Fprintf(d.Stderr, "pg-origin-guard: unknown realm %q\n", args[0])
		return ExitUsage
	}
	if err := s.SetIgnored(args[0], ignore); err != nil {
		return reportErr(d, err)
	}
	return ExitOK
}

func runProxy(ctx context.Context, args []string, d Deps) int {
	fs := newFlags("proxy", d)
	realm := fs.String("realm", "", "realm name")
	termWait := fs.Duration("terminal-wait", 0, "how long a terminal caller waits before preempting (default 30s)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	argv := fs.Args()
	if *realm == "" || len(argv) == 0 {
		fmt.Fprintln(d.Stderr, "pg-origin-guard: proxy needs --realm R -- CMD ARGS...")
		return ExitUsage
	}

	cfg, cerr := loadConfig(d)
	var p *Proxy
	if cerr == nil {
		c := NewChecker(cfg, d.Env, d.Dialer, d.Clock)
		var pres PresenceSource = NewFilePresence(cfg.Presence, d.Clock)
		if d.Presence != nil {
			pres = d.Presence
		}
		p = &Proxy{
			Config: cfg, Clock: d.Clock, Store: c.Store, Certs: c.Certs, Rec: c.Rec,
			Presence: pres, Spawner: d.Spawner, Procs: d.Procs, Timers: d.Timers,
			HasTerminal: d.HasTerminal, StderrIsTerminal: d.StderrIsTerminal,
			Stderr: d.Stderr, Signals: d.Signals, TerminalWait: *termWait,
		}
		return p.Run(ctx, *realm, argv)
	}
	// A config fault fails open: run the command unchanged, say so in events.
	cfg = DefaultConfig(d.Env)
	sink := &FileSink{Dir: d.Env.StateDir(), Clock: d.Clock, RetentionDays: cfg.Events.RetentionDays}
	_ = sink.Emit(Event{Time: d.Clock.Now(), Event: EventGuardFault, Realm: *realm, Fault: "config"})
	fmt.Fprintf(d.Stderr, "pg-origin-guard: %v; running the command unguarded\n", cerr)
	p = &Proxy{
		Spawner: d.Spawner, Stderr: d.Stderr, Signals: d.Signals, Clock: d.Clock,
		Timers: d.Timers, StderrIsTerminal: d.StderrIsTerminal, HasTerminal: d.HasTerminal,
	}
	return p.runPlain(ctx, argv)
}

func sortStrings(s []string) { slices.Sort(s) }
