package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
)

func runReap(args []string) int {
	_ = args
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reap config:", err)
		return 1
	}
	svc, st, code := buildService()
	if code != 0 {
		return code
	}
	defer func() { _ = st.Close() }()
	// reap runs unattended (timer) — surface failures so the pool doesn't sit
	// silently ungoverned.
	if err := svc.Reap(context.Background(), cfg.Pool.MaxSessions, time.Duration(cfg.Pool.IdleTTL)); err != nil {
		fmt.Fprintln(os.Stderr, "reap:", err)
		return 1
	}
	return 0
}
