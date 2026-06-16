package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/ccpool/internal/config"
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
	argv, pool, err := stripPoolFlag(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if pool != "" {
		// Validate (and create-on-demand) the pool dir up front so a bad --pool fails
		// as a usage error (exit 2) before any subcommand runs, rather than surfacing
		// later as a generic config-load error. The canonical root becomes CCPOOL_POOL,
		// overriding any inherited value.
		pc, perr := config.ResolvePool(pool)
		if perr != nil {
			fmt.Fprintln(os.Stderr, perr)
			os.Exit(2)
		}
		_ = os.Setenv("CCPOOL_POOL", pc.Root)
	}
	cmd, rest := pickSubcommand(argv)
	switch cmd {
	case "attach":
		os.Exit(runAttach(rest))
	case "attend":
		os.Exit(runAttend(rest))
	case "cancel":
		os.Exit(runCancel(rest))
	case "close":
		os.Exit(runClose(rest))
	case "doctor":
		os.Exit(runDoctor(rest))
	case "hook":
		os.Exit(runHook(rest))
	case "list":
		os.Exit(runList(rest))
	case "new":
		os.Exit(runNew(rest))
	case "reap":
		os.Exit(runReap(rest))
	case "reap-all":
		os.Exit(runReapAll(rest))
	case "reply":
		os.Exit(runReply(rest))
	case "result":
		os.Exit(runResult(rest))
	case "state":
		os.Exit(runState(rest))
	case "tail":
		os.Exit(runTail(rest))
	case "trust":
		os.Exit(runTrust(rest))
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		os.Exit(2)
	}
}
