package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/ledger"
)

// cmdCost reports the persisted classifier cost ledger (bead pg2-ohvpk
// requirement 3): cumulative spend by run, written AS EACH RUN PROGRESSED,
// not only once it finished — so a killed run's spend is still accounted
// for. Only *classify.CLI runs are ever recorded; the baseline rule makes no
// model calls and has nothing to record.
func cmdCost(_ context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("cost", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "", "include runs started at ts >= this ISO-8601 prefix")
	until := fs.String("until", "", "include runs started at ts < this ISO-8601 prefix (exclusive)")
	ledgerPath := fs.String("ledger", "", "cost ledger path (default $"+ledger.EnvPath+", else beside the index)")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit cost — the persisted classifier cost ledger

Reports cumulative classifier spend BY RUN. Each run's spend is written as it
progressed — after every batch, not only once the run finished — so a run
that was killed mid-flight (a timeout, a SIGTERM, a closed terminal) still has
its calls and $ accounted for here, up to whatever it actually paid for
before it stopped. A run's LAST recorded snapshot showing done=false is
exactly that: a run that never reached its own end.

FLAGS
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	path := *ledgerPath
	if path == "" {
		p, err := ledger.DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}
	entries, err := ledger.Load(path)
	if err != nil {
		if os.IsNotExist(unwrapPathErr(err)) {
			if *asJSON {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				return enc.Encode([]ledger.Entry{})
			}
			fmt.Fprintf(stdout, "cost ledger: %s (does not exist yet — no classification pass has recorded spend)\n", path)
			return nil
		}
		return err
	}

	runs := ledger.Latest(entries)
	var filtered []ledger.Entry
	for _, r := range runs {
		ts := r.StartedAt.UTC().Format(time.RFC3339)
		if *since != "" && ts < *since {
			continue
		}
		if *until != "" && ts >= *until {
			continue
		}
		filtered = append(filtered, r)
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(filtered)
	}

	fmt.Fprintf(stdout, "cost ledger: %s\n", path)
	fmt.Fprintf(stdout, "%-40s %-10s %-20s %6s %6s %10s %-5s\n",
		"run_id", "command", "classifier", "calls", "batch", "usd", "done")
	var totalUSD float64
	var totalCalls int
	for _, r := range filtered {
		fmt.Fprintf(stdout, "%-40s %-10s %-20s %6d %6d %10.4f %-5t\n",
			r.RunID, r.Command, r.Classifier, r.Calls, r.Batches, r.USD, r.Done)
		totalUSD += r.USD
		totalCalls += r.Calls
	}
	fmt.Fprintf(stdout, "\n%d run(s), %d call(s) total, $%.4f total\n", len(filtered), totalCalls, totalUSD)
	return nil
}
