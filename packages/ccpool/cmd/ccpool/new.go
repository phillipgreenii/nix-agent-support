package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/launch"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/trust"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

func runNew(args []string) int {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	cwd := fs.String("cwd", "", "project dir (default: current dir)")
	model := fs.String("model", "", "claude model")
	displayName := fs.String("name", "", "optional display label for the session (claude --name; nullable)")
	env := envFlag{}
	fs.Var(env, "env", "extra env KEY=VAL injected into the session (repeatable)")
	permMode := fs.String("permission-mode", "", "claude --permission-mode value: default|acceptEdits|plan|auto|dontAsk|bypassPermissions (workers need bypassPermissions)")
	effort := fs.String("effort", "", "claude --effort value (e.g. max)")
	pos := parseInterspersed(fs, args) // flags may follow the positional external_id
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool new <external_id> [--name label] [--cwd dir] [--model m] [--env KEY=VAL ...] [--permission-mode m] [--effort v]")
		return 2
	}
	externalID := pos[0]

	// Validate --permission-mode against the documented set BEFORE any I/O. Empty
	// is allowed (omit the flag); an explicit unknown value is a usage error (2),
	// consistent with the other usage failures above.
	if *permMode != "" && !launch.PermissionMode(*permMode).Valid() {
		fmt.Fprintf(os.Stderr, "ccpool new: invalid --permission-mode %q (want one of: default acceptEdits plan auto dontAsk bypassPermissions)\n", *permMode)
		return 2
	}

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

	el := openEventLog(cfg)
	st, err := store.Open(cfg.DBPath, clock.Real{}, store.WithEventLog(el))
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()

	svc := session.New(newSessionDeps(cfg, st, el))

	h, err := svc.Ensure(context.Background(), externalID, dir, m, session.EnsureOpts{
		Env:            env,
		Name:           *displayName,
		PermissionMode: launch.PermissionMode(*permMode),
		Effort:         *effort,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "new:", err)
		return 1
	}
	// Columns: external_id, name, state, short claude_session_id (ADR 0015).
	fmt.Printf("%s\t%s\t%s\t%s\n", h.ExternalID, h.Name, h.State, shortUUID(h.ClaudeSessionID))
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
