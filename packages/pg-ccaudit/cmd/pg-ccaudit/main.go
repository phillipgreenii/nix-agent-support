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
	"flag"
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

MISTAKE CENSUS (read-only; the three tiers, in order)
  candidates  Tier 1: structural mistake candidates, SQL only, no model calls
  classify    Tier 2: decide which candidates are real, with a reported run cost
  report      Tier 3: ONE ranked report, mistakes AND command failures, each routed
  evaluate    score a classifier against the gold set and against the naive baseline
  gold        maintain the gold set (seed the file channel, sample for labelling)

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

// parseInterspersed parses flags that appear BEFORE, AFTER or BETWEEN positional
// arguments, and returns the positionals in order.
//
// Go's flag package stops at the first non-flag token, which for a subcommand that
// takes a positional means every flag after it is silently treated as another
// positional. That is not a hypothetical: it made the shipped review skill's own
// documented invocations fail —
//
//	pg-ccaudit query error-rate-by-tool --since 2026-07-22 --until 2026-07-30
//
// parsed `error-rate-by-tool` and then handed `--since`, `2026-07-22`, `--until`
// and `2026-07-30` to a query that takes no arguments, so the command errored out
// with a message about argument counts. Worse for a query that DOES take one:
// `--since` would be bound as its parameter value. Either way the window a census
// claimed to cover was not the window it ran over, which is precisely the class of
// unverifiable number this index exists to eliminate.
//
// The loop is the standard fix: parse, take the one positional the parser stopped
// on, parse again from the token after it.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, errUsage
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
}

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
	case "candidates":
		return cmdCandidates(ctx, args[1:], stdout, stderr)
	case "classify":
		return cmdClassify(ctx, args[1:], stdout, stderr)
	case "evaluate":
		return cmdEvaluate(ctx, args[1:], stdout, stderr)
	case "report":
		return cmdReport(ctx, args[1:], stdout, stderr)
	case "gold":
		return cmdGold(ctx, args[1:], stdout, stderr)
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
