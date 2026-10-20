// Command work-report-narrative is the narrative report generator: a
// contract-version-1 generator program (docket pg2-6oop0) that work-report's
// runner execs. It reads the envelope on stdin, hands the baseline text to
// claude as data, writes report.md into the envelope's output directory and
// prints the manifest on stdout.
//
// It is the only work-report code that execs claude. The call is hardened:
// the system prompt travels out of band as an argument, the data is the
// process's stdin inside a neutralized <activity_data> fence, and the tool
// allow-list is empty. Every failure is a non-zero exit with the reason on
// stderr; a fabricated or fallback report is never written.
//
// Exit codes follow the generator table: 0 success, 1 unexpected failure, 2
// usage error (malformed envelope, unknown mode, unsupported contract
// version), 3 invalid config.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
