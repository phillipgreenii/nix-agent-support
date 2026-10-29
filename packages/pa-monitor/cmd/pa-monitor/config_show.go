package main

import (
	"fmt"
	"os"
)

// runConfigShow implements the `config show` subcommand. Prints the
// loaded config so users can verify nix-rendered values.
func runConfigShow(args []string) {
	// Lightweight — no RPC needed; just load the config file the daemon
	// would load and print key fields.
	cfg, err := configLoad()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config show: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("plan_tier:                 %s\n", cfg.PlanTier)
	fmt.Printf("burn_window_short:         %s\n", cfg.BurnWindowShort)
	fmt.Printf("burn_window_long:          %s\n", cfg.BurnWindowLong)
	fmt.Printf("refresh_interval:          %s\n", cfg.RefreshInterval)
	fmt.Printf("caffeinate_grace:          %s\n", cfg.CaffeinateGrace)
	fmt.Printf("working_threshold:         %s\n", cfg.WorkingThreshold)
	fmt.Printf("idle_threshold:            %s\n", cfg.IdleThreshold)
	fmt.Printf("cmux_sidebar_enable:       %v\n", cfg.CmuxSidebarEnable)
	fmt.Printf("auto_restart_on_version_mismatch: %v\n", cfg.AutoRestartOnVersionMismatch)
	for _, d := range cfg.Decorators {
		fmt.Printf("decorator:                 %s -> %s (timeout %dms, env %d)\n", d.Name, d.Command, d.TimeoutMS, len(d.Env))
	}
}
