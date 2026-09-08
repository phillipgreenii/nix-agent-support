// root.go: pg-connector's root cobra command. pg-connector is the only
// user-facing CLI surface — each entity-type/capability packet's own verb
// group (pr.go's pr commands, ci.go's ci commands, issue.go's issue
// commands, scm.go's scm commands) attaches its subcommands to this same
// root. pg-connector remains the single binary and the single user-facing
// CLI surface that pr/ci/issue/scm are verb groups of, never a separate
// binary of its own.
//
// The persistent --output flag (registered here via output.go's
// addOutputFlag, so every verb group inherits it without redeclaring it
// itself) selects pg-connector's own CLI presentation mode
// [bead pg2-ox1k6] — see output.go's header comment for the json-default
// design rationale.
//
// root's PersistentPreRunE below is what makes --output validation
// genuinely pre-dispatch [bug A4, bead pg2-zc3b4]: cobra runs the nearest
// ancestor's PersistentPreRunE before the invoked (leaf) command's own
// RunE, and no verb-group command in this package defines its own
// PersistentPreRunE to shadow it — so this fires, for every verb, strictly
// before that verb's RunE calls Dispatch/fanOut*. Without it, an invalid
// --output value was only ever caught inside output.go's
// writeTargetedResult/writeFanOutResult, both of which run AFTER the
// backend has already executed (and, for a write op, already mutated
// state) — see output.go's own header on outputModeFor for the full
// story.
package main

import (
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// Version is set at build time by mkGoApp (versionPath = "main.Version").
var Version = "dev"

// rootLong is root's --help "Long" body: the exit-code taxonomy
// (docs/behavior/invariants.md's INV-EXIT-1/INV-EXIT-2, restated here so an
// operator/script author never has to leave --help to learn it) and the
// config file resolution order (registry.go's own $PG_PR_CONFIG -> XDG ->
// ~/.config order) [finding A30]. Neither was documented anywhere --help
// reached before this.
const rootLong = `Unified pluggable connector umbrella CLI.

Exit codes (pg-connector's own CLI layer — never confused with a backend's
own 0/1 wire-protocol exit code):
  Fan-out ops (e.g. "ci list", "auth status", "config validate" — query
  every backend registered for a type/capability):
    0  every queried backend succeeded (no degraded/failed row)
    2  degraded/partial — at least one backend succeeded, at least one did not
    3  total failure — no backend succeeded, including zero backends registered
  Targeted ops (resolve to exactly one backend — e.g. "pr show",
  "pr categorize", "issue create", "scm worktree add"):
    0  the operation completed and produced a well-formed response
    4  not_found — a well-formed negative answer, never shared with a real failure
    1  any other error, or a CLI-level failure before a response ever existed
  1 is otherwise reserved for the CLI's own generic/unexpected-failure path
  and is never emitted by the fan-out scheme for an in-taxonomy outcome.

Config file:
  Resolved in order: $PG_PR_CONFIG (explicit override, if set) ->
  $XDG_CONFIG_HOME/pg-pr/config.yaml -> ~/.config/pg-pr/config.yaml. The
  directory name stays "pg-pr" even though this binary is pg-connector,
  since both tools may share one config.yaml (pg-connector only reads its
  own connector: key). Run "pg-connector config show" to see which file
  this build actually resolved and what it registers under connector:.`

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pg-connector",
		Short:         "Unified pluggable connector umbrella CLI",
		Long:          rootLong,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Validates --output before any subcommand's RunE runs, so a
		// bad value is caught with zero side effects — see this file's
		// header comment and output.go's outputModeFor.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			_, err := outputModeFor(cmd)
			return err
		},
	}
	// Appends the wire protocol's own ProtocolVersion to --version's
	// output, on its own line after cobra's stock "<name> version
	// <version>\n" line — cobra's own default template
	// (defaultVersionTemplate in its command.go) previously had no way to
	// surface this at all [finding A30]. protocolVersion is a single
	// build-time constant (pkg/scriptout.ProtocolVersion), not one of the
	// per-capability schemaVersions (those vary per capability and per
	// backend, and are already exposed by "config validate" and each
	// backend's own capabilities op) — see envelope.go's own doc comment
	// on why the two are never conflated.
	root.SetVersionTemplate(fmt.Sprintf(
		"{{with .DisplayName}}{{printf \"%%s \" .}}{{end}}{{printf \"version %%s\" .Version}}\nprotocolVersion: %d\n",
		scriptout.ProtocolVersion,
	))
	root.AddCommand(newAuthCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newPrCmd())
	root.AddCommand(newCiCmd())
	root.AddCommand(newIssueCmd())
	root.AddCommand(newScmCmd())
	root.AddCommand(newAttentionCmd())
	root.AddCommand(newSearchCmd())
	addOutputFlag(root)
	return root
}
