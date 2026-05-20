package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/poller"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/headless"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/tui"
)

var version = "dev"

// pickSubcommand inspects os.Args-style input and returns the subcommand
// name plus the remaining args (minus the subcommand token).
//
// Rules:
//   - If args[1] is a known subcommand name, that wins; the rest are its args.
//   - Otherwise the command is "tui" and args[1:] are its args.
//   - The flag-first case (e.g. --wait-until-idle) routes to tui because
//     no current TUI flags collide with a subcommand name.
func pickSubcommand(args []string) (cmd string, rest []string) {
	known := map[string]bool{
		"daemon":                     true,
		"status":                     true,
		"agents-busy-check":          true,
		"wait-until-agents-finished": true,
		"config":                     true,
		"caffeinate":                 true,
		"nudge":                      true,
		"info":                       true,
		"cmux-bridge":                true,
	}
	if len(args) < 2 {
		return "tui", nil
	}
	if known[args[1]] {
		return args[1], args[2:]
	}
	return "tui", args[1:]
}

func main() {
	cmd, rest := pickSubcommand(os.Args)
	switch cmd {
	case "daemon":
		runDaemon(rest)
	case "status":
		runStatus(rest)
	case "agents-busy-check":
		runAgentsBusyCheck(rest)
	case "wait-until-agents-finished":
		runWaitUntilAgentsFinished(rest)
	case "config":
		runConfigSubcommand(rest)
	case "caffeinate":
		runCaffeinate(rest)
	case "nudge":
		runNudge(rest)
	case "info":
		runInfo(rest)
	case "cmux-bridge":
		runCmuxBridge(rest)
	case "tui":
		runTUI(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		os.Exit(2)
	}
}

// runConfigSubcommand dispatches `config show` (only "show" supported v1).
func runConfigSubcommand(args []string) {
	if len(args) == 0 || args[0] == "show" {
		runConfigShow(args)
		return
	}
	fmt.Fprintf(os.Stderr, "config: unknown action %q (only 'show' supported)\n", args[0])
	os.Exit(2)
}

func runTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	waitMode := fs.Bool("wait-until-idle", false, "headless: wait until all sessions idle")
	maxWaitS := fs.Int("maximum-wait", 0, "headless: maximum wait in seconds (0 = use config)")
	intervalS := fs.Int("time-between-checks", 0, "headless: poll interval in seconds (0 = use config)")
	consecutive := fs.Int("consecutive-idle-checks", 0, "headless: consecutive idle checks before exit (0 = use config)")
	caffeinateFlag := fs.Bool("caffeinate", false, "headless: keep Mac awake during wait")
	remoteMode := fs.Bool("remote", false, "fetch state from the running daemon over gRPC instead of polling locally")
	showVersion := fs.Bool("version", false, "print version")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if *showVersion {
		fmt.Println("pa-monitor", version)
		return
	}

	if *remoteMode {
		runTUIRemote()
		return
	}

	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}

	home, _ := os.UserHomeDir()

	// ccusage is slow (~5–20s to parse a busy ~/.claude/projects tree), so
	// we run it on a 60s background ticker and serve the poll hot path from
	// a cache. The first poll returns nil (→ "5h Block (unavailable)") until
	// the first refresh succeeds.
	ccusageCache := ccusage.NewCachedRunner(60*time.Second, 60*time.Second,
		func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(ctx, "ccusage", "blocks", "--active", "--json", "--offline").Output()
		})
	ccusageCache.Start(context.Background())

	prCache := session.NewPRCache(session.DefaultPRCachePath())

	signalers := signal.DefaultSignalers()

	p := &poller.Poller{
		SessionsDir:      session.DefaultSessionsDir(),
		ClaudeHome:       filepath.Join(home, ".claude"),
		PidAlive:         session.DefaultPidAlive,
		PlanTier:         cfg.PlanTier,
		WorkingThreshold: cfg.WorkingThreshold,
		IdleThreshold:    cfg.IdleThreshold,
		BurnWindowShort:  cfg.BurnWindowShort,
		BurnWindowLong:   cfg.BurnWindowLong,
		Now:              time.Now,
		CCUsageFn:        ccusageCache.Get,
		CCUsageStateFn:   func() (bool, error) { return ccusageCache.Probed(), ccusageCache.LastErr() },
		PRLookupFn:       prCache.Get,
		Signalers:        signalers,
	}

	if *waitMode {
		maxWait := cfg.MaximumWait
		if *maxWaitS > 0 {
			maxWait = time.Duration(*maxWaitS) * time.Second
		}
		interval := cfg.HeadlessInterval
		if *intervalS > 0 {
			interval = time.Duration(*intervalS) * time.Second
		}
		idle := cfg.ConsecutiveIdleChecks
		if *consecutive > 0 {
			idle = *consecutive
		}
		var proc *caffeinate.Proc
		if *caffeinateFlag {
			proc = &caffeinate.Proc{}
			_ = proc.Spawn(os.Getpid())
			defer func() { _ = proc.Kill() }()
		}
		code := headless.Run(context.Background(), headless.Opts{
			Poller:                p,
			Interval:              interval,
			ConsecutiveIdleChecks: idle,
			Maximum:               maxWait,
			Writer:                os.Stdout,
			CmuxSidebarEnable:     cfg.CmuxSidebarEnable,
		})
		os.Exit(code)
	}

	// interactive TUI
	proc := &caffeinate.Proc{}
	defer func() { _ = proc.Kill() }()
	mgr := &caffeinate.Manager{
		Grace:   cfg.CaffeinateGrace,
		Spawn:   proc.Spawn,
		Kill:    proc.Kill,
		IsAlive: proc.IsAlive,
		Now:     time.Now,
		PID:     os.Getpid(),
	}
	cacheDir := filepath.Join(home, ".cache", "pa-monitor")
	errLog := &tui.ErrorLogger{CacheDir: cacheDir}

	reporter := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable: cfg.CmuxSidebarEnable,
		Logf:   errLog.LogString,
	})

	model := tui.NewModel(tui.Options{
		Tree:                 &aggregate.Tree{},
		Poller:               p,
		Interval:             cfg.RefreshInterval,
		Caffeinate:           mgr,
		CacheDir:             cacheDir,
		Signalers:            signalers,
		AutoResumeDelay:      cfg.AutoResumeDelay,
		AutoResumeMessage:    cfg.AutoResumeMessage,
		Reporter:             reporter,
		SidebarIntervalTicks: cfg.CmuxSidebarIntervalTicks,
		ErrorLogger:          errLog,
	})
	prog := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
