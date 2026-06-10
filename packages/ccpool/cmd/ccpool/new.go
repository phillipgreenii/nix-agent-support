package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/lock"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
	"github.com/phillipgreenii/ccpool/internal/trust"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

func runNew(args []string) int {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	cwd := fs.String("cwd", "", "project dir (default: current dir)")
	model := fs.String("model", "", "claude model")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool new <name> [--cwd dir] [--model m]")
		return 2
	}
	name := fs.Arg(0)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	dir := *cwd
	if dir == "" {
		if cfg.Claude.DefaultCwd != "" {
			dir = cfg.Claude.DefaultCwd
		} else {
			dir, _ = os.Getwd()
		}
	}
	m := *model
	if m == "" {
		m = cfg.Claude.DefaultModel
	}

	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()

	home, _ := os.UserHomeDir()
	svc := session.New(session.Deps{
		Tmux:      tmux.NewClient(cfg.Tmux.Socket),
		Trust:     truster{path: filepath.Join(home, ".claude.json")},
		Store:     st,
		Wait:      storeWaiter{st: st, timeout: time.Duration(cfg.Wait.Timeout)},
		Lock:      lock.New(cfg.RuntimeDir),
		Socket:    cfg.Tmux.Socket,
		Prefix:    cfg.Tmux.Prefix,
		PluginDir: cfg.Claude.PluginDir,
		ClaudeBin: cfg.Claude.Bin,
		NewUUID:   func() string { return uuid.NewString() },
		Now:       time.Now,
	})

	h, err := svc.Ensure(context.Background(), name, dir, m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "new:", err)
		return 1
	}
	fmt.Printf("%s\t%s\t%s\n", h.Name, h.State, shortUUID(h.UUID))
	return 0
}

// truster adapts trust.EnsureTrusted to session.Truster (binds the path).
type truster struct{ path string }

func (t truster) EnsureTrusted(cwd string) error { return trust.EnsureTrusted(t.path, cwd) }

// storeWaiter adapts the store + wait poll loop to session.Waiter.
type storeWaiter struct {
	st      *store.Store
	timeout time.Duration
}

func (w storeWaiter) Wait(ctx context.Context, name string, since int64) (wait.Outcome, error) {
	return wait.ForGenerationAdvance(ctx, w.st, name, since, wait.Opts{Timeout: w.timeout})
}
