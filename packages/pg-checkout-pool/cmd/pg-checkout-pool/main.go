// pg-checkout-pool manages a bounded pool of reusable repository checkouts:
// lease, renew, release and reap on top of the slot store and the worktree
// provisioner, plus a read-only status command.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
)

// Version is injected at build time by mkGoApp.
var Version = "dev"

const rootEnv = "PG_CHECKOUT_POOL_ROOT"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, pool.SystemClock{}))
}

func run(args []string, stdout, stderr io.Writer, clock pool.Clock) int {
	return newApp(stdout, stderr, clock).run(args)
}

func defaultRoot() string {
	if v := os.Getenv(rootEnv); v != "" {
		return v
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "pg-checkout-pool")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pg-checkout-pool")
}

const usage = `usage: pg-checkout-pool <command> [flags]

commands:
  lease    --repo DIR --ref REF --owner SESSION-ID [--bead ID] [--branch NAME]
           [--ttl 30m] [--wait 2m] [--reuse] [--max-slots 6] [--min-free-bytes N] [--keep PATH]...
  renew    --lease ID [--ttl 30m]
  release  --lease ID [--disposition reset|keep|quarantine] [--keep PATH]...
  prewarm  --repo DIR --ref REF --count N [--delay 30s] [--max-slots 6] [--min-free-bytes N]
  revalidate --repo DIR
  reap     [--dry-run] [--grace DUR] [--keep PATH]...
  status   [--json]
  metrics  [--out FILE]
  serve    [--addr 127.0.0.1:9148]   loopback /metrics, rendered on each scrape
  version

every command takes --root DIR (env ` + rootEnv + `). Output is JSON except status and metrics.
exit codes: 0 ok; 2 usage; 3 pool-exhausted; 4 low-disk; 5 ref-unresolvable;
6 quarantine-only; 7 git-wedged; 8 lease-not-found; 9 internal error;
10 slot-busy; 11 owner-already-leases (lease --reuse returns the owner's live lease instead)`

func (a *app) run(args []string) int {
	stderr, stdout, clock := a.stderr, a.stdout, a.clock
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stderr, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, Version)
		return 0
	case "status":
		return status(args[1:], stdout, stderr, clock)
	case "metrics":
		return a.metrics(args[1:])
	case "serve":
		return a.serve(args[1:], nil)
	case "lease":
		return a.lease(args[1:])
	case "renew":
		return a.renew(args[1:])
	case "release":
		return a.release(args[1:])
	case "reap":
		return a.reap(args[1:])
	case "prewarm":
		return a.prewarm(args[1:])
	case "revalidate":
		return a.revalidate(args[1:])
	}
	fmt.Fprintf(stderr, "pg-checkout-pool: unknown command %q\n", args[0])
	return 2
}

func status(args []string, stdout, stderr io.Writer, clock pool.Clock) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", defaultRoot(), "pool state directory (env "+rootEnv+")")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	sts, err := pool.NewStore(*root, clock).Status()
	if err != nil {
		fmt.Fprintf(stderr, "pg-checkout-pool: %v\n", err)
		return exitInternal
	}
	if *asJSON {
		if sts == nil {
			sts = []pool.SlotStatus{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(sts); err != nil {
			fmt.Fprintf(stderr, "pg-checkout-pool: %v\n", err)
			return exitInternal
		}
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SLOT\tSTATE\tOWNER\tBEAD-HINT\tCOMMIT\tEXPIRY\tHELD\tLEAK")
	for _, s := range sts {
		r := s.Record
		exp := "-"
		if !r.Expiry.IsZero() {
			exp = r.Expiry.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%t\t%t\n",
			r.Slot, r.State, dash(r.OwnerSessionID), dash(r.BeadHint), dash(short(r.Commit)), exp, s.Held, s.Leak)
	}
	_ = tw.Flush()
	return 0
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
