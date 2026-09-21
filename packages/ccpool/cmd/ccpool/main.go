package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/telemetry"
)

var version = "dev"

// stripPoolFlag removes a leading "--pool <dir>" (or "--pool=<dir>") that appears
// BEFORE the subcommand, returning the cleaned argv + the pool dir. A --pool after
// the subcommand, or a missing value, is an error (the subcommand flagsets are
// ExitOnError and would mishandle it). Position contract: ccpool --pool <dir> <cmd>.
func stripPoolFlag(argv []string) (clean []string, pool string, err error) {
	if len(argv) < 2 {
		return argv, "", nil
	}
	a := argv[1]
	switch {
	case a == "--pool":
		if len(argv) < 3 {
			return nil, "", fmt.Errorf("--pool requires a directory argument")
		}
		return append([]string{argv[0]}, argv[3:]...), argv[2], nil
	case strings.HasPrefix(a, "--pool="):
		return append([]string{argv[0]}, argv[2:]...), strings.TrimPrefix(a, "--pool="), nil
	}
	// reject a --pool anywhere after the subcommand
	for _, x := range argv[1:] {
		if x == "--pool" || strings.HasPrefix(x, "--pool=") {
			return nil, "", fmt.Errorf("--pool must come before the subcommand: ccpool --pool <dir> <command>")
		}
	}
	return argv, "", nil
}

func main() {
	os.Exit(run())
}

// run holds everything main() used to do directly, so that telemetry.
// Init's shutdown (below) is guaranteed to flush before the process exits.
// A deferred call in main() itself would never run: every branch below
// used to end by calling os.Exit directly, and os.Exit bypasses deferred
// functions outright — so the flush has to live inside a function main()
// calls and returns FROM, not inside main() itself. Mirrors packages/
// pg-router/cmd/pg-router/main.go's own main()/run() split (pg2-qye99 /
// pg2-24f89).
func run() int {
	// Telemetry: called unconditionally, for every subcommand. Init never
	// returns an error in practice — a missing OTEL_EXPORTER_OTLP_ENDPOINT
	// installs no-op providers (the common case for a one-shot operator
	// invocation with no OTLP env set), and a bad endpoint logs one stderr
	// warning and continues.
	shutdown := telemetry.Init(context.Background())
	defer func() { _ = shutdown(context.Background()) }()
	slog.SetDefault(slog.New(telemetry.Fanout(
		slog.NewTextHandler(os.Stderr, nil),
		telemetry.NewSlogHandler(),
	)))

	argv, pool, err := stripPoolFlag(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if pool != "" {
		// Validate (and create-on-demand) the pool dir up front so a bad --pool fails
		// as a usage error (exit 2) before any subcommand runs, rather than surfacing
		// later as a generic config-load error. The canonical root becomes CCPOOL_POOL,
		// overriding any inherited value.
		pc, perr := config.ResolvePool(pool)
		if perr != nil {
			fmt.Fprintln(os.Stderr, perr)
			return 2
		}
		_ = os.Setenv("CCPOOL_POOL", pc.Root)
	}
	kind, sub, rest := pickSubcommand(argv)
	switch kind {
	case dispatchHelp:
		// Bare `ccpool`, or an explicit -h/--help/help: print the top-level
		// usage listing every subcommand. list is NOT run implicitly any
		// more -- ask for it explicitly (`ccpool list`).
		fmt.Print(usageText())
		return 0
	case dispatchUnknown:
		// argv[1] matched nothing in the registry (typo, or an unimplemented
		// verb like "send"). Previously this silently ran `list` with the
		// unrecognized token passed through as list's own arg.
		fmt.Fprintf(os.Stderr, "ccpool: unknown subcommand %q\n\n", sub.name)
		fmt.Fprint(os.Stderr, usageText())
		return 2
	default: // dispatchKnown
		return sub.run(rest)
	}
}
