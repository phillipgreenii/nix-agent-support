// pg-connector-github is the daemon-backed GitHub connector. One binary, three
// modes selected by the first non-flag word:
//
//	pg-connector-github [--config <path>] serve   the daemon
//	pg-connector-github [--config <path>] pr      client mode for the pr instance
//	pg-connector-github [--config <path>] ci      client mode for the ci instance
//	pg-connector-github [--config <path>] status [--json]
//	                                              the health report; exit 0 healthy, 2 degraded, 3 down
//
// --config is a global flag (accepted before or after the mode word) naming the
// rendered config file (see internal/config); when absent,
// PG_CONNECTOR_GITHUB_CONFIG is used. --version prints the stamped version.
//
// Every mode loads and validates the config first. serve runs the daemon
// (internal/serve): the process shell (state directory, lock, socket, drain on
// SIGTERM; see pkg/connectord/proc), the store, the governor, the scheduler, the
// request router, the group refresh engine and the Phase 2 and Phase 3a ops. pr and ci are the client modes
// (internal/client): one scriptout request on stdin, one response on stdout,
// forwarded to the daemon over its socket (pkg/connectord/socket).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/logs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
)

// Version is stamped at build time (mkGoApp's versionPath, `main.Version`,
// like every pg-connector backend).
var Version = "dev"

// clientBudgetOverride is a build-time seam (-ldflags "-X main.clientBudgetOverride=4s"),
// never set in a shipped build: when it parses as a positive duration it
// replaces the client's per-op call budget, so the real-binary tests need not
// wait out the 30 s production budget to see a daemon-down answer. The
// binary's environment is deliberately not consulted: the config's five
// variables are the only ones this binary reads.
var clientBudgetOverride = ""

// Exit codes of the process itself. 1 is reserved for unexpected errors and 2
// is a usage error. A client mode's own exit codes are the scriptout taxonomy's
// (2 to 8, see scriptout.ExitCodeForCode) once it has a request to answer, so
// exitConfig, which precedes any request, shares the value 4 with
// unavailable on purpose: both mean "this backend cannot be used".
const (
	exitOK     = 0
	exitUsage  = 2
	exitConfig = 4
)

// The three modes, selected by the first non-flag word.
const (
	modeServe = "serve"
	modePR    = "pr"
	modeCI    = "ci"
	// modeStatus is the operator-facing health report (internal/ops/status).
	modeStatus = "status"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
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
       pg-connector-github [--config <path>] status [--json]
       pg-connector-github --version

modes:
  serve   run the daemon
  pr      client mode, pr instance
  ci      client mode, ci instance
  status  the health report (exit 0 healthy, 2 degraded, 3 down); --json for the document

--config <path> names the rendered config file; PG_CONNECTOR_GITHUB_CONFIG is
used only when the flag is absent.
`

// run is main with its I/O injected so it is testable. It returns the exit
// code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
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
	case modeServe, modePR, modeCI, modeStatus:
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
	return runMode(o.mode, o.modeArgs, cfg, stdin, stdout, stderr)
}

// runMode dispatches to the selected mode: serve runs the process shell, pr and
// ci run the client mode of their instance.
func runMode(mode string, modeArgs []string, cfg *config.Config, stdin io.Reader, stdout, stderr io.Writer) int {
	switch mode {
	case modePR:
		return runClient(client.PR(), cfg, stdin, stdout)
	case modeCI:
		return runClient(client.CI(), cfg, stdin, stdout)
	case modeStatus:
		return runStatus(modeArgs, cfg, stdout, stderr)
	}
	return runServe(cfg, stderr)
}

// runClient answers one scriptout request for the instance.
func runClient(inst client.Instance, cfg *config.Config, stdin io.Reader, stdout io.Writer) int {
	inst = inst.WithFallback(status.OpName, status.Fallback(status.StaticFromConfig(cfg, Version), nil))
	o := client.Options{
		Instance:        inst,
		SocketPath:      cfg.SocketPath,
		SupervisorLabel: cfg.SupervisorLabel,
		ConfigPath:      cfg.Path,
		Build:           Version,
		ClientLog:       logs.NewClientLog(cfg.StateDir, Version),
	}
	if d, err := time.ParseDuration(clientBudgetOverride); err == nil && d > 0 {
		o.Budget = d
	}
	return client.Run(context.Background(), o, stdin, stdout)
}
