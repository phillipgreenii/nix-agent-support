package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/session"
)

func runReap(args []string) int {
	_ = args
	cfg, err := config.Load()
	if err != nil {
		slog.Error("reap: config load failed", "err", err)
		return 1
	}
	svc, st, code := buildService()
	if code != 0 {
		return code
	}
	defer func() { _ = st.Close() }()
	// reap runs unattended (timer) — surface failures so the pool doesn't sit
	// silently ungoverned.
	if err := svc.Reap(context.Background(), cfg.Pool.MaxSessions, time.Duration(cfg.Pool.IdleTTL),
		session.WithDeadNeedsInputTTL(time.Duration(cfg.Pool.DeadNeedsInputTTL))); err != nil {
		slog.Error("reap: failed", "err", err)
		return 1
	}
	return 0
}
