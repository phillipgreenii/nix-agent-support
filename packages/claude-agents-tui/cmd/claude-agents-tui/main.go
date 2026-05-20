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

	"github.com/phillipgreenii/claude-agents-tui/internal/cmuxstatus"
	"github.com/phillipgreenii/claude-agents-tui/internal/config"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/caffeinate"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/ccusage"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/poller"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/headless"
	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
	"github.com/phillipgreenii/claude-agents-tui/internal/tui"
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
	known := map[string]bool{"daemon": true}
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
	case "tui":
		runTUI(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		os.Exit(2)
	}
}

func runTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	waitMode := fs.Bool("wait-until-idle", false, "headless: wait until all sessions idle")
	maxWaitS := fs.Int("maximum-wait", 0, "headless: maximum wait in seconds (0 = use config)")
	intervalS := fs.Int("time-between-checks", 0, "headless: poll interval in seconds (0 = use config)")
	consecutive := fs.Int("consecutive-idle-checks", 0, "headless: consecutive idle checks before exit (0 = use config)")
	caffeinateFlag := fs.Bool("caffeinate", false, "headless: keep Mac awake during wait")
	showVersion := fs.Bool("version", false, "print version")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if *showVersion {
		fmt.Println("claude-agents-tui", version)
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
	cacheDir := filepath.Join(home, ".cache", "claude-agents-tui")
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

func runDaemon(args []string) {
	fmt.Fprintln(os.Stderr, "daemon: not yet implemented")
	os.Exit(1)
}
