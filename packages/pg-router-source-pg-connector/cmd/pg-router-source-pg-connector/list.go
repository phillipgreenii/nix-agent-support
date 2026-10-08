// list.go: the "list" verb — runs
// `pg-connector <type> list --query <query> --backend <binary> --output
// json` (a FULL, non---ids-only fetch), applies the two post-filters
// (--title-prefix, --issue-type) exactly reproducing today's jq
// pipelines, and prints one pg-router rawItem per surviving entity
// [design: section 6.1].
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// listWire is the "list" (full, non---ids-only) wire response's shape
// this file needs: the entities array. present_ids/sources are always
// present too (same struct on pg-connector's side) but are harmless,
// ignorable extra JSON for this particular call path — this subcommand
// reads entities, never present_ids [design: section 6.1].
type listWire struct {
	Entities []json.RawMessage `json:"entities"`
}

// listEntity decodes the subset of an entity's own wire shape
// (schema.PR/schema.Issue) this file needs. issue_type/metadata are
// present on schema.Issue and absent on schema.PR — decoding a PR entity
// here leaves them at their zero value ("" / nil) rather than erroring,
// which is fine: this adapter's Contract does not need defensive
// handling for a missing field, only the already-stated
// empty-title-string fallback to id [design: section 6.1, Freedom
// boundary].
type listEntity struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	IssueType string         `json:"issue_type"`
	Metadata  map[string]any `json:"metadata"`
}

func newListCmd() *cobra.Command {
	var backend, beadsDir, titlePrefix, issueType string
	cmd := &cobra.Command{
		Use:   "list <type> <query>",
		Short: "List entities matching a named query, as pg-router rawItems",
		Args:  cobra.ExactArgs(2),
	}
	cmd.Flags().StringVar(&backend, "backend", "", "pin the fan-out to exactly this registered backend (required) — selects which backend's own <query> block resolves")
	cmd.Flags().StringVar(&beadsDir, "beads-dir", "", "sets PG_CONNECTOR_ISSUE_BEADS_DIR in the pg-connector child's environment")
	cmd.Flags().StringVar(&titlePrefix, "title-prefix", "", "keep only entities whose title field starts with this prefix (case-sensitive, exact prefix match)")
	cmd.Flags().StringVar(&issueType, "issue-type", "", "keep only entities whose own issue-type field equals this exactly")
	_ = cmd.MarkFlagRequired("backend")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		entityType, query := args[0], args[1]
		out, err := invokeOrFail(cmd.Context(), cmd.ErrOrStderr(),
			[]string{entityType, "list", "--query", query, "--backend", backend, "--output", "json"},
			beadsDirEnv(beadsDir))
		if err != nil {
			return err
		}

		var wire listWire
		if err := json.Unmarshal(out, &wire); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
			return errFailed
		}

		items := make([]rawItem, 0, len(wire.Entities))
		for _, raw := range wire.Entities {
			var e listEntity
			if err := json.Unmarshal(raw, &e); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				return errFailed
			}
			// Both filters apply (AND) when both flags are given; either
			// alone applies only its own filter; neither given means no
			// filtering [design: section 6.1].
			if titlePrefix != "" && !strings.HasPrefix(e.Title, titlePrefix) {
				continue
			}
			if issueType != "" && e.IssueType != issueType {
				continue
			}
			title := e.Title
			if title == "" {
				title = e.ID
			}
			items = append(items, rawItem{
				ID:       e.ID,
				Type:     e.IssueType,
				Title:    title,
				Metadata: e.Metadata,
			})
		}
		return writeItems(cmd.OutOrStdout(), items)
	}
	return cmd
}
