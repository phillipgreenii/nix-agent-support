// ci.go: the "pg-connector ci" CLI verb group, built by the "generic ci
// entity/capability" packet on top of the Tier-1 core's registry/dispatcher
// and outcome-reporting helper. pg-connector remains the only user-facing
// CLI surface — ci is one of its verb groups, never a separate binary
// [design: §4, §6.1].
//
// connector.ci is list-valued (multiple simultaneously-registered CI
// backends, matching pr/issue) [design: §4.1]. "ci list" is therefore a
// FAN-OUT op — it queries every registered ci backend and concatenates
// their runs (the design's explicit "runs concatenates" merge strategy for
// the CI fan-out [design: §4.5]) — and uses the fan-out exit-code scheme
// (0/2/3) via outcome.go's FanOutOutcome.ExitCode. "ci logs" and
// "ci rerun-failed" are TARGETED ops (resolve to the one backend registered
// under connector.ci, mirroring pr.go's own targeted-op dispatch) and use
// the targeted exit-code scheme (0/4/1) via outcome.go's TargetedExitCode
// [design: §4.1, §4.5]. This file calls the dispatcher/fan-out helpers and
// hands their raw per-call result/error to outcome.go; it never decides the
// exit code itself.
package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// ciListOutcome is "ci list"'s wire response: every registered ci backend's
// runs concatenated into Runs, with each backend's own health as one row in
// Sources — never collapsed, matching the sources[] convention every
// fan-out in this design uses [design: §4.5].
type ciListOutcome struct {
	Runs    []schema.CIRun `json:"runs"`
	Sources []SourceResult `json:"sources"`
}

// exitCode delegates to the shared fan-out scheme (0/2/3) — the exact same
// classification logic auth.go/config_validate.go's fan-outs use, applied
// to ciListOutcome's own Sources rows.
func (o ciListOutcome) exitCode() int {
	return FanOutOutcome{Sources: o.Sources}.ExitCode()
}

// fanOutCIList queries "list_runs" against every backend in backends and
// concatenates their runs, building one sources[] row per backend queried.
// A backend not implementing list_runs (recognized generically via the
// wire-level unknown_op sentinel, matching auth.go's own convention) is
// reported as disabled with reason "not applicable" rather than a
// forced/meaningless answer.
func fanOutCIList(ctx context.Context, backends []string, prID string) ciListOutcome {
	var out ciListOutcome
	for _, b := range backends {
		resp, err := scriptout.Invoke(ctx, b, "list_runs", map[string]string{"pr_id": prID})
		if err != nil {
			if errors.Is(err, scriptout.ErrUnknownOp) {
				out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDisabled, Reason: "not applicable"})
				continue
			}
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		var runs []schema.CIRun
		if err := scriptout.Decode(resp.Result, &runs); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		out.Runs = append(out.Runs, runs...)
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(runs)})
	}
	return out
}

func newCiCmd() *cobra.Command {
	ciCmd := &cobra.Command{
		Use:   "ci",
		Short: "CI capability commands",
	}
	ciCmd.AddCommand(newCiListCmd())
	ciCmd.AddCommand(newCiLogsCmd())
	ciCmd.AddCommand(newCiRerunFailedCmd())
	return ciCmd
}

func newCiListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <pr-id>",
		Short: "List CI runs for a PR, fanned out across every registered ci backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			backends, err := reg.List("ci")
			if err != nil {
				return err
			}
			outcome := fanOutCIList(cmd.Context(), backends, args[0])
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetEscapeHTML(false)
			if err := enc.Encode(outcome); err != nil {
				return err
			}
			if code := outcome.exitCode(); code != 0 {
				return &exitError{code: code}
			}
			return nil
		},
	}
}

func newCiLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Get the raw logs for a CI run (targeted; resolves to the one registered ci backend)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "ci", "get_logs", map[string]string{"run_id": args[0]})
			return reportCiTargetedOutcome(cmd, resp, dispatchErr)
		},
	}
}

func newCiRerunFailedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rerun-failed <pr-id>",
		Short: "Rerun a PR's failed CI runs (targeted; resolves to the one registered ci backend)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "ci", "rerun_failed", map[string]string{"pr_id": args[0]})
			return reportCiTargetedOutcome(cmd, resp, dispatchErr)
		},
	}
}

// reportCiTargetedOutcome writes resp's wire envelope (its "result" on
// success, or its "error" body per the taxonomy on failure) to stdout —
// matching the wire protocol's own "only stdout JSON is the contract"
// convention — and translates err into pg-connector's own targeted-op exit
// code via outcome.go's TargetedExitCode, never deciding the exit code
// itself [design: §4.5]. Mirrors pr.go's reportPrTargetedOutcome. A nil
// resp is a CLI-level failure before any well-formed wire response was
// produced (e.g. no backend registered, or an ambiguous multi-backend
// registration) — that case is returned as a plain error instead, so
// main's run() reports it on stderr rather than fabricating a JSON body.
func reportCiTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error) error {
	if resp == nil {
		return err
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetEscapeHTML(false)
	if encErr := enc.Encode(resp); encErr != nil {
		return encErr
	}
	if code := TargetedExitCode(err); code != 0 {
		return &exitError{code: code}
	}
	return nil
}
