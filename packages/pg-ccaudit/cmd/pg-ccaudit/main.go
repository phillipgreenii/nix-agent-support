// Command pg-ccaudit indexes Claude Code transcripts into SQLite and answers
// census questions about them from that index.
//
// The index exists so that asking "which tool errors are costing us, and where
// does each fix belong" is a query rather than a full re-scan of a multi-gigabyte
// JSONL corpus. Scanning it raw is what stalled two supervised agent runs; the
// review skill's first instruction is therefore to query this database and never
// to read the transcripts directly.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// Version is injected at build time by the nix Go builder from a per-source
// content digest.
var Version = "dev"

const usage = `pg-ccaudit — index Claude Code transcripts into SQLite and query them

USAGE
  pg-ccaudit <command> [flags]

COMMANDS
  ingest      index new/appended transcripts (incremental, resumable, single-instance)
  status      report index coverage and how far it lags the corpus
  query       run a named, versioned canned query (read-only)
  queries     list the canned queries
  schema      print the database DDL
  version     print the version

COMMON FLAGS
  --db PATH     database path (default $PG_CCAUDIT_DB, else $XDG_DATA_HOME/pg-ccaudit/transcripts.db)
  --root DIR    transcript root (default $PG_CCAUDIT_ROOT, else ~/.claude/projects)

Run 'pg-ccaudit <command> --help' for a command's own flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "pg-ccaudit: %v\n", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

func run(ctx context.Context, args []string, stdout, stderr *os.File) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errUsage
	}
	switch args[0] {
	case "ingest":
		return cmdIngest(ctx, args[1:], stdout, stderr)
	case "status":
		return cmdStatus(ctx, args[1:], stdout, stderr)
	case "query":
		return cmdQuery(ctx, args[1:], stdout, stderr)
	case "queries":
		return cmdQueries(args[1:], stdout, stderr)
	case "schema":
		return cmdSchema(args[1:], stdout, stderr)
	case "version", "--version", "-V":
		fmt.Fprintf(stdout, "pg-ccaudit %s\n", Version)
		return nil
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprintf(stderr, "pg-ccaudit: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return errUsage
	}
}
