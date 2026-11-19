package main

import (
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// repairDuplicates retires the duplicate append-file rows an earlier reboot left
// behind (same inode, birth time and path, another device), offline. The daemon
// MUST be stopped: the command takes the store exclusively.
func (a *app) repairDuplicates(args []string) int {
	var c common
	fs := newFlagSet("repair-duplicates", a.errw)
	c.bind(fs)
	dry := fs.Bool("dry-run", false, "report what would change and write nothing")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	if fs.NArg() > 0 {
		return a.emitError(c.jsonOut, wire.ErrUsage, "repair-duplicates takes no arguments")
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrBadConfig, err.Error())
	}
	var h wire.Hello
	if cl.Do(a.ctx, "GET", "/v1/hello", nil, &h) == nil {
		return a.emitErrorFor(c.jsonOut, wire.ErrBusy, "the daemon is running on "+cfg.Socket+"; stop it first", cfg.Socket)
	}
	d, err := serve.Open(serve.Options{Config: cfg, Logger: serve.NewLogger(a.errw), Version: Version})
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUnavailable, fmt.Sprintf("cannot open index.db: %v", err))
	}
	rep, rerr := d.Parser().RetireDuplicateAppendRows(*dry)
	if err := d.Close(); err != nil && rerr == nil {
		rerr = err
	}
	if c.jsonOut {
		a.printJSON(rep)
	} else {
		verb := "retired"
		if rep.DryRun {
			verb = "would retire"
		}
		_, _ = fmt.Fprintf(a.out, "%d duplicate group(s): %s %d row(s), moved %d record(s) to the kept row, removed %d duplicate record(s); %d already retired, %d skipped\n",
			rep.Groups, verb, rep.Retired, rep.Repointed, rep.Cleared, rep.AlreadyRetired, len(rep.Skipped))
		for _, s := range rep.Skipped {
			_, _ = fmt.Fprintf(a.out, "skipped file %d (keeper %d) %s: %s\n", s.File, s.Keeper, s.Path, s.Reason)
		}
	}
	if rerr != nil {
		return a.emitError(c.jsonOut, wire.ErrInternal, "repair-duplicates: "+rerr.Error())
	}
	return wire.ExitOK
}
