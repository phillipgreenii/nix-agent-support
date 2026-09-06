// output.go: pg-connector's own CLI output-mode selection and shared
// write-path helpers.
//
// This is a CLI PRESENTATION concern only [bead pg2-ox1k6]. It never
// touches pkg/scriptout's wire protocol (the stdin/stdout JSON envelope a
// Tier-2 backend speaks to the umbrella) — that stays JSON-only and
// unaffected. Every human-mode formatter in this package works from the
// already-decoded typed pkg/schema structs the umbrella has already
// produced by the time a verb group's own report*TargetedOutcome/fan-out
// printing runs; it never reformats a raw JSON blob itself.
//
// Design choice (this bead's own open design question, explicitly left to
// design time rather than presumed): pg-connector selects human vs. JSON
// via an EXPLICIT --output json|human flag, defaulting to json
// everywhere — never TTY auto-detection. Recorded here since it is a
// design decision, not an implementation detail:
//
//   - pg-connector's primary caller is agent/automation tooling invoking
//     it as a subprocess (this repo's own domain), not a human at an
//     interactive terminal; JSON-by-default in EVERY context — including
//     a human's own interactive shell — is the safer default for that
//     primary caller. A human who wants the readable form opts in with
//     one flag.
//   - Auto-detection would make output shape depend on inherited process
//     plumbing (a real TTY vs. a pty allocated by some wrapper vs. a
//     pipe) that varies across CI systems, pty-allocating harnesses, and
//     even different invocations of the *same* script — a script that
//     works when run directly but silently changes shape under a tool
//     that allocates a pty is exactly the surprising, hard-to-reproduce
//     breakage this bead's "existing JSON-consuming scripts must keep
//     working unchanged" requirement guards against. An explicit flag has
//     no such environmental dependency: the same invocation always
//     produces the same shape, in CI and at a human's own prompt alike.
//   - It needs no new dependency (no golang.org/x/term or similar) and is
//     trivial to test deterministically — go test has no controlling
//     terminal to fake.
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// OutputMode is pg-connector's own CLI presentation mode — entirely
// separate from pkg/scriptout's wire protocol.
type OutputMode string

const (
	// OutputJSON prints the same raw wire envelope pg-connector has always
	// printed — the default, so every existing JSON-consuming script keeps
	// working unchanged with no flag added [bead pg2-ox1k6].
	OutputJSON OutputMode = "json"
	// OutputHuman prints a per-verb-group, human-readable rendering of the
	// already-decoded typed schema struct instead of the raw envelope.
	OutputHuman OutputMode = "human"
)

const outputFlagName = "output"

// addOutputFlag registers the --output flag once on root, as a persistent
// flag so every verb group (pr/ci/issue/scm/auth/config) inherits it
// without redeclaring it itself.
func addOutputFlag(root *cobra.Command) {
	root.PersistentFlags().String(outputFlagName, string(OutputJSON),
		"output format: json (default; the stable machine-readable wire envelope scripts/automation depend on) or human (readable summaries for interactive use)")
}

// outputModeFor reads cmd's --output flag (inherited from root). An
// unrecognized value is reported as pg-connector's own generic CLI
// failure (exit 1) — mirroring pr.go's own --disposition client-side
// validation.
//
// This function itself only reads and validates the flag; it does not by
// itself guarantee pre-dispatch timing. What makes an unrecognized value
// caught before any backend is ever dispatched is root.go's
// PersistentPreRunE, which calls outputModeFor and runs before every
// verb's own RunE (and therefore before that RunE's call to
// Dispatch/fanOut*) [bug A4, bead pg2-zc3b4]. writeTargetedResult and
// writeFanOutResult below also call outputModeFor, but only to look up
// the already-validated mode for rendering — by the time either runs,
// root's PersistentPreRunE has already rejected an invalid value with
// zero side effects, so their own call to outputModeFor cannot itself be
// where an invalid value's error path is reached in practice.
func outputModeFor(cmd *cobra.Command) (OutputMode, error) {
	raw, err := cmd.Flags().GetString(outputFlagName)
	if err != nil {
		return "", err
	}
	switch OutputMode(raw) {
	case OutputJSON, OutputHuman:
		return OutputMode(raw), nil
	default:
		return "", fmt.Errorf("pg-connector: --output %q must be one of json|human", raw)
	}
}

// humanizeResult formats a targeted op's successful raw result payload
// (resp.Result — already known to be present, on the non-error branch) as
// human-readable text. Each verb-group file (pr.go, ci.go, issue.go,
// scm.go) supplies its own, since the result shapes differ per op — this
// bead's own "one formatter per capability/verb group" requirement.
type humanizeResult func(result json.RawMessage) (string, error)

// writeTargetedResult is the shared write path behind every verb group's
// own report*TargetedOutcome wrapper (pr.go, ci.go, issue.go, scm.go). In
// OutputJSON mode (the default) it is byte-for-byte pg-connector's
// pre-existing behavior: resp's wire envelope written verbatim to stdout.
// In OutputHuman mode it writes humanize's formatted text on success, or a
// formatted one-line error on failure, INSTEAD of the raw envelope — never
// both in the same invocation (see this file's header comment). Either
// way it translates err into pg-connector's own targeted-op exit code via
// outcome.go's TargetedExitCode exactly as before; this function never
// decides the exit code itself. A nil resp is a CLI-level failure before
// any well-formed wire response was produced — returned as a plain error,
// unaffected by output mode, matching the pre-existing convention.
func writeTargetedResult(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	if resp == nil {
		return err
	}
	mode, modeErr := outputModeFor(cmd)
	if modeErr != nil {
		return modeErr
	}
	if mode == OutputHuman {
		if werr := writeHumanTargeted(cmd, resp, humanize); werr != nil {
			return werr
		}
	} else {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetEscapeHTML(false)
		if encErr := enc.Encode(resp); encErr != nil {
			return encErr
		}
	}
	if code := TargetedExitCode(err); code != 0 {
		return &exitError{code: code}
	}
	return nil
}

func writeHumanTargeted(cmd *cobra.Command, resp *scriptout.Response, humanize humanizeResult) error {
	out := cmd.OutOrStdout()
	if resp.Error != nil {
		fmt.Fprintf(out, "error: %s: %s\n", resp.Error.Code, resp.Error.Message)
		return nil
	}
	text, err := humanize(resp.Result)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, text)
	return nil
}

// writeFanOutResult is the shared write path behind every fan-out verb's
// own printing (ci.go's "ci list", auth.go's "auth status",
// config_validate.go's "config validate"). In OutputJSON mode (the
// default) it is byte-for-byte pg-connector's pre-existing behavior:
// jsonPayload encoded verbatim to stdout. In OutputHuman mode it writes
// humanize's formatted text instead. exitCode is computed by the caller
// exactly as before (FanOutOutcome.ExitCode / ciListOutcome.exitCode) —
// this function never decides it itself.
func writeFanOutResult(cmd *cobra.Command, jsonPayload any, exitCode int, humanize func() string) error {
	mode, modeErr := outputModeFor(cmd)
	if modeErr != nil {
		return modeErr
	}
	if mode == OutputHuman {
		fmt.Fprintln(cmd.OutOrStdout(), humanize())
	} else {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetEscapeHTML(false)
		if err := enc.Encode(jsonPayload); err != nil {
			return err
		}
	}
	if exitCode != 0 {
		return &exitError{code: exitCode}
	}
	return nil
}

// formatSourcesTable renders a fan-out outcome's sources[] rows — shared
// by "ci list" (ciListOutcome.Sources), "auth status", and
// "config validate" (both FanOutOutcome), since all three use the exact
// same SourceResult row shape. This is the fan-out outcome envelope's own
// compact rendering this bead names explicitly.
func formatSourcesTable(sources []SourceResult) string {
	if len(sources) == 0 {
		return "  (no backends registered)"
	}
	var b strings.Builder
	for i, s := range sources {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "  %s: %s", s.Source, s.Status)
		if s.Count > 0 {
			fmt.Fprintf(&b, " count=%d", s.Count)
		}
		if s.Reason != "" {
			fmt.Fprintf(&b, " (%s)", s.Reason)
		}
	}
	return b.String()
}
