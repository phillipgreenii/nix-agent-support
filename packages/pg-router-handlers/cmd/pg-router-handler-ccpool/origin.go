package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/eventlog"
	"github.com/phillipgreenii/pg-router-handlers/internal/originguard"
	"github.com/phillipgreenii/pg-router-handlers/internal/originprobe"
	"github.com/phillipgreenii/pg-router-handlers/internal/rolefile"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
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
//
// Two checks, either of which declines: the handler's own active reachability
// probe (per watched origin, flap controlled), and pg-origin-guard's Check for
// the certificate-and-presence part (per realm). A guard fault (an error from
// check) fails OPEN: the dispatch proceeds and the fault is logged.
func originGate(ctx context.Context, p *originprobe.Prober, check originguard.Check, role roles.Role, cfg config.Config, beadID string) (reason string, declined bool) {
	root := dispatchRepoRoot(role, cfg)
	if w, ok := p.Resolve(root); ok {
		if d := p.Check(ctx, w); d.Gated {
			slog.Warn("dispatch declined: origin unavailable", "role", role.Name, "bead", beadID,
				"origin", w.Key, "class", string(d.State.Class), "since", d.State.Since,
				"consecutive_failures", d.State.ConsecutiveFailures, "stderr_tail", d.State.LastError)
			return busyReasonOriginUnavailable, true
		}
	}
	if root == "" || check == nil || notAGitCheckout(role, root) {
		return "", false
	}
	v, err := check(ctx, root)
	if err != nil {
		slog.Warn("origin guard fault; dispatching anyway", "role", role.Name, "bead", beadID, "err", err)
		return "", false
	}
	if v.Available {
		return "", false
	}
	slog.Warn("dispatch declined: origin guard reports auth unavailable", "role", role.Name, "bead", beadID, "reason", v.Reason)
	return busyReasonOriginUnavailable, true
}

// notAGitCheckout reports whether root is the fixed directory of a "path"
// isolation role that exists but is not a git checkout (the pn-workspace root a
// drain worker runs from). It has no origin for the guard to read, and the
// guard's .git-exists check would misreport it as mount-missing and decline
// every dispatch. A root that does not exist at all is NOT exempt: that is a
// missing mount, which the guard must still report.
func notAGitCheckout(role roles.Role, root string) bool {
	if role.CCPool == nil || role.CCPool.Isolation.Type != "path" {
		return false
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return false
	}
	_, err := os.Lstat(filepath.Join(root, ".git"))
	return err != nil
}

// writeGuardStatus lists, below the probe table, each repo root a dispatch
// consults pg-origin-guard for: the launch config's repoRoot and the fixed path
// of each --role file's path isolation. The probe table only covers
// originProbe.origins, but the guard can decline roots that are not listed
// there. Guard Check reads local state only (no network, no login).
func writeGuardStatus(w io.Writer, cfg config.Config, check originguard.Check, roleFiles []string) error {
	var roots []string
	seen := map[string]bool{}
	add := func(r string) {
		if r != "" && !seen[r] {
			seen[r] = true
			roots = append(roots, r)
		}
	}
	add(cfg.RepoRoot)
	for _, f := range roleFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		role, err := rolefile.DecodeRole(f, data, false)
		if err != nil {
			return err
		}
		if role.CCPool != nil && notAGitCheckout(role, dispatchRepoRoot(role, cfg)) {
			continue
		}
		add(dispatchRepoRoot(role, cfg))
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nGUARD_ROOT\tAVAILABLE\tREASON")
	for _, r := range roots {
		if check == nil {
			fmt.Fprintf(tw, "%s\t-\tguard not configured\n", r)
			continue
		}
		v, err := check(context.Background(), r)
		if err != nil {
			fmt.Fprintf(tw, "%s\t-\tguard fault: %v\n", r, err)
			continue
		}
		fmt.Fprintf(tw, "%s\t%t\t%s\n", r, v.Available, v.Reason)
	}
	return tw.Flush()
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
	var roleFiles stringList
	fs.Var(&roleFiles, "role", "role config JSON whose repo root status also lists (repeatable; origin status only)")
	cfgPath := fs.String("config", os.Getenv(rolefile.EnvConfig), "path to this process's own launch config JSON (or "+rolefile.EnvConfig+")")
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
	cfg, err := rolefile.LoadConfig(*cfgPath)
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
		if err := writeGuardStatus(stdout, cfg, newGuardCheck(cfg), roleFiles); err != nil {
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

type stringList []string

func (l *stringList) String() string     { return fmt.Sprint([]string(*l)) }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func watched(p *originprobe.Prober, key string) bool {
	for _, o := range p.Origins() {
		if o.Key == key {
			return true
		}
	}
	return false
}

const originHelp = `pg-router-handler-ccpool origin — per-origin availability probe (operator tool).

usage: pg-router-handler-ccpool origin <status|ignore|unignore> [--config FILE] [--role FILE]... [key]

  status         list watched origins with class, gated, ignored, since, failures,
                 and a redacted last-error tail (reads state files; never probes),
                 then each repo root pg-origin-guard is consulted for (the
                 config repoRoot and each --role FILE's path) with its verdict
  ignore <key>   write the kill-switch file: the probe becomes a no-op for that
                 origin and never declines a dispatch
  unignore <key> remove the kill-switch file

There is no "pause an origin" verb: the gate auto-clears on the first ok probe,
so a hand-set pause would fight it. Use the global pg-router pause for that.
`

// newGuardCheck builds the production pg-origin-guard Check for cfg. A package
// variable so a test can substitute a fake without a binary on PATH.
var newGuardCheck = func(cfg config.Config) originguard.Check {
	return originguard.ExecCheck(cfg.OriginProbe.GuardBin)
}
