package main

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/inputproc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/sandboxdetect"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
	"github.com/spf13/cobra"
)

// sandboxEnabledPtr converts the nullable sandbox_enabled column into an
// *int suitable for JSON encoding (nil → null, 0 → 0, 1 → 1).
func sandboxEnabledPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	x := int(v.Int64)
	return &x
}

// sandboxEnabledLabel returns a three-value human label: "on", "off", "-".
func sandboxEnabledLabel(v sql.NullInt64) string {
	if !v.Valid {
		return "-"
	}
	if v.Int64 != 0 {
		return "on"
	}
	return "off"
}

// sandboxEnabledKey returns a three-value key suitable for grouping in
// map[string]int aggregations: "on", "off", "unknown".
func sandboxEnabledKey(v sql.NullInt64) string {
	if !v.Valid {
		return "unknown"
	}
	if v.Int64 != 0 {
		return "on"
	}
	return "off"
}

// knownSubcommands lists the first-argument tokens that should route to the
// cobra CLI rather than to hook mode. Hook mode is the default when this
// binary is invoked with no arguments (as a Claude Code hook) or when the
// first argument is not a recognized CLI entry point.
var knownSubcommands = map[string]bool{
	"baseline":             true,
	"compare":              true,
	"evaluate":             true,
	"mark-excluded":        true,
	"report":               true,
	"set-correct-decision": true,
	"show":                 true,
	"completion":           true, // cobra builtin
	"help":                 true, // cobra builtin
	"-h":                   true,
	"--help":               true,
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "claude-extended-tool-approver",
		Short: "Claude Code extended tool approver — hook and evaluation CLI",
		Long: `claude-extended-tool-approver is a Claude Code PreToolUse hook that
evaluates tool invocations against a set of rules and logs decisions for
later analysis.

When invoked with no arguments, it runs in hook mode: reading a Claude Code
hook event from stdin and writing a decision to stdout.

When invoked with a subcommand, it acts as an evaluation CLI for querying
and analyzing the decision log.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newBaselineCmd())
	root.AddCommand(newCompareCmd())
	root.AddCommand(newEvaluateCmd())
	root.AddCommand(newMarkExcludedCmd())
	root.AddCommand(newReportCmd())
	root.AddCommand(newSetCorrectDecisionCmd())
	root.AddCommand(newShowCmd())
	return root
}

func main() {
	// CLI mode: dispatch to cobra when the first argument looks like a
	// subcommand or a help flag. Otherwise fall through to hook mode.
	if len(os.Args) > 1 {
		if knownSubcommands[os.Args[1]] {
			if err := newRootCmd().Execute(); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	// Hook mode: panic recovery → abstain
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: panic: %v\n", r)
			fmt.Println("{}")
			os.Exit(0)
		}
	}()

	input, err := hookio.ParseInput(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: %v\n", err)
		fmt.Println("{}")
		os.Exit(0)
	}

	switch input.HookEventName {
	case "PreToolUse", "":
		handlePreToolUse(input)
	case "PermissionRequest":
		handlePermissionRequest(input)
	case "PostToolUse":
		handlePostToolUse(input)
	case "PermissionDenied":
		handlePermissionDenied(input)
	case "SessionEnd":
		handleSessionEnd(input)
	default:
		fmt.Println("{}")
	}
}

// projectDirForDetect returns the directory used to resolve Claude Code
// settings for sandbox detection. CLAUDE_PROJECT_DIR is set by the harness
// when invoking hooks; the hook's CWD is the fallback.
func projectDirForDetect(input *hookio.HookInput) string {
	if d := os.Getenv("CLAUDE_PROJECT_DIR"); d != "" {
		return d
	}
	return input.CWD
}

func handlePreToolUse(input *hookio.HookInput) {
	eng := setup.NewEngineForCWD(input.CWD)
	result := eng.Evaluate(input)

	var updatedInput map[string]interface{}
	if (result.Decision == hookio.Approve || result.Decision == hookio.Ask) &&
		inputproc.Configured() && input.ToolName == "Bash" {
		if cmd, err := input.BashCommand(); err == nil {
			if rewritten, changed := inputproc.Process(cmd); changed {
				updatedInput = map[string]interface{}{
					"command": rewritten,
				}
			}
		}
	}

	if store, err := asklog.NewStore(asklog.DefaultDBPath()); err == nil {
		defer store.Close()
		store.SetSandboxEnabled(sandboxdetect.Detect(projectDirForDetect(input)))
		if err := asklog.RecordPreToolDecision(store, input, result); err != nil {
			fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog: %v\n", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog open: %v\n", err)
	}

	output := hookio.FormatOutput(result, updatedInput)
	os.Stdout.Write(output)
	fmt.Fprintln(os.Stdout)
}

func handlePermissionRequest(input *hookio.HookInput) {
	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog open: %v\n", err)
		fmt.Println("{}")
		return
	}
	defer store.Close()
	store.SetSandboxEnabled(sandboxdetect.Detect(projectDirForDetect(input)))

	var suggestions string
	if input.PermissionSuggestions != nil {
		suggestions = string(input.PermissionSuggestions)
	}

	if err := asklog.RecordPermissionRequest(store, input, suggestions); err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog: %v\n", err)
	}
	fmt.Println("{}")
}

func handlePostToolUse(input *hookio.HookInput) {
	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog open: %v\n", err)
		fmt.Println("{}")
		return
	}
	defer store.Close()

	if err := asklog.ResolveApproved(store, input, ""); err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog: %v\n", err)
	}
	fmt.Println("{}")
}

func handlePermissionDenied(input *hookio.HookInput) {
	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog open: %v\n", err)
		fmt.Println("{}")
		return
	}
	defer store.Close()
	store.SetSandboxEnabled(sandboxdetect.Detect(projectDirForDetect(input)))

	if err := asklog.RecordPermissionDenied(store, input); err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog: %v\n", err)
	}
	fmt.Println("{}")
}

func handleSessionEnd(input *hookio.HookInput) {
	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog open: %v\n", err)
		fmt.Println("{}")
		return
	}
	defer store.Close()

	if err := asklog.ResolveDeniedAll(store, input.SessionID); err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: asklog: %v\n", err)
	}
	fmt.Println("{}")
}
