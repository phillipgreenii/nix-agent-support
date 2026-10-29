package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/events"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/ingest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/store"
)

// runIngest reads the event files into the database and prints one line per
// file plus a total. Skipped lines do not change the exit status: they are
// reported, not fatal.
func runIngest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "database path (default $XDG_STATE_HOME/claude-tool-tracker/tracker.db)")
	dir := fs.String("events-dir", "", "event files directory (default $XDG_STATE_HOME/claude-tool-tracker/events)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "claude-tool-tracker ingest: unexpected argument", fs.Arg(0))
		return 2
	}
	if *dbPath == "" {
		p, err := store.DefaultPath()
		if err != nil {
			fmt.Fprintln(stderr, "claude-tool-tracker ingest:", err)
			return 1
		}
		*dbPath = p
	}
	if *dir == "" {
		d, err := events.Dir()
		if err != nil {
			fmt.Fprintln(stderr, "claude-tool-tracker ingest:", err)
			return 1
		}
		*dir = d
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintln(stderr, "claude-tool-tracker ingest:", err)
		return 1
	}
	defer db.Close()

	res, err := ingest.Run(db, *dir, time.Now())
	var total ingest.FileResult
	if res != nil {
		for _, f := range res.Files {
			fmt.Fprintf(stdout, "%s\tlines=%d new=%d duplicate=%d skipped_invalid_json=%d skipped_missing_field=%d\n",
				f.File, f.LinesRead, f.EventsNew, f.EventsDuplicate, f.SkippedInvalidJSON, f.SkippedMissingField)
			total.LinesRead += f.LinesRead
			total.EventsNew += f.EventsNew
			total.EventsDuplicate += f.EventsDuplicate
			total.SkippedInvalidJSON += f.SkippedInvalidJSON
			total.SkippedMissingField += f.SkippedMissingField
		}
		fmt.Fprintf(stdout, "total\tfiles=%d lines=%d new=%d duplicate=%d skipped_invalid_json=%d skipped_missing_field=%d\n",
			len(res.Files), total.LinesRead, total.EventsNew, total.EventsDuplicate, total.SkippedInvalidJSON, total.SkippedMissingField)
	}
	if err != nil {
		fmt.Fprintln(stderr, "claude-tool-tracker ingest:", err)
		return 1
	}
	return 0
}
