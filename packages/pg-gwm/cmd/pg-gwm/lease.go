package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/manager"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// openManager resolves the repository at path (the current directory when
// empty) and builds its Manager from the configuration file. baseDir is the
// absolute path the command was aimed at.
func (a *app) openManager(ctx context.Context, path string) (m *manager.Manager, baseDir string, err error) {
	if path == "" {
		path = "."
	}
	baseDir, err = filepath.Abs(path)
	if err != nil {
		return nil, "", manager.ErrUsage(err.Error(), "", nil)
	}
	r, err := repo.Resolve(ctx, a.git, baseDir)
	switch {
	case errors.Is(err, repo.ErrNotARepo), errors.Is(err, repo.ErrCwdGone):
		return nil, "", manager.ErrUsage(err.Error(), "run inside a repository or pass --repo <path>", map[string]any{"path": baseDir})
	case err != nil:
		return nil, "", err
	}
	file, err := config.Load(config.DefaultPath())
	if err != nil {
		return nil, "", manager.ErrUsage(err.Error(), "run: pg-gwm config check", nil)
	}
	cfg := file.For(r)
	store := state.Open(r.CommonDir)
	store.Retention = cfg.TombstoneRetention
	m = &manager.Manager{
		Repo:     r,
		Cfg:      cfg,
		Store:    store,
		Prov:     provision.NewWorktree(r.Main, a.git, cfg.GitTimeout.D, cfg.CreateTimeout.D),
		Git:      a.git,
		Registry: state.Registry{Path: state.RegistryPath()},
		Counters: state.Counters{Path: state.CountersPath()},
	}
	a.counters, a.label = m.Counters, r.Label
	return m, baseDir, nil
}

// cmdContext is cancelled by SIGINT and SIGTERM.
func cmdContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// usageErr reports a command-line mistake in the text form (the format is
// not yet known when flags fail to parse).
func (a *app) usageErr(err error) int {
	return a.emitErr("text", false, manager.ErrUsage(err.Error(), "see: pg-gwm help", nil))
}

func (a *app) lease(args []string) int {
	fs := newFlagSet("lease")
	var c commonFlags
	c.register(fs, true)
	var (
		req      manager.LeaseRequest
		ttl, wt  string
		newB, rB string
		meta     = kvFlag{}
	)
	fs.StringVar(&req.Name, "name", "", "name of the lease")
	fs.BoolVar(&req.OrRenew, "or-renew", false, "renew the lease if --name holds one")
	fs.StringVar(&req.Owner, "owner", "", "who holds the lease")
	fs.Var(meta, "metadata", "key=value (repeatable)")
	fs.StringVar(&ttl, "ttl", "", "time to live: none or <int><s|m|h|d>")
	fs.StringVar(&newB, "b", "", "create the branch")
	fs.StringVar(&rB, "B", "", "create or reset the branch")
	fs.BoolVar(&req.Detach, "detach", false, "detach HEAD at the commit-ish")
	fs.StringVar(&wt, "wait", "", "wait up to this long for a worktree: <int><s|m|h|d>")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return a.usageErr(err)
	}
	format, ferr := c.resolveFormat(a.stdoutIsTerminal())
	printPath, perr := c.printPath()
	if err := errors.Join(ferr, perr); err != nil {
		return a.usageErr(err)
	}
	if len(pos) > 1 {
		return a.emitErr(format, printPath, manager.ErrUsage(fmt.Sprintf("at most one commit-ish, got %d arguments", len(pos)), "", nil))
	}
	if len(pos) == 1 {
		req.CommitIsh = pos[0]
	}
	if newB != "" && rB != "" {
		return a.emitErr(format, printPath, manager.ErrUsage("-b and -B are mutually exclusive", "", nil))
	}
	req.Branch, req.Create, req.Reset = newB, newB != "", rB != ""
	if rB != "" {
		req.Branch = rB
	}
	if len(meta) > 0 {
		req.Metadata = meta
	}
	if ttl != "" {
		d, err := config.ParseDuration(ttl)
		if err != nil {
			return a.emitErr(format, printPath, manager.ErrUsage(err.Error(), "e.g. --ttl 2h, or --ttl none", map[string]any{"flag": "--ttl"}))
		}
		req.TTL = &d
	}
	if wt != "" {
		d, err := config.ParseDuration(wt)
		if err != nil || d.None {
			return a.emitErr(format, printPath, manager.ErrUsage(fmt.Sprintf("--wait %q: want <int><s|m|h|d>", wt), "", map[string]any{"flag": "--wait"}))
		}
		req.Wait = d.D
	}

	ctx, stop := cmdContext()
	defer stop()
	m, baseDir, err := a.openManager(ctx, c.repo)
	if err != nil {
		return a.emitErr(format, printPath, err)
	}
	req.BaseDir = baseDir
	rec, err := m.Lease(ctx, req)
	if err != nil {
		return a.emitErr(format, printPath, err)
	}
	if printPath {
		fmt.Fprintln(a.stdout, rec.WorktreePath)
		return 0
	}
	return a.emit(format, rec)
}
