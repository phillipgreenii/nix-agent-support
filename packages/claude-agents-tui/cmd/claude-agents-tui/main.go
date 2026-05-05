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

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/caffeinate"
	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
	"github.com/phillipgreenii/claude-agents-tui/internal/config"
	"github.com/phillipgreenii/claude-agents-tui/internal/headless"
	"github.com/phillipgreenii/claude-agents-tui/internal/poller"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/tui"
)

var version = "dev"

func main() {
	waitMode := flag.Bool("wait-until-idle", false, "headless: wait until all sessions idle")
	maxWaitS := flag.Int("maximum-wait", 0, "headless: maximum wait in seconds (0 = use config)")
	intervalS := flag.Int("time-between-checks", 0, "headless: poll interval in seconds (0 = use config)")
	consecutive := flag.Int("consecutive-idle-checks", 0, "headless: consecutive idle checks before exit (0 = use config)")
	caffeinateFlag := flag.Bool("caffeinate", false, "headless: keep Mac awake during wait")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()

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
		})
		os.Exit(code)
	}

	// interactive TUI
	proc := &caffeinate.Proc{}
	mgr := &caffeinate.Manager{
		Grace: cfg.CaffeinateGrace,
		Spawn: proc.Spawn,
		Kill:  proc.Kill,
		Now:   time.Now,
		PID:   os.Getpid(),
	}
	model := tui.NewModel(tui.Options{
		Tree:       &aggregate.Tree{},
		Poller:     p,
		Interval:   cfg.RefreshInterval,
		Caffeinate: mgr,
	})
	prog := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
