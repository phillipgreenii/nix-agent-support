// agentsession.go: the agentsession capability's Tier-1 verb group —
// show/list only (read-only). show is a targeted, id-keyed op
// (DispatchTargeted, mirroring issue.go's show); list is NOT id-keyed and
// has exactly one intended backend today, so it uses the simpler Dispatch
// helper (hard-fails at N>1 with no pin) rather than ci.go's fan-out
// Sources/outcome-struct machinery, which this capability does not need
// (YAGNI).
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func newAgentSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agentsession",
		Short: "Query Claude Code agent sessions (pa-monitor-backed)",
	}
	cmd.AddCommand(newAgentSessionShowCmd())
	cmd.AddCommand(newAgentSessionListCmd())
	return cmd
}

func newAgentSessionShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one agent session's current state",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportAgentSessionTargetedOutcome(cmd, nil, err, humanizeAgentSession)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "agentsession", "show", map[string]string{"id": args[0]}, *backendFlag)
		return reportAgentSessionTargetedOutcome(cmd, resp, dispatchErr, humanizeAgentSession)
	}
	return cmd
}

func newAgentSessionListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agent sessions from the registered pa-monitor backend",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend (agentsession has one intended backend today, so this only guards against a stale/mistyped name)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportAgentSessionTargetedOutcome(cmd, nil, err, humanizeAgentSessionList)
		}
		resp, dispatchErr := Dispatch(cmd.Context(), reg, "agentsession", "list", map[string]bool{"ids_only": false}, *backendFlag)
		return reportAgentSessionTargetedOutcome(cmd, resp, dispatchErr, humanizeAgentSessionList)
	}
	return cmd
}

func reportAgentSessionTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

func humanizeAgentSession(raw json.RawMessage) (string, error) {
	var s schema.AgentSession
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	status := s.Status
	if s.Blocker != "" {
		status += "/" + s.Blocker
	}
	return fmt.Sprintf("session_id: %s\nstatus:     %s\nmodel:      %s\ncwd:        %s\ntokens:     %d\ncost_usd:   $%.2f\n",
		s.SessionID, status, s.Model, s.Cwd, s.Tokens, s.CostUSD), nil
}

func humanizeAgentSessionList(raw json.RawMessage) (string, error) {
	var l schema.AgentSessionListResult
	if err := json.Unmarshal(raw, &l); err != nil {
		return "", err
	}
	if len(l.Entities) == 0 {
		return "agent sessions: (none)\n", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "agent sessions (%d):\n", len(l.Entities))
	for _, s := range l.Entities {
		status := s.Status
		if s.Blocker != "" {
			status += "/" + s.Blocker
		}
		fmt.Fprintf(&b, "  %-12s %-20s %s\n", s.SessionID, status, s.Cwd)
	}
	return b.String(), nil
}
