package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func (a *app) coverage(args []string) int {
	var c common
	var view string
	var wait bool
	var timeout time.Duration
	fs := newFlagSet("coverage", a.errw)
	c.bind(fs)
	fs.StringVar(&view, "view", "summary", "summary or files")
	fs.BoolVar(&wait, "wait-caught-up", false, "poll until the recorder is caught up")
	fs.DurationVar(&timeout, "timeout", 10*time.Minute, "how long --wait-caught-up waits (exit 6 when it expires)")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	ctx, cancel := context.WithTimeout(a.ctx, timeout)
	defer cancel()
	var cov wire.Coverage
	for {
		cov = wire.Coverage{}
		if err := cl.Post(a.ctx, "coverage", wire.CoverageRequest{View: view}, &cov); err != nil {
			return a.emitWireError(c.jsonOut, err, cfg.Socket)
		}
		if !wait || cov.CaughtUp {
			break
		}
		select {
		case <-ctx.Done():
			a.printCoverage(cov, c.jsonOut)
			return a.emitErrorFor(c.jsonOut, wire.ErrWaitTimeout, fmt.Sprintf("not caught up after %s", timeout), cfg.Socket)
		case <-time.After(a.poll()):
		}
	}
	a.printCoverage(cov, c.jsonOut)
	if cov.Degraded && !c.partialOK {
		return a.emitErrorFor(c.jsonOut, wire.ErrBehind, "the recorder is still behind (first backfill)", cfg.Socket)
	}
	return wire.ExitOK
}

func (a *app) printCoverage(cov wire.Coverage, jsonOut bool) {
	if jsonOut {
		b, _ := json.Marshal(cov)
		_, _ = fmt.Fprintln(a.out, string(b))
		return
	}
	_, _ = fmt.Fprintf(a.out, "caught up: %t\nfiles: %d\ncopy lag: %d bytes max, %.0fs max\nparse lag: %d bytes max\nbad lines: %d, unknown keys: %d, unparsed kinds: %d\nheld: %d, settled: %d, removed: %d\n",
		cov.CaughtUp, cov.Files, cov.CopyLagBytesMax, cov.CopyLagSecondsMax, cov.ParseLagBytesMax,
		cov.BadLines, cov.UnknownKeys, cov.UnparsedKinds, cov.Held, cov.Settled, cov.Removed)
	if cov.Worst != nil {
		_, _ = fmt.Fprintf(a.out, "worst: %s (%d bytes behind)\n", cov.Worst.Path, cov.Worst.CopyLagBytes)
	}
	if b := cov.Backfill; b != nil && b.Running {
		_, _ = fmt.Fprintf(a.out, "backfill: %d of %d files copied, about %.0fs left\n", b.Copied, b.Total, b.EtaS)
	}
	for _, r := range cov.Roots {
		_, _ = fmt.Fprintf(a.out, "root: %s present=%t files=%d\n", r.Path, r.Present, r.Files)
	}
	for _, f := range cov.FileList {
		_, _ = fmt.Fprintf(a.out, "file %d %s %s size=%d copied=%d parsed=%d lag=%d bytes %.0fs\n",
			f.FileID, f.Source, f.Path, f.Size, f.CopyOffset, f.ParseOffset, f.CopyLagBytes, f.CopyLagS)
	}
}

func (a *app) poll() time.Duration {
	if a.pollEvery > 0 {
		return a.pollEvery
	}
	return time.Second
}
