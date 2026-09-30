package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/originprobe"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router/conformance"
)

// busyReasonOriginUnavailable is the busy-decline reason a dispatch into a
// gated origin carries (INV-CCH-10, bead pg2-4gi2c). It is a fixed value: the
// origin key goes in the log line, never here, so pg_router_failures{reason}
// stays a bounded label set.
const busyReasonOriginUnavailable = originprobe.ReasonOriginUnavailable

// newOriginProber builds the production Prober for cfg, wiring transitions
// into <stateDir>/origin-events.jsonl. The event log is best effort: if it
// cannot be opened, transitions are still logged through slog.
func newOriginProber(cfg config.Config) *originprobe.Prober {
	p := originprobe.New(cfg.OriginProbe)
	if len(cfg.OriginProbe.Origins) == 0 {
		return p
	}
	if w, err := eventlog.New(filepath.Join(filepath.Dir(p.Dir()), "origin-events.jsonl")); err == nil {
		p.Emit = w.Emit
	} else {
		slog.Warn("origin probe: event log unavailable", "err", err)
	}
	return p
}

// dispatchRepoRoot is the repo root a dispatch of role runs in, or "" when it
// has none this feature can resolve. A command role runs no session in a repo,
// and a workforest spans several, so neither has one and neither is ever
// gated. A "path" role runs in its fixed directory.
func dispatchRepoRoot(role roles.Role, cfg config.Config) string {
	if role.CCPool == nil {
		return ""
	}
	switch role.CCPool.Isolation.Type {
	case "", "worktree", "none":
		return cfg.RepoRoot
	case "path":
		return role.CCPool.Isolation.Path
	default:
		return ""
	}
}

// originGate reports whether a dispatch of role must be declined because the
// origin it runs against is unavailable. It runs BEFORE anything that touches
// beads or ccpool, so a decline mutates nothing. A role with no resolvable
// watched origin is never gated.
func originGate(ctx context.Context, p *originprobe.Prober, role roles.Role, cfg config.Config, beadID string) (reason string, declined bool) {
	w, ok := p.Resolve(dispatchRepoRoot(role, cfg))
	if !ok {
		return "", false
	}
	d := p.Check(ctx, w)
	if !d.Gated {
		return "", false
	}
	slog.Warn("dispatch declined: origin unavailable", "role", role.Name, "bead", beadID,
		"origin", w.Key, "class", string(d.State.Class), "since", d.State.Since,
		"consecutive_failures", d.State.ConsecutiveFailures, "stderr_tail", d.State.LastError)
	return busyReasonOriginUnavailable, true
}

// runOrigin implements the `origin` operator subcommands:
//
//	origin status              list watched origins with class, since, last error
//	origin ignore <key>        write the kill-switch file; the probe never declines
//	origin unignore <key>      remove the kill-switch file
//
// There is deliberately no "pause an origin" verb: the gate auto-clears on the
// first ok probe, so a hand-set pause would fight it. Use the global
// pg-router pause instead.
func runOrigin(args []string) int { return runOriginTo(args, os.Stdout, os.Stderr) }

func runOriginTo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("origin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", os.Getenv(envConfig), "path to this process's own launch config JSON (or "+envConfig+")")
	// flag stops at the first non-flag, so the verb comes first: origin <verb> [flags] [key].
	if len(args) == 0 {
		fmt.Fprintln(stderr, "origin: a verb is required (status|ignore|unignore)")
		return conformance.ExitUsage
	}
	verb, rest := args[0], args[1:]
	if verb == "-h" || verb == "--help" || verb == "help" {
		fmt.Fprint(stdout, originHelp)
		return conformance.ExitOK
	}
	if err := fs.Parse(rest); err != nil {
		fmt.Fprintln(stderr, "origin:", err)
		return conformance.ExitUsage
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "origin:", err)
		return conformance.ExitError
	}
	p := originprobe.New(cfg.OriginProbe)

	switch verb {
	case "status":
		if fs.NArg() > 0 {
			fmt.Fprintln(stderr, "origin status: unexpected argument:", fs.Arg(0))
			return conformance.ExitUsage
		}
		if err := p.WriteStatus(stdout); err != nil {
			fmt.Fprintln(stderr, "origin status:", err)
			return conformance.ExitError
		}
		return conformance.ExitOK
	case "ignore", "unignore":
		if fs.NArg() != 1 {
			fmt.Fprintf(stderr, "origin %s: exactly one <key> is required\n", verb)
			return conformance.ExitUsage
		}
		key := fs.Arg(0)
		if !watched(p, key) {
			fmt.Fprintf(stderr, "origin %s: %q is not a watched origin (see: origin status)\n", verb, key)
			return conformance.ExitUsage
		}
		if verb == "ignore" {
			err = p.Ignore(key)
		} else {
			err = p.Unignore(key)
		}
		if err != nil {
			fmt.Fprintf(stderr, "origin %s: %v\n", verb, err)
			return conformance.ExitError
		}
		fmt.Fprintf(stdout, "%s: %s\n", verb, key)
		return conformance.ExitOK
	default:
		fmt.Fprintf(stderr, "origin: unknown verb %q (want status|ignore|unignore)\n", verb)
		return conformance.ExitUsage
	}
}

func watched(p *originprobe.Prober, key string) bool {
	for _, o := range p.Origins() {
		if o.Key == key {
			return true
		}
	}
	return false
}

const originHelp = `pg-router-ccpool-handler origin — per-origin availability probe (operator tool).

usage: pg-router-ccpool-handler origin <status|ignore|unignore> [--config FILE] [key]

  status         list watched origins with class, gated, ignored, since, failures,
                 and a redacted last-error tail (reads state files; never probes)
  ignore <key>   write the kill-switch file: the probe becomes a no-op for that
                 origin and never declines a dispatch
  unignore <key> remove the kill-switch file

There is no "pause an origin" verb: the gate auto-clears on the first ok probe,
so a hand-set pause would fight it. Use the global pg-router pause for that.
`
