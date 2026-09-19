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

// pickSubcommand returns the subcommand name and the remaining args.
// With no subcommand, defaults to "list".
func pickSubcommand(args []string) (cmd string, rest []string) {
	known := map[string]bool{
		"attach":   true,
		"attend":   true,
		"cancel":   true,
		"close":    true,
		"doctor":   true,
		"hook":     true,
		"list":     true,
		"meta":     true,
		"new":      true,
		"reap":     true,
		"reap-all": true,
		"reply":    true,
		"result":   true,
		"state":    true,
		"tail":     true,
		"trust":    true,
		"version":  true,
	}
	if len(args) < 2 {
		return "list", nil
	}
	if known[args[1]] {
		return args[1], args[2:]
	}
	return "list", args[1:]
}

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
	cmd, rest := pickSubcommand(argv)
	switch cmd {
	case "attach":
		return runAttach(rest)
	case "attend":
		return runAttend(rest)
	case "cancel":
		return runCancel(rest)
	case "close":
		return runClose(rest)
	case "doctor":
		return runDoctor(rest)
	case "hook":
		return runHook(rest)
	case "list":
		return runList(rest)
	case "meta":
		return runMeta(rest)
	case "new":
		return runNew(rest)
	case "reap":
		return runReap(rest)
	case "reap-all":
		return runReapAll(rest)
	case "reply":
		return runReply(rest)
	case "result":
		return runResult(rest)
	case "state":
		return runState(rest)
	case "tail":
		return runTail(rest)
	case "trust":
		return runTrust(rest)
	case "version":
		fmt.Println(version)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		return 2
	}
}
