// changes.go: the "changes" verb — runs
// `pg-connector <type> changes --query <query> --consumer <id> --output
// json` and reprints its wire response as one pg-router rawItem array
// [design: section 6.1].
package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// changesWire mirrors pg-connector's own already-landed "changes" JSON
// envelope (cmd/pg-connector/changes.go's changesWire) — decoded
// generically since this adapter has no compile-time dependency on
// packages/pg-connector [design: section 6.1].
type changesWire struct {
	Sources []changesSource `json:"sources"`
	Changes []changesEntry  `json:"changes"`
}

// changesSource is one row of the "changes" wire response's sources[]
// array. Only Backend/Status are needed here (building
// metadata.degraded_sources below); Version/Truncated are ignorable extra
// JSON for this call path.
type changesSource struct {
	Backend string `json:"backend"`
	Status  string `json:"status"`
}

// changesEntry is one row of the "changes" wire response's changes[]
// array.
type changesEntry struct {
	Change string          `json:"change"`
	Source string          `json:"source"`
	Entity json.RawMessage `json:"entity"`
}

// entityIdentity decodes the subset of an entity's own wire shape
// (schema.PR/schema.Issue) this file needs: its own id and, if
// non-empty, its own title. Every entity pg-connector returns is
// schema-conformant and always carries a non-null id/title (phase 7,
// landed) [design: section 6.1, Freedom boundary].
type entityIdentity struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func newChangesCmd() *cobra.Command {
	var consumer, beadsDir string
	cmd := &cobra.Command{
		Use:   "changes <type> <query>",
		Short: "Report entities changed since a consumer's last call, as pg-router rawItems",
		Args:  cobra.ExactArgs(2),
	}
	cmd.Flags().StringVar(&consumer, "consumer", "", "consumer id whose cursor pg-connector's own ledger reads and advances (required)")
	cmd.Flags().StringVar(&beadsDir, "beads-dir", "", "sets PG_CONNECTOR_ISSUE_BEADS_DIR in the pg-connector child's environment")
	_ = cmd.MarkFlagRequired("consumer")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		entityType, query := args[0], args[1]
		out, err := invokeOrFail(cmd.Context(), cmd.ErrOrStderr(),
			[]string{entityType, "changes", "--query", query, "--consumer", consumer, "--output", "json"},
			beadsDirEnv(beadsDir))
		if err != nil {
			return err
		}

		var wire changesWire
		if err := json.Unmarshal(out, &wire); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
			return errFailed
		}

		// The same degraded-backend list applies to every item printed
		// by this one invocation — it describes the OVERALL call's
		// degraded backends, never a per-entity fact [design: section
		// 6.1].
		degraded := make([]string, 0)
		for _, s := range wire.Sources {
			if s.Status == "degraded" {
				degraded = append(degraded, s.Backend)
			}
		}

		items := make([]rawItem, 0, len(wire.Changes))
		for _, c := range wire.Changes {
			var id entityIdentity
			if err := json.Unmarshal(c.Entity, &id); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				return errFailed
			}
			title := id.Title
			if title == "" {
				title = id.ID
			}
			items = append(items, rawItem{
				ID:    id.ID,
				Type:  entityType,
				Title: title,
				Metadata: map[string]any{
					"change":           c.Change,
					"source":           c.Source,
					"degraded_sources": degraded,
				},
			})
		}
		return writeItems(cmd.OutOrStdout(), items)
	}
	return cmd
}
