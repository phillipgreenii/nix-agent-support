package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/phillipgreenii/pg-ccaudit/internal/ingest"
	"github.com/phillipgreenii/pg-ccaudit/internal/lock"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

func cmdIngest(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "database path")
	socket := fs.String("socket", "", "recorder socket (default: $PG_CLAUDE_CODE_RECORDER_SOCKET, else the recorder's default path)")
	thinking := fs.Bool("thinking", false,
		"also index thinking blocks (DEFAULT OFF; no shipped query reads them)")
	pageLimit := fs.Int("page-limit", 0, "feed page size (0 = the recorder's default)")
	quiet := fs.Bool("quiet", false, "suppress the summary line when nothing was read")
	lockPath := fs.String("lock", "", "advisory lock path (default: alongside the database)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit ingest — index new tool calls and results from the recorder

Reads the recorder daemon's change feed through two subscriptions (every tool
call and result without bodies; bodies only for errored results), each with its
own cursor kept in the database. A new subscriber starts from a snapshot; an
expired cursor resyncs from a snapshot. Claude's own files are never read: with
the recorder down this prints "recorder offline" and exits 0, and the next tick
polls again.

Single-instance: a second concurrent ingest detects the advisory lock, does
nothing, and exits 0.

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
	lp := *lockPath
	if lp == "" {
		lp = lock.DefaultPath(resolvedDB)
	}
	handle, err := lock.TryAcquire(lp)
	if err != nil {
		var held *lock.ErrHeld
		if errors.As(err, &held) {
			fmt.Fprintf(stdout, "ingest skipped: another ingest holds %s (single-instance writer)\n", lp)
			return nil
		}
		return err
	}
	defer func() {
		if err := handle.Release(); err != nil {
			fmt.Fprintf(stderr, "pg-ccaudit: release lock: %v\n", err)
		}
	}()

	c, err := client.Dial(ctx, client.Options{Socket: *socket, Name: "pg-ccaudit"})
	if err != nil {
		if errors.Is(err, client.ErrDown) {
			fmt.Fprintf(stdout, "ingest skipped: recorder offline (%v); will poll again later\n", err)
			return nil
		}
		return err
	}

	db, err := store.Open(resolvedDB, *thinking)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	fmt.Fprintf(stdout, "pg-ccaudit ingest: recorder feed db=%s thinking=%t\n", resolvedDB, *thinking)
	var progress *os.File
	if !*quiet {
		progress = stdout
	}
	opt := ingest.RecorderOptions{Client: c, Thinking: *thinking, PageLimit: *pageLimit}
	if progress != nil {
		opt.Progress = progress
	}
	stats, err := ingest.RunRecorder(ctx, db, opt)
	if err != nil {
		if errors.Is(err, client.ErrDown) || client.Retryable(err) || errors.Is(err, client.ErrBehind) {
			fmt.Fprintf(stdout, "ingest cycle skipped: recorder not ready (%v); will poll again later\n", err)
			return nil
		}
		return err
	}
	if progress == nil {
		fmt.Fprintln(stdout, stats.Summary())
	}
	return nil
}

func resolveDB(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	return store.DefaultDBPath()
}
