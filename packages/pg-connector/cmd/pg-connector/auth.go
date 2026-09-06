// auth.go: the "pg-connector auth status" Tier-1-only CLI verb.
//
// It fans out the existing auth_status op across every registered backend
// regardless of capability (rather than belonging to any one entity type)
// via the sources[] outcome envelope. A backend not implementing/answering
// auth_status is reported as disabled with reason "not applicable" rather
// than a forced/meaningless answer — recognized generically here by the
// wire-level unknown_op sentinel, since a capability's dispatch table
// simply omits an auth_status entry when its provider doesn't implement
// pkg/provider.AuthChecker.
package main

import (
	"context"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// FanOutAuthStatus fans the auth_status op out across every backend in
// backends, building the sources[] envelope. Sources starts as a non-nil
// empty slice so a zero-backend (misconfigured host) result still
// marshals its sources[] field as [] rather than null [bug A15] — a
// nil slice would make `jq '.sources[]'` exit 5 on exactly the host
// that's misconfigured.
func FanOutAuthStatus(ctx context.Context, backends []string) FanOutOutcome {
	out := FanOutOutcome{Sources: make([]SourceResult, 0, len(backends))}
	for _, b := range backends {
		out.Sources = append(out.Sources, authStatusOne(ctx, b))
	}
	return out
}

func authStatusOne(ctx context.Context, backend string) SourceResult {
	resp, err := scriptout.Invoke(ctx, backend, scriptout.OpAuthStatus, nil)
	if err != nil {
		if errors.Is(err, scriptout.ErrUnknownOp) {
			return SourceResult{Source: backend, Status: SourceDisabled, Count: 0, Reason: "not applicable"}
		}
		return SourceResult{Source: backend, Status: SourceDegraded, Count: 0, Reason: err.Error()}
	}

	var status scriptout.AuthStatus
	if err := scriptout.Decode(resp.Result, &status); err != nil {
		return SourceResult{Source: backend, Status: SourceDegraded, Count: 0, Reason: err.Error()}
	}
	if status.State == scriptout.AuthOK {
		return SourceResult{Source: backend, Status: SourceSucceeded, Count: 0}
	}
	reason := string(status.State)
	if status.Detail != "" {
		reason = reason + ": " + status.Detail
	}
	return SourceResult{Source: backend, Status: SourceDegraded, Count: 0, Reason: reason}
}

func newAuthCmd() *cobra.Command {
	authCmd := &cobra.Command{
		Use:   "auth",
		Short: "Auth-related commands",
	}
	authCmd.AddCommand(newAuthStatusCmd())
	return authCmd
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Fan auth_status out across every registered backend",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			backends, err := reg.AllBackends()
			if err != nil {
				return err
			}
			outcome := FanOutAuthStatus(cmd.Context(), backends)
			return writeFanOutResult(cmd, outcome, outcome.ExitCode(), func() string {
				return "auth status:\n" + formatSourcesTable(outcome.Sources)
			})
		},
	}
}
