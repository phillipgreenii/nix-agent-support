package main

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/pg-gwm/internal/manager"
)

func (a *app) info(args []string) int {
	fs := newFlagSet("info")
	var c commonFlags
	c.register(fs, true)
	var k manager.InfoKey
	fs.StringVar(&k.Path, "path", "", "look up by worktree path")
	fs.StringVar(&k.ID, "id", "", "look up by worktree id")
	fs.StringVar(&k.LeaseID, "lease-id", "", "look up by lease id")
	fs.StringVar(&k.Name, "name", "", "look up by lease name")
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
		return a.emitErr(format, printPath, manager.ErrUsage(fmt.Sprintf("at most one key, got %d arguments", len(pos)), "", nil))
	}
	if len(pos) == 1 {
		k.Raw = pos[0]
	}
	ctx, stop := cmdContext()
	defer stop()
	m, _, err := a.openManager(ctx, c.repo)
	if err != nil {
		return a.emitErr(format, printPath, err)
	}
	rec, err := m.Info(ctx, k)
	if err != nil {
		return a.emitErr(format, printPath, err)
	}
	return a.emitRecord(format, printPath, rec)
}

func (a *app) list(args []string) int {
	fs := newFlagSet("list")
	var c commonFlags
	c.register(fs, false)
	var unmanaged bool
	fs.BoolVar(&unmanaged, "include-unmanaged", false, "also list worktrees pg-gwm does not manage")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return a.usageErr(err)
	}
	format, ferr := c.resolveFormat(a.stdoutIsTerminal())
	if ferr != nil {
		return a.usageErr(ferr)
	}
	if len(pos) > 0 {
		return a.emitErr(format, false, manager.ErrUsage("list takes no arguments", "", nil))
	}
	ctx, stop := cmdContext()
	defer stop()
	m, _, err := a.openManager(ctx, c.repo)
	if err != nil {
		return a.emitErr(format, false, err)
	}
	recs, err := m.List(ctx, unmanaged)
	if err != nil {
		return a.emitErr(format, false, err)
	}
	return a.emit(format, recs)
}
