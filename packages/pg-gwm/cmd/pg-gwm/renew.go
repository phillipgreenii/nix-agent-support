package main

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/manager"
)

// leaseKey is the lease a renew, release or expire names: one positional lease
// id or --name.
func leaseKey(pos []string, name string) (manager.LeaseKey, error) {
	switch {
	case len(pos) > 1:
		return manager.LeaseKey{}, manager.ErrUsage(fmt.Sprintf("at most one lease id, got %d arguments", len(pos)), "", nil)
	case len(pos) == 1 && name != "":
		return manager.LeaseKey{}, manager.ErrUsage("give a lease id or --name, not both", "", nil)
	case len(pos) == 0 && name == "":
		return manager.LeaseKey{}, manager.ErrUsage("name the lease: a lease id or --name", "e.g. pg-gwm renew --name N", nil)
	}
	if len(pos) == 1 {
		return manager.LeaseKey{LeaseID: pos[0]}, nil
	}
	return manager.LeaseKey{Name: name}, nil
}

// leaseCommand parses and runs renew, release or expire; do runs the engine.
func (a *app) leaseCommand(cmd string, args []string, do func(*manager.Manager, manager.LeaseKey, *config.Duration, map[string]string) (manager.Record, error)) int {
	fs := newFlagSet(cmd)
	var c commonFlags
	c.register(fs, cmd == "renew")
	var (
		name, ttl string
		meta      = kvFlag{}
	)
	fs.StringVar(&name, "name", "", "name of the lease")
	if cmd == "renew" {
		fs.StringVar(&ttl, "ttl", "", "new time to live: <int><s|m|h|d>")
		fs.Var(meta, "metadata", "key=value (repeatable); replaces the lease's metadata")
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return a.usageErr(err)
	}
	format, ferr := c.resolveFormat(a.stdoutIsTerminal())
	printPath, perr := c.printPath()
	if err := errors.Join(ferr, perr); err != nil {
		return a.usageErr(err)
	}
	key, err := leaseKey(pos, name)
	if err != nil {
		return a.emitErr(format, printPath, err)
	}
	var d *config.Duration
	if ttl != "" {
		v, err := config.ParseDuration(ttl)
		if err != nil {
			return a.emitErr(format, printPath, manager.ErrUsage(err.Error(), "e.g. --ttl 2h", map[string]any{"flag": "--ttl"}))
		}
		d = &v
	}
	var md map[string]string
	if len(meta) > 0 {
		md = meta
	}

	ctx, stop := cmdContext()
	defer stop()
	m, _, err := a.openManager(ctx, c.repo)
	if err != nil {
		return a.emitErr(format, printPath, err)
	}
	rec, err := do(m, key, d, md)
	if err != nil {
		return a.emitErr(format, printPath, err)
	}
	return a.emitRecord(format, printPath, rec)
}

// emitRecord prints a successful record, and its warnings to standard error.
func (a *app) emitRecord(format string, printPath bool, rec manager.Record) int {
	for _, w := range rec.Warnings {
		a.warnf("%s", w)
	}
	if printPath {
		fmt.Fprintln(a.stdout, rec.WorktreePath)
		return 0
	}
	return a.emit(format, rec)
}

func (a *app) renew(args []string) int {
	return a.leaseCommand("renew", args, func(m *manager.Manager, k manager.LeaseKey, ttl *config.Duration, md map[string]string) (manager.Record, error) {
		ctx, stop := cmdContext()
		defer stop()
		return m.Renew(ctx, k, ttl, md)
	})
}

func (a *app) release(args []string) int {
	return a.leaseCommand("release", args, func(m *manager.Manager, k manager.LeaseKey, _ *config.Duration, _ map[string]string) (manager.Record, error) {
		ctx, stop := cmdContext()
		defer stop()
		return m.Release(ctx, k)
	})
}

func (a *app) expire(args []string) int {
	return a.leaseCommand("expire", args, func(m *manager.Manager, k manager.LeaseKey, _ *config.Duration, _ map[string]string) (manager.Record, error) {
		ctx, stop := cmdContext()
		defer stop()
		return m.Expire(ctx, k)
	})
}
