// pg-connector-github is the daemon-backed GitHub connector. One binary, three
// modes selected by the first non-flag word:
//
//	pg-connector-github [--config <path>] serve   the daemon
//	pg-connector-github [--config <path>] pr      client mode for the pr instance
//	pg-connector-github [--config <path>] ci      client mode for the ci instance
//
// --config is a global flag (accepted before or after the mode word) naming the
// rendered config file (see internal/config); when absent,
// PG_CONNECTOR_GITHUB_CONFIG is used. --version prints the stamped version.
//
// This is the skeleton: every mode loads and validates the config and then
// stops with a clear "not implemented" error. The daemon, the socket server and
// the client forwarding are filled in by later work.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
)

// Version is stamped at build time (mkGoApp's versionPath, `main.Version`,
// like every pg-connector backend).
var Version = "dev"

// Exit codes. 1 is reserved for unexpected errors; 2 is a usage error and 3
// means the selected mode is not implemented yet.
const (
	exitOK             = 0
	exitUsage          = 2
	exitNotImplemented = 3
	exitConfig         = 4
)

// The three modes, selected by the first non-flag word.
const (
	modeServe = "serve"
	modePR    = "pr"
	modeCI    = "ci"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

// options is the parsed command line.
type options struct {
	configPath string
	mode       string
	modeArgs   []string
	version    bool
	help       bool
}

// parseArgs scans args for the global flags (--config, --version and --help;
// accepted before or after the mode word, so a --config given after the mode is
// never silently ignored) and for the mode, the first non-flag word. An unknown
// flag before the mode is an error; every other argument after the mode word
// belongs to the mode.
func parseArgs(args []string) (options, error) {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--version" || a == "-version":
			o.version = true
		case a == "--help" || a == "-h" || a == "-help":
			o.help = true
		case a == "--config" || a == "-config":
			if i+1 >= len(args) {
				return o, errors.New("--config requires a path")
			}
			i++
			o.configPath = args[i]
		case strings.HasPrefix(a, "--config="):
			o.configPath = strings.TrimPrefix(a, "--config=")
		case strings.HasPrefix(a, "-config="):
			o.configPath = strings.TrimPrefix(a, "-config=")
		case o.mode != "":
			o.modeArgs = append(o.modeArgs, a)
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %q", a)
		default:
			o.mode = a
		}
	}
	return o, nil
}

const usage = `usage: pg-connector-github [--config <path>] <serve|pr|ci>
       pg-connector-github --version

modes:
  serve   run the daemon
  pr      client mode, pr instance
  ci      client mode, ci instance

--config <path> names the rendered config file; PG_CONNECTOR_GITHUB_CONFIG is
used only when the flag is absent.
`

// run is main with its I/O injected so it is testable. It returns the exit
// code.
func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	o, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-github: %v\n\n%s", err, usage)
		return exitUsage
	}
	if o.version {
		fmt.Fprintf(stdout, "pg-connector-github %s\n", Version)
		return exitOK
	}
	if o.help {
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	switch o.mode {
	case modeServe, modePR, modeCI:
	case "":
		fmt.Fprintf(stderr, "pg-connector-github: a mode is required\n\n%s", usage)
		return exitUsage
	default:
		fmt.Fprintf(stderr, "pg-connector-github: unknown mode %q\n\n%s", o.mode, usage)
		return exitUsage
	}

	cfg, err := config.Load(o.configPath, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-github %s: %v\n", o.mode, err)
		return exitConfig
	}
	return runMode(o.mode, o.modeArgs, cfg, stderr)
}

// runMode dispatches to the selected mode. Each mode is a stub that later work
// fills in.
func runMode(mode string, _ []string, cfg *config.Config, stderr io.Writer) int {
	fmt.Fprintf(stderr, "pg-connector-github %s: not implemented yet (skeleton stub; config %s loaded, hash %s)\n", mode, cfg.Path, cfg.Hash)
	return exitNotImplemented
}
