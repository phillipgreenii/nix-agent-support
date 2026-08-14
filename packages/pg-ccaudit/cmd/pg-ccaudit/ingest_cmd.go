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
)

func cmdIngest(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "database path")
	root := fs.String("root", "", "transcript root")
	thinking := fs.Bool("thinking", false,
		"also index thinking blocks (T-16; DEFAULT OFF — ~94 MB corpus-wide and no shipped query reads them)")
	progressEvery := fs.Int("progress-every", ingest.DefaultProgressEvery,
		"emit a progress line every N scanned files")
	finalAfter := fs.Duration("final-after", ingest.DefaultFinalAfter,
		"how long a fully consumed file must be quiescent before it is recorded complete (0 = immediately)")
	quiet := fs.Bool("quiet", false, "suppress progress lines (the final summary is still printed)")
	lockPath := fs.String("lock", "", "advisory lock path (default: alongside the database)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit ingest — index new and appended transcripts

Incremental and resumable: change is detected on (path, size, mtime) and only the
appended byte range is parsed. A file whose size shrank or whose mtime moved
backward was rewritten (compaction does this) and is re-indexed from zero.

Single-instance: a second concurrent ingest detects the advisory lock, does
nothing, and exits 0 — an overlapping tick is expected, not an error.

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
	if _, err := os.Stat(resolvedRoot); err != nil {
		return fmt.Errorf("transcript root %s: %w", resolvedRoot, err)
	}

	lp := *lockPath
	if lp == "" {
		lp = lock.DefaultPath(resolvedDB)
	}
	handle, err := lock.TryAcquire(lp)
	if err != nil {
		var held *lock.ErrHeld
		if errors.As(err, &held) {
			// T-12: detect and NO-OP. Exit zero — a skipped overlapping tick is
			// correct behaviour, and reporting it as failure would make the
			// scheduled sweep log an alarm every time two ticks overlapped.
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

	db, err := store.Open(resolvedDB, *thinking)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	progress := (*os.File)(nil)
	if !*quiet {
		progress = stdout
	}
	fmt.Fprintf(stdout, "pg-ccaudit ingest: root=%s db=%s thinking=%t\n", resolvedRoot, resolvedDB, *thinking)
	// A user-typed 0 means "final as soon as fully consumed"; the library reads a
	// zero Options field as "caller said nothing", so translate here.
	resolvedFinalAfter := *finalAfter
	if resolvedFinalAfter == 0 {
		resolvedFinalAfter = ingest.FinalAfterImmediate
	}
	opt := ingest.Options{
		Root:          resolvedRoot,
		Thinking:      *thinking,
		ProgressEvery: *progressEvery,
		FinalAfter:    resolvedFinalAfter,
	}
	if progress != nil {
		opt.Progress = progress
	} else {
		// Even quiet mode prints the final summary: it is the only observable
		// proof that the sweep did zero work rather than silently failing.
		opt.Progress = nil
	}
	stats, err := ingest.Run(ctx, db, opt)
	if err != nil {
		return err
	}
	if progress == nil {
		fmt.Fprintln(stdout, stats.Summary())
	}
	if stats.Failed > 0 {
		fmt.Fprintf(stderr, "pg-ccaudit: %d file(s) could not be indexed; see warnings above\n", stats.Failed)
	}
	return nil
}

func resolveDB(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	return store.DefaultDBPath()
}

func resolveRoot(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	return store.DefaultTranscriptRoot()
}
