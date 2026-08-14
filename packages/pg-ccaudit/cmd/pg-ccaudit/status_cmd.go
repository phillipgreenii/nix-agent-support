package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/query"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

func cmdStatus(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "database path")
	root := fs.String("root", "", "transcript root")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit status — report index coverage and staleness

READ-ONLY, like every query-path command: it tells you how far behind the index
is and stops there. Ingestion is the scheduled sweep's job (or an explicit
`+"`pg-ccaudit ingest`"+`), never a side effect of asking.

FLAGS
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	resolvedDB, err := resolveDB(*dbPath)
	if err != nil {
		return err
	}
	resolvedRoot, err := resolveRoot(*root)
	if err != nil {
		return err
	}
	db, err := store.OpenReadOnly(resolvedDB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	s := query.Measure(ctx, db, resolvedRoot)
	hasThinking, err := store.HasThinkingTable(db)
	if err != nil {
		return err
	}

	if *asJSON {
		payload := map[string]any{
			"db":             resolvedDB,
			"root":           resolvedRoot,
			"files_on_disk":  s.FilesOnDisk,
			"files_indexed":  s.FilesIndexed,
			"files_missing":  s.FilesMissing,
			"files_behind":   s.FilesBehind,
			"open_files":     s.OpenFiles,
			"bytes_behind":   s.BytesBehind,
			"lines_bad":      s.LinesBad,
			"stale":          s.Stale(),
			"thinking_table": hasThinking,
		}
		if !s.NewestOnDisk.IsZero() {
			payload["newest_on_disk"] = s.NewestOnDisk.UTC().Format(time.RFC3339)
		}
		if !s.NewestIndexed.IsZero() {
			payload["newest_indexed"] = s.NewestIndexed.UTC().Format(time.RFC3339)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}

	fmt.Fprintf(stdout, "db:   %s\n", resolvedDB)
	fmt.Fprintf(stdout, "root: %s\n", resolvedRoot)
	fmt.Fprintln(stdout, s.Note())
	fmt.Fprintf(stdout, "thinking table (T-16): %t\n", hasThinking)

	req := query.Request{Query: mustQuery("coverage"), Format: query.FormatTable}
	res, err := query.Run(ctx, db, req)
	if err != nil {
		return err
	}
	return query.Render(stdout, req, res)
}

func mustQuery(name string) query.Query {
	q, err := query.Lookup(name)
	if err != nil {
		panic(err)
	}
	return q
}
