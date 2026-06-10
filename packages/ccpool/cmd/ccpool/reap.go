package main

import (
	"context"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
)

func runReap(args []string) int {
	_ = args
	cfg, err := config.Load()
	if err != nil {
		return 1
	}
	svc, st, code := buildService()
	if code != 0 {
		return code
	}
	defer st.Close()
	if err := svc.Reap(context.Background(), cfg.Pool.MaxSessions, time.Duration(cfg.Pool.IdleTTL)); err != nil {
		return 1
	}
	return 0
}
