// sweep.go: the "sweep" verb — runs
// `pg-connector <type> list --query <query> --ids-only --output json`
// once per named query, unions the matched ids across all of them, and
// prints one pg-router rawItem per unioned id [design: section 6.1].
package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// idsOnlyWire is the "list --ids-only" wire response's shape this file
// needs: the TOP-LEVEL present_ids array. entities is always empty for
// --ids-only (this packet's own Contract "Consumes" section) and is
// deliberately not decoded here — reading it for ids would silently drop
// every id on every real invocation [design: section 6.1].
type idsOnlyWire struct {
	PresentIDs []string `json:"present_ids"`
}

func newSweepCmd() *cobra.Command {
	var beadsDir string
	cmd := &cobra.Command{
		Use:   "sweep <type> <query>...",
		Short: "Union matched ids across one or more named queries, as pg-router rawItems",
		// At least the <type> argument must be present; whether at least
		// one <query> name follows it is this RunE's own job to check
		// below (there is no pg-connector subprocess to consult for a
		// zero-query invocation, so it is classified directly) [design:
		// section 6.1, this packet's own "sweep" Produces bullet].
		Args: cobra.MinimumNArgs(1),
	}
	cmd.Flags().StringVar(&beadsDir, "beads-dir", "", "sets PG_CONNECTOR_ISSUE_BEADS_DIR in the pg-connector child's environment")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		entityType := args[0]
		queries := args[1:]
		if len(queries) == 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "pg-router-source-pg-connector sweep: at least one query name is required")
			return errFailed
		}

		env := beadsDirEnv(beadsDir)
		seen := make(map[string]bool)
		ordered := make([]string, 0)
		for _, query := range queries {
			out, err := invokeOrFail(cmd.Context(), cmd.ErrOrStderr(),
				[]string{entityType, "list", "--query", query, "--ids-only", "--output", "json"},
				env)
			if err != nil {
				return err
			}
			var wire idsOnlyWire
			if err := json.Unmarshal(out, &wire); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				return errFailed
			}
			for _, id := range wire.PresentIDs {
				if !seen[id] {
					seen[id] = true
					ordered = append(ordered, id)
				}
			}
		}

		// Sweep does not run a second, non---ids-only fetch to backfill
		// title/other metadata: its whole purpose is a cheap
		// reconciliation signal by id, not a content refresh. Title
		// equals the id (present_ids carries no title), and metadata is
		// exactly {"change": "sweep"} — no other keys [design: section
		// 6.1].
		items := make([]rawItem, 0, len(ordered))
		for _, id := range ordered {
			items = append(items, rawItem{
				ID:       id,
				Type:     entityType,
				Title:    id,
				Metadata: map[string]any{"change": "sweep"},
			})
		}
		return writeItems(cmd.OutOrStdout(), items)
	}
	return cmd
}
