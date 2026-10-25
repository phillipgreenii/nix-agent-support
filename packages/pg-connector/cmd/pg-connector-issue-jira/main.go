// pg-connector-issue-jira is the issue capability's second Tier-2 backend: a
// thin, standalone executable speaking only the scriptout wire protocol
// (pkg/scriptout.ServeLoop), with no independent human-facing CLI identity —
// mirroring cmd/pg-connector-issue-beads/main.go's identical structure
// [carry-over basis: cmd/pg-connector-issue-beads/main.go]. It implements
// pkg/provider/issue.Provider against a real external Jira integration by
// shelling out to phillipg-nix-repo-base's generic pjira CLI (internal).
//
// Unlike pg-connector-issue-beads, this binary's dispatch table DOES gain an
// auth_status entry: internal.Backend implements pkg/provider.AuthChecker
// via pjira's own auth-status op (see internal/backend.go's CheckAuth doc
// comment) — capabilities.ops (via scriptout.AddCapabilities) reflects that
// automatically, with no separate hand-typed ops list.
//
// With --config <file> the binary has three more modes (see dispatch): serve
// (the daemon), --instance <name> (the client the umbrella execs: capabilities
// and the five writes in-process, every other op forwarded to the daemon over
// its socket; internal/client) and status [--json] (the health report).
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath,
// mirroring every existing Tier-2 backend's own convention).
var Version = "dev"

// Exit codes of the process itself that precede any request. 1 is reserved for
// unexpected errors; a client mode's own codes are the scriptout taxonomy's.
const (
	// exitUsage is a command line the binary does not accept.
	exitUsage = 2
	// exitConfig is a config file that cannot be loaded; it shares the value 4
	// with unavailable on purpose: both mean "this backend cannot be used".
	exitConfig = 4
)

// The modes selected by what follows --config.
const (
	// modeServe is the daemon.
	modeServe = "serve"
	// modeStatus is the operator-facing health report.
	modeStatus = "status"
	// modeClient is the client, selected by --instance and no mode word.
	modeClient = "client"
)

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// dispatch selects the mode. --version and --help work in every form. With no
// --config flag the binary is the old stateless backend, exactly as before
// (scriptout.ServeLoop over the old dispatch table), whatever other arguments
// it was given. With --config <path> it runs
//
//	serve                  the daemon
//	--instance <name>      the client: one request on stdin, one response on stdout
//	status [--json]        the health report (exit 0 healthy, 2 degraded, 3 down)
//
// and any other --config form is a usage error. An --instance that differs from
// the config's instance exits 4, like a config that cannot be loaded.
func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	for _, a := range args {
		switch a {
		case "--version", "-version":
			fmt.Fprintf(stdout, "pg-connector-issue-jira %s\n", Version)
			return 0
		case "--help", "-h", "-help":
			fmt.Fprint(stdout, usage)
			return 0
		}
	}
	path, rest, given, err := splitConfigFlag(args)
	if !given {
		return run()
	}
	var instance string
	var instanceGiven bool
	if err == nil {
		instance, rest, instanceGiven, err = splitInstanceFlag(rest)
	}
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-issue-jira: %v\n\n%s", err, usage)
		return exitUsage
	}
	var mode string
	switch {
	case len(rest) == 0 && instanceGiven:
		mode = modeClient
	case len(rest) == 1 && rest[0] == modeServe && !instanceGiven:
		mode = modeServe
	case len(rest) >= 1 && rest[0] == modeStatus:
		mode = modeStatus
	default:
		fmt.Fprintf(stderr, "pg-connector-issue-jira: --config takes --instance <name>, %q or %q\n\n%s", modeServe, modeStatus, usage)
		return exitUsage
	}
	var modeArgs []string
	if mode == modeStatus {
		modeArgs = rest[1:]
		// A bad argument is a usage error before the config is read.
		if _, err := parseStatusArgs(modeArgs); err != nil {
			fmt.Fprintf(stderr, "pg-connector-issue-jira status: %v\n\n%s", err, usage)
			return exitUsage
		}
	}
	cfg, err := config.Load(path, nil)
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-issue-jira %s: %v\n", mode, err)
		return exitConfig
	}
	if instanceGiven && instance != cfg.Instance {
		fmt.Fprintf(stderr, "pg-connector-issue-jira %s: --instance %q is not the config's instance %q\n", mode, instance, cfg.Instance)
		return exitConfig
	}
	switch mode {
	case modeServe:
		return runServe(cfg, stderr)
	case modeStatus:
		return runStatus(modeArgs, cfg, stdout, stderr)
	}
	return runClient(cfg, stdin, stdout)
}

const usage = `usage: pg-connector-issue-jira                                    (the stateless backend: one request on stdin)
       pg-connector-issue-jira --config <path> serve              (the daemon)
       pg-connector-issue-jira --config <path> --instance <name>  (client: one request on stdin)
       pg-connector-issue-jira --config <path> status [--json]    (health report: exit 0 healthy, 2 degraded, 3 down)
       pg-connector-issue-jira --version
`

// splitConfigFlag scans args for --config <path> / --config=<path> (also with a
// single dash) and returns the path, the remaining arguments and whether the
// flag appeared at all. A --config with no path is an error.
func splitConfigFlag(args []string) (path string, rest []string, given bool, err error) {
	return splitValueFlag(args, "config", "a path")
}

// splitInstanceFlag scans args for --instance <name> / --instance=<name> (also
// with a single dash), like splitConfigFlag does for --config.
func splitInstanceFlag(args []string) (name string, rest []string, given bool, err error) {
	return splitValueFlag(args, "instance", "a name")
}

// splitValueFlag scans args for --name <value> / --name=<value> (also with a
// single dash) and returns the value, the remaining arguments and whether the
// flag appeared at all. A flag with no value is an error naming what it needs.
func splitValueFlag(args []string, name, needs string) (value string, rest []string, given bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--"+name || a == "-"+name:
			given = true
			if i+1 >= len(args) {
				err = fmt.Errorf("--%s requires %s", name, needs)
				continue
			}
			i++
			value = args[i]
		case strings.HasPrefix(a, "--"+name+"="):
			given, value = true, strings.TrimPrefix(a, "--"+name+"=")
		case strings.HasPrefix(a, "-"+name+"="):
			given, value = true, strings.TrimPrefix(a, "-"+name+"=")
		default:
			rest = append(rest, a)
		}
	}
	return value, rest, given, err
}

// run builds this backend's Provider (a pjira-CLI-backed Backend) and its
// op-dispatch table, then hands the table to the Tier-1 core's generic
// serve loop.
func run() int {
	backend := internal.New(internal.NewCLIRunner())
	return scriptout.ServeLoop(instrument(newDispatchTable(backend), os.Getenv))
}

// instrument wraps table so every call appends one event to this backend's
// own rotating event log (bead pg2-ltddq; package eventlog explains the
// ownership contract). It lives apart from newDispatchTable so the wiring
// tests that drive newDispatchTable never write to the real state home. With
// the log disabled or unresolvable it returns table unchanged.
func instrument(table scriptout.DispatchTable, getenv func(string) string) scriptout.DispatchTable {
	return eventlog.Instrument(table, eventlog.SinkFromEnv(getenv), Version, time.Now)
}

// newDispatchTable builds the issue capability's table (show/create/
// comment/transition, plus auth_status since internal.Backend implements
// AuthChecker) via issue.NewDispatchTable, then merges in the search
// capability's table (search, plus its own auth_status entry —
// functionally identical to issue's, since both type-assert the same
// backend; harmless to overwrite) built by
// pkg/provider/search.NewDispatchTable (bead pg2-8hcnx: this backend's
// `pjira search --jql` call already existed for List but was never wired
// to the cross-capability "search" op), then adds this backend's own
// capabilities entry via scriptout.AddCapabilities — AddCapabilities
// computes capabilities.ops straight from this table's own registered op
// names, so this backend never hand-types a second, separately maintained
// ops list [carry-over basis: cmd/pg-connector-issue-beads/main.go's
// identical newDispatchTable, bead pg2-fh2vh].
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := issue.NewDispatchTable(backend)
	for op, handler := range search.NewDispatchTable(backend) {
		table[op] = handler
	}
	// activity (list_activity, plus its own auth_status entry, identical in
	// effect to the others) built by pkg/provider/activity.NewDispatchTable:
	// this backend's own ListActivity, scoped to the operator's own identity
	// (see internal/activity.go).
	for op, handler := range activity.NewDispatchTable(backend) {
		table[op] = handler
	}
	return scriptout.AddCapabilities(table, schema.IssueSchemaVersion, capabilitiesBase())
}

// capabilitiesBase declares this backend's schemaVersions and its
// non-empty state and priority vocabularies (Jira's own classic default
// workflow/priority names — see internal.Vocabulary/internal.
// PriorityVocabulary's doc comments for why those are real, not invented).
// Version carries this binary's own ldflags-stamped build var.
func capabilitiesBase() scriptout.CapabilitiesResponse {
	return scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions: map[string]int{
			"issue":    schema.IssueSchemaVersion,
			"search":   schema.SearchSchemaVersion,
			"activity": schema.ActivitySchemaVersion,
		},
		Vocabulary: map[string]any{
			"state":    internal.Vocabulary,
			"priority": internal.PriorityVocabulary,
			// The activity kinds this backend emits; capabilities.ops stays
			// derived from the table, never a hand-typed list.
			"activity_kinds": internal.ActivityKinds,
		},
		Version: Version,
	}
}
