package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/lock"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// defaultRetentionDays is the retention window `prune --days` documents and the
// pg-disk-reclaimer registry item passes. 90 days covers the widest window any
// analysis skill (improvement-retro, tool-error-waste-review) looks back over,
// with a margin of about a quarter.
const defaultRetentionDays = 90

func cmdPrune(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "database path")
	days := fs.Int("days", defaultRetentionDays, "remove indexed files whose newest event is older than this many days")
	yes := fs.Bool("yes", false, "actually delete and VACUUM; without it prune is a dry run")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	lockPath := fs.String("lock", "", "advisory lock path (default: alongside the database)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit prune — drop indexed history older than a retention window

Removes every indexed file (a recorder file generation) whose NEWEST event is
older than --days (default %d), with all its rows, then runs VACUUM so the
disk space comes back. A file is removed whole or not at all. The feed cursors
are kept, so ingest does not re-read what was pruned. The gold set file is never
touched.

Without --yes this is a dry run: it counts and deletes nothing. VACUUM needs free
disk space about the size of the database. Takes the ingest lock, so a running
ingest makes it skip (exit 0, nothing done).

FLAGS
`, defaultRetentionDays)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *days <= 0 {
		return fmt.Errorf("--days must be positive")
	}
	resolvedDB, err := resolveDB(*dbPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(resolvedDB); err != nil {
		return fmt.Errorf("index not found at %s: %w", resolvedDB, err)
	}
	if *yes {
		lp := *lockPath
		if lp == "" {
			lp = lock.DefaultPath(resolvedDB)
		}
		handle, err := lock.TryAcquire(lp)
		if err != nil {
			fmt.Fprintf(stdout, "prune skipped: another ingest or prune holds %s\n", lp)
			return nil
		}
		defer func() { _ = handle.Release() }()
	}
	db, err := store.Open(resolvedDB, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	cutoff := time.Now().AddDate(0, 0, -*days)
	res, err := store.Prune(ctx, db, cutoff, !*yes)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	verb := "would remove"
	if !res.DryRun {
		verb = "removed"
	}
	fmt.Fprintf(stdout, "cutoff: %s (%d days)\n%s %d files\n", res.Cutoff, *days, verb, res.Files)
	names := make([]string, 0, len(res.Rows))
	for t := range res.Rows {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		fmt.Fprintf(stdout, "  %-15s %d rows\n", t, res.Rows[t])
	}
	if res.DryRun {
		fmt.Fprintln(stdout, "dry run: pass --yes to delete")
	} else if res.Vacuumed {
		fmt.Fprintln(stdout, "vacuumed")
	}
	return nil
}
