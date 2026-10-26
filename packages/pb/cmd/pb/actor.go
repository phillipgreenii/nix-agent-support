package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/pb/internal/actor"
	"github.com/spf13/cobra"
)

func newActorCmd() *cobra.Command {
	var role, scratchpad string
	cmd := &cobra.Command{
		Use:   "actor",
		Short: "Print this session's actor id for a role (<session-id>-<role>)",
		Long: `Prints the id a session passes as "--actor" on every bd claim, unclaim, comment,
gate and close: the session identifier plus a role suffix. Run it ONCE per session
and reuse the printed value.

Session identifier, first match wins:
  1. $CLAUDE_SESSION_ID (else $CLAUDE_CODE_SESSION_ID), stable across compaction;
  2. the UUID inside --scratchpad PATH, the session's OWN private path (never a
     shared one, or two sessions collide); PATH is only parsed, never read;
  3. a fresh random UUID. This command cannot persist it: the caller MUST keep
     the printed id for the whole session (a note is printed on stderr).

The role suffix is load-bearing, so the roles' ids differ for the same session:
/drain-beads' resume query recovers every in-progress bead of its own actor, and a
dispatched subagent must never derive its orchestrator's actor id (pg2-mcp1j).
Valid roles: ` + strings.Join(actor.Roles, ", ") + `.

Output: the actor id and a newline on stdout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := actor.Derive(actor.Params{Role: role, Scratchpad: scratchpad, Getenv: os.Getenv})
			if err != nil {
				return err
			}
			if res.Source == actor.SourceRandom {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"pb actor: no session id in the environment or --scratchpad; generated a random one. Remember this id for the whole session: running this again prints a different one.")
			}
			fmt.Fprintln(cmd.OutOrStdout(), res.Actor)
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "actor role suffix: "+strings.Join(actor.Roles, ", ")+" (required)")
	cmd.Flags().StringVar(&scratchpad, "scratchpad", "", "the session's own private (scratchpad) path; its UUID is the fallback session id")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}
