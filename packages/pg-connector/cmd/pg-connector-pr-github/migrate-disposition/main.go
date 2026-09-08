// migrate-disposition is the one-shot cutover tool that migrates pg-pr's
// legacy feedback-disposition data into pg-connector-pr-github's own local
// Store [design: ADR 0063 Decisions 1, 2, 5 —
// docs/adr/0063-pg-connector-pr-github-disposition-store-migration.md].
//
// It is a standalone binary, deliberately not wired as a subcommand of
// either pg-pr or pg-connector-pr-github: pg-connector-pr-github "has no
// independent CLI identity" (a scriptout-protocol-only process) and pg-pr
// "has no reason to know pg-connector's on-disk store shape" [design: ADR
// 0063 Decision 5].
//
// It is nested under cmd/pg-connector-pr-github/ (never a sibling top-level
// cmd/ directory) because that is load-bearing: Go's internal/ visibility
// rule only lets an importer rooted under cmd/pg-connector-pr-github/**
// import cmd/pg-connector-pr-github/internal/..., which is exactly what
// lets this tool call internal.ImportLegacyDispositions/internal.Store
// directly rather than re-deriving that mapping logic.
//
// Pipeline, run once per invocation:
//
//  1. `pg-pr config show --json` — discover every repo pg-pr itself is
//     configured to track (its repos[].remote list). `pg-pr pr list` alone
//     only ever resolves to ONE repo per call, so this is the only way to
//     enumerate all of them [binding decision: repo enumeration].
//  2. For each tracked repo, `pg-pr pr list --repo <remote> --json` — the
//     repo's open/draft (repo, number) pairs.
//  3. For each pair, `pg-pr feedback list <repo> <number> --json`, decoded
//     into []internal.LegacyFeedbackItem, then
//     internal.ImportLegacyDispositions(store, prID, items) — prID built
//     LOCALLY as fmt.Sprintf("%s#%d", repo, number) (see formatLocalPRID:
//     the unexported internal.formatPRID cannot be imported, and provider.go
//     is read-only reference here, never modified to export it).
//
// No direct SQLite access to pg-pr's store is ever made — the only source
// of truth is pg-pr's own existing, unchanged CLI JSON output [design: ADR
// 0063 Decision 1]. This tool also does not import pg-pr's internal/config
// package directly (a different Go module's internal package it has no
// nesting relationship with); repo discovery crosses the CLI/JSON boundary
// exactly like every other step here.
//
// Idempotent: safe to re-run. internal.ImportLegacyDispositions's own
// mapping is a pure function of its input and internal.Store.SetDisposition
// is a plain set/overwrite, so importing the same export twice reaches the
// same end state [design: ADR 0063 Decision 3].
//
// category is NOT migrated by this tool: pg-pr has no source column for it
// [design: ADR 0063 Decision 4].
//
// Freedom boundary / stated limitation: `pg-pr pr list --json` (no --repo
// filter, or with one) lists only a repo's OPEN/DRAFT PRs — it does not
// enumerate merged/closed PRs. This tool therefore migrates feedback only
// for PRs pg-pr still considers open at the moment it runs; a merged/closed
// PR's feedback-disposition history is NOT carried forward. This is this
// packet's own scoping choice, not a design requirement — the dependent
// sibling packet's own operator-run cutover step decides whether that gap
// warrants a manual override at invocation time.
//
// This tool BUILDS and TESTS only, against fixture data (see main_test.go).
// It MUST NOT be run against a real store as part of building it, and it
// does not touch pg-pr's own feedback command group — both belong to the
// dependent sibling packet that consumes this tool's pinned CLI interface.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options holds this tool's own pinned CLI flags:
// go run ./cmd/pg-connector-pr-github/migrate-disposition [--pg-pr-bin
// <path>] [--store <path>] [--dry-run].
type options struct {
	pgPrBin string
	store   string
	dryRun  bool
}

// parseFlags parses argv into options, resolving --store's default
// (internal.DefaultStorePath — the SAME default cmd/pg-connector-pr-github's
// own main.go uses) only when --store was not passed, since that default
// can itself fail (an unresolvable $HOME).
func parseFlags(argv []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("migrate-disposition", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pgPrBin := fs.String("pg-pr-bin", "pg-pr", "the pg-pr binary to invoke as a subprocess (resolved via $PATH)")
	store := fs.String("store", "", "destination pg-connector-pr-github JSON store path (default: internal.DefaultStorePath())")
	dryRun := fs.Bool("dry-run", false, "run the full enumerate/export/decode pipeline and print would-be-imported counts without mutating the store")
	if err := fs.Parse(argv); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected positional argument(s): %v (this tool takes no --repo flag — it discovers every repo pg-pr tracks itself)", fs.Args())
	}

	storePath := *store
	if storePath == "" {
		def, err := internal.DefaultStorePath()
		if err != nil {
			return options{}, fmt.Errorf("resolve default --store path: %w", err)
		}
		storePath = def
	}

	return options{pgPrBin: *pgPrBin, store: storePath, dryRun: *dryRun}, nil
}

// run is main's testable body: argv excludes the program name, and stdout/
// stderr are injected so tests never touch the real os.Stdout/os.Stderr.
func run(argv []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(argv, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "migrate-disposition: %v\n", err)
		return 2
	}

	s, cleanup, err := newRunStore(opts)
	if err != nil {
		fmt.Fprintf(stderr, "migrate-disposition: %v\n", err)
		return 1
	}
	defer cleanup()

	if err := migrate(context.Background(), runPgPr, opts, s, stdout); err != nil {
		fmt.Fprintf(stderr, "migrate-disposition: %v\n", err)
		return 1
	}
	return 0
}

// newRunStore returns the *internal.Store this run imports into, plus a
// cleanup func the caller must defer.
//
// Under --dry-run, the returned store is backed by a throwaway scratch file
// in a fresh temp directory, NEVER opts.store (the real destination) — so
// the full pipeline, including the real internal.ImportLegacyDispositions
// call (no re-derived counting logic), runs exactly as it would for real,
// but internal.Store.SetDisposition ends up mutating only the scratch copy,
// which cleanup then discards. This is what makes --dry-run's printed
// counts an exact preview of a real run while still satisfying "no
// mutation" of the real store: the real store's file is never opened, read,
// or written under --dry-run.
func newRunStore(opts options) (s *internal.Store, cleanup func(), err error) {
	if !opts.dryRun {
		return internal.NewStore(opts.store), func() {}, nil
	}
	dir, err := os.MkdirTemp("", "migrate-disposition-dry-run-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create --dry-run scratch directory: %w", err)
	}
	return internal.NewStore(filepath.Join(dir, "store.json")), func() { _ = os.RemoveAll(dir) }, nil
}

// pgPrRunner invokes `pg-pr <args...>` and returns its stdout, or an error
// if the subprocess failed to run or exited non-zero. It is a package-level
// var (runPgPr below) precisely so tests can substitute a fake that returns
// canned fixture JSON instead of spawning a real pg-pr binary.
type pgPrRunner func(ctx context.Context, bin string, args ...string) ([]byte, error)

// runPgPr is the production pgPrRunner: a real subprocess invocation.
var runPgPr pgPrRunner = execPgPr

func execPgPr(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w (stderr: %s)", bin, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// repoResult is one tracked repo's migration totals, used only to print the
// per-repo line before the final total (Produces' "print — per repo, then
// as a final total — the count of PRs processed and dispositions
// imported").
type repoResult struct {
	repo     string
	prs      int
	imported int
}

// migrate runs the full enumerate -> export -> decode -> import pipeline
// once, against every repo pg-pr tracks, printing per-repo and total
// summary lines to out. A per-item mapping error from
// internal.ImportLegacyDispositions (an unrecognised legacy
// DispositionAction, or a comment with no usable id) is reported to out and
// does NOT abort the run — one bad row must not block every other PR's
// legitimate disposition from migrating, mirroring
// ImportLegacyDispositions's own non-aborting contract. A subprocess
// invocation or JSON-decode failure IS fatal and returns a non-nil error
// (Produces' "nonzero on any subprocess-invocation or decode failure").
func migrate(ctx context.Context, runner pgPrRunner, opts options, s *internal.Store, out io.Writer) error {
	repos, err := discoverRepos(ctx, runner, opts.pgPrBin)
	if err != nil {
		return fmt.Errorf("discover tracked repos: %w", err)
	}

	var totalImported int
	for _, repo := range repos {
		numbers, err := listOpenPRNumbers(ctx, runner, opts.pgPrBin, repo)
		if err != nil {
			return fmt.Errorf("list PRs for %s: %w", repo, err)
		}
		result := repoResult{repo: repo, prs: len(numbers)}
		for _, number := range numbers {
			items, err := listFeedback(ctx, runner, opts.pgPrBin, repo, number)
			if err != nil {
				return fmt.Errorf("list feedback for %s: %w", formatLocalPRID(repo, number), err)
			}
			prID := formatLocalPRID(repo, number)
			imported, importErr := internal.ImportLegacyDispositions(s, prID, items)
			result.imported += imported
			if importErr != nil {
				fmt.Fprintf(out, "migrate-disposition: %s: %v\n", prID, importErr)
			}
		}
		totalImported += result.imported
		fmt.Fprintf(out, "%s: %d PR(s) processed, %d disposition(s) imported\n", result.repo, result.prs, result.imported)
	}
	fmt.Fprintf(out, "total: %d disposition(s) imported across %d repo(s)\n", totalImported, len(repos))
	return nil
}

// pgPrConfigShow is the subset of `pg-pr config show --json`'s output this
// tool reads: RepoConfig.Remote (packages/pg-pr/internal/config/config.go),
// JSON key "remote", inside the top-level "repos" array.
type pgPrConfigShow struct {
	Repos []struct {
		Remote string `json:"remote"`
	} `json:"repos"`
}

// discoverRepos runs `pg-pr config show --json` once and returns every
// tracked repo's remote identifier [binding decision: repo enumeration —
// pg-pr pr list has no all-repos-at-once form, so config show --json is the
// correct multi-repo source, mirroring pg-pr's own sync.go "for _, r :=
// range liveCfg().Repos" pattern via the CLI/JSON boundary instead of a Go
// import].
func discoverRepos(ctx context.Context, runner pgPrRunner, bin string) ([]string, error) {
	raw, err := runner(ctx, bin, "config", "show", "--json")
	if err != nil {
		return nil, fmt.Errorf("pg-pr config show --json: %w", err)
	}
	var cfg pgPrConfigShow
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode pg-pr config show --json output: %w", err)
	}
	repos := make([]string, 0, len(cfg.Repos))
	for _, r := range cfg.Repos {
		if r.Remote == "" {
			continue
		}
		repos = append(repos, r.Remote)
	}
	return repos, nil
}

// pgPrListItem is the subset of `pg-pr pr list --json`'s per-PR fields this
// tool needs (packages/pg-pr/cmd/pg-pr/pr_list.go's prListItem — this tool
// only needs the PR number; repo is already known from the --repo value
// that scoped the call).
type pgPrListItem struct {
	Number int `json:"number"`
}

// listOpenPRNumbers runs `pg-pr pr list --repo <repo> --json` and returns
// that repo's open/draft PR numbers. Never called without --repo: a bare
// `pg-pr pr list --json` only ever resolves to one (auto-detected) repo,
// never every tracked repo [binding decision: repo enumeration].
func listOpenPRNumbers(ctx context.Context, runner pgPrRunner, bin, repo string) ([]int, error) {
	raw, err := runner(ctx, bin, "pr", "list", "--repo", repo, "--json")
	if err != nil {
		return nil, fmt.Errorf("pg-pr pr list --repo %s --json: %w", repo, err)
	}
	var items []pgPrListItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode pg-pr pr list --repo %s --json output: %w", repo, err)
	}
	numbers := make([]int, 0, len(items))
	for _, it := range items {
		numbers = append(numbers, it.Number)
	}
	return numbers, nil
}

// listFeedback runs `pg-pr feedback list <repo> <number> --json` and
// decodes its output into []internal.LegacyFeedbackItem — the exact type
// internal.ImportLegacyDispositions consumes, so no separate decode-then-
// convert step is needed.
func listFeedback(ctx context.Context, runner pgPrRunner, bin, repo string, number int) ([]internal.LegacyFeedbackItem, error) {
	raw, err := runner(ctx, bin, "feedback", "list", repo, strconv.Itoa(number), "--json")
	if err != nil {
		return nil, fmt.Errorf("pg-pr feedback list %s %d --json: %w", repo, number, err)
	}
	var items []internal.LegacyFeedbackItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode pg-pr feedback list %s %d --json output: %w", repo, number, err)
	}
	return items, nil
}

// formatLocalPRID builds this tool's own prID convention:
// "<repo>#<number>" — the exact convention
// cmd/pg-connector-pr-github/internal/provider.go's own formatPRID uses and
// migrate_test.go's fixtures already demonstrate literally (e.g.
// "owner/repo#1"). Reimplemented here rather than imported because
// formatPRID is unexported (lowercase) and this packet's Files section
// forbids modifying provider.go to export it [binding decision:
// reimplement formatPRID].
func formatLocalPRID(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}
