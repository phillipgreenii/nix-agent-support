package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/pg-ccaudit/internal/query"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

func cmdQuery(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "database path")
	root := fs.String("root", "", "transcript root (for the staleness comparison only)")
	since := fs.String("since", "", "include events with ts >= this ISO-8601 prefix (e.g. 2026-07-22)")
	until := fs.String("until", "", "include events with ts < this ISO-8601 prefix (exclusive)")
	format := fs.String("format", "table", "output format: table, tsv or json")
	noStaleness := fs.Bool("no-staleness", false, "suppress the staleness note on stderr")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit query <name> [args…] — run a named, versioned canned query

READ-ONLY. This command never ingests. It reports how far the index lags the
corpus on stderr and leaves the decision to you.

QUERIES
`)
		for _, q := range query.All() {
			fmt.Fprintf(stderr, "  %-24s v%d  %s\n", q.Usage(), q.Version, q.Doc)
		}
		fmt.Fprintf(stderr, "\nFLAGS\n")
		fs.PrintDefaults()
	}
	rest, perr := parseInterspersed(fs, args)
	if perr != nil {
		return perr
	}
	if len(rest) == 0 {
		fs.Usage()
		return errUsage
	}
	q, err := query.Lookup(rest[0])
	if err != nil {
		return err
	}
	f, err := query.ParseFormat(*format)
	if err != nil {
		return err
	}

	resolvedDB, err := resolveDB(*dbPath)
	if err != nil {
		return err
	}
	db, err := store.OpenReadOnly(resolvedDB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if !*noStaleness {
		resolvedRoot, rerr := resolveRoot(*root)
		if rerr != nil {
			fmt.Fprintf(stderr, "staleness: UNKNOWN — %v\n", rerr)
		} else {
			fmt.Fprintln(stderr, query.Measure(ctx, db, resolvedRoot).Note())
		}
	}

	req := query.Request{
		Query:  q,
		Args:   rest[1:],
		Since:  *since,
		Until:  *until,
		Format: f,
	}
	res, err := query.Run(ctx, db, req)
	if err != nil {
		return err
	}
	return query.Render(stdout, req, res)
}

func cmdQueries(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("queries", flag.ContinueOnError)
	fs.SetOutput(stderr)
	verbose := fs.Bool("verbose", false, "also print each query's interpretation notes and SQL")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	for _, q := range query.All() {
		fmt.Fprintf(stdout, "%-26s v%d  %s\n", q.Usage(), q.Version, q.Doc)
		for _, p := range q.Params {
			fmt.Fprintf(stdout, "  %-12s %s (default %q)\n", p.Name, p.Doc, p.Default)
		}
		if *verbose {
			if q.Notes != "" {
				fmt.Fprintf(stdout, "  notes: %s\n", q.Notes)
			}
			fmt.Fprintf(stdout, "  sql:%s\n", strings.ReplaceAll("\n"+strings.TrimSpace(q.SQL), "\n", "\n    "))
			fmt.Fprintln(stdout)
		}
	}
	return nil
}

func cmdSchema(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	fs.SetOutput(stderr)
	thinking := fs.Bool("thinking", false, "also print the optional thinking table (T-16)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	fmt.Fprint(stdout, strings.TrimSpace(store.Schema)+"\n")
	if *thinking {
		fmt.Fprint(stdout, strings.TrimSpace(store.ThinkingSchema)+"\n")
	}
	return nil
}
