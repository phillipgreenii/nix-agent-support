package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	env := envFlag{}
	fs.Var(env, "env", "extra env KEY=VAL injected into the session (repeatable)")
	pos := parseInterspersed(fs, args) // flags may follow the positional name
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool new <name> [--cwd dir] [--model m] [--env KEY=VAL ...]")
		return 2
	}
	name := pos[0]

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
		Sleep:     time.Sleep,
	})

	h, err := svc.Ensure(context.Background(), name, dir, m, session.EnsureOpts{Env: env})
	if err != nil {
		fmt.Fprintln(os.Stderr, "new:", err)
		return 1
	}
	fmt.Printf("%s\t%s\t%s\n", h.Name, h.State, shortUUID(h.UUID))
	return 0
}

// envFlag collects repeated `--env KEY=VAL` into a map. Implements flag.Value so
// `ccpool new` can take --env any number of times (pr-pool injects one per key).
type envFlag map[string]string

func (e envFlag) String() string { return "" }

func (e envFlag) Set(kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("invalid --env %q, want KEY=VAL", kv)
	}
	e[k] = v
	return nil
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
