package main

import (
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// sweep asks the daemon for a forced full sweep: it empties the skip cache and
// the persisted seen table and starts a sweep now that ingests every file. The
// recorder never does this on a timer; this command is the only trigger.
func (a *app) sweep(args []string) int {
	var c common
	var force, wait bool
	fs := newFlagSet("sweep", a.errw)
	c.bind(fs)
	fs.BoolVar(&force, "force", false, "empty the skip cache and the seen table and sweep every file now (required)")
	fs.BoolVar(&wait, "wait", false, "block until the sweep has finished")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	if fs.NArg() > 0 {
		return a.emitError(c.jsonOut, wire.ErrUsage, "sweep takes no arguments")
	}
	if !force {
		return a.emitError(c.jsonOut, wire.ErrUsage, "sweep needs --force: the periodic full verification was retired and this is how to run it")
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	var ans serve.SweepAnswer
	if err := cl.Post(a.ctx, "sweep", serve.SweepRequest{Force: true}, &ans); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	target := ans.Completed
	if ans.Started {
		target++ // the pass this request began
	}
	for wait && (ans.Running || ans.Completed < target) {
		select {
		case <-a.ctx.Done():
			return a.emitError(c.jsonOut, wire.ErrCancelled, "interrupted; the sweep continues in the daemon")
		case <-time.After(a.poll()):
		}
		if err := cl.Post(a.ctx, "sweep", serve.SweepRequest{}, &ans); err != nil {
			return a.emitWireError(c.jsonOut, err, cfg.Socket)
		}
	}
	if wait && ans.Error != "" {
		return a.emitError(c.jsonOut, wire.ErrInternal, "forced sweep failed: "+ans.Error)
	}
	if c.jsonOut {
		a.printJSON(ans)
		return wire.ExitOK
	}
	switch {
	case wait:
		_, _ = fmt.Fprintln(a.out, "sweep: forced sweep finished")
	case ans.Started:
		_, _ = fmt.Fprintln(a.out, "sweep: forced sweep started (add --wait to block until it finishes)")
	default:
		_, _ = fmt.Fprintln(a.out, "sweep: a forced sweep is already running")
	}
	return wire.ExitOK
}
