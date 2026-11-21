package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/settle"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
)

// runLedger implements `pg-router ledger drop <query>`, the operator reset step
// of a settle consumer: it deletes the lane ledger the core keeps for one query,
// so a consumer reset (which replays history) is not followed by a report of a
// stale token.
//
// The command is offline only. It takes the queue log's exclusive lock, so it is
// refused (exit 9) while any daemon runs. That is the narrower of "refuse while a
// settle is in flight" and "require the router stopped": a running core holds the
// ledger in memory and rewrites the file on its next poll, so an edit under it
// would be silently undone, and a stopped router has no settle in flight by
// construction.
func runLedger(args []string) int {
	if len(args) == 0 || args[0] != "drop" {
		fmt.Fprintln(os.Stderr, "ledger: usage: pg-router ledger drop <query>")
		return exitUsage
	}
	if len(args) != 2 || args[1] == "" {
		fmt.Fprintln(os.Stderr, "ledger drop: exactly one <query> is required")
		return exitUsage
	}
	return ledgerDrop(os.Stdout, os.Stderr, config.LogDir(), args[1])
}

// ledgerDrop is runLedger's testable body.
func ledgerDrop(stdout, stderr io.Writer, logDir, query string) int {
	lock, err := eventqueue.LockLog(filepath.Join(logDir, "queue.jsonl"))
	if err != nil {
		var locked *eventqueue.ErrLogLocked
		if errors.As(err, &locked) {
			fmt.Fprintf(stderr, "ledger drop: %v\n", locked)
			fmt.Fprintln(stderr, "ledger drop: stop the router (or pause the query and wait for it to be stopped) and retry; nothing was changed")
			return exitBusy
		}
		fmt.Fprintf(stderr, "ledger drop: %v\n", err)
		return exitGeneric
	}
	defer func() { _ = lock.Close() }()
	l, err := settle.OpenLedger(filepath.Join(logDir, "ack-ledger.json"))
	if err != nil {
		fmt.Fprintf(stderr, "ledger drop: %v\n", err)
		return exitGeneric
	}
	n, err := l.DropQuery(query)
	if err != nil {
		fmt.Fprintf(stderr, "ledger drop: %v\n", err)
		return exitGeneric
	}
	fmt.Fprintf(stdout, "dropped %d lane(s) of query %s\n", n, textsafe.Sanitize(query))
	return exitOK
}
