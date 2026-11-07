// pg-checkout-pool manages a bounded pool of reusable repository checkouts.
// This version ships the slot store and a read-only `status` command; leasing
// and resetting arrive with the provisioner.
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

func run(args []string, stdout, stderr io.Writer, clock pool.Clock) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stderr, "usage: pg-checkout-pool status [--root DIR] [--json]\n       pg-checkout-pool version")
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
		return 1
	}
	if *asJSON {
		if sts == nil {
			sts = []pool.SlotStatus{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(sts); err != nil {
			fmt.Fprintf(stderr, "pg-checkout-pool: %v\n", err)
			return 1
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
