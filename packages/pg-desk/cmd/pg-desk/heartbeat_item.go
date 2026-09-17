package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// heartbeatItemNow is the clock; overridable in tests.
var heartbeatItemNow = func() string { return time.Now().UTC().Format(time.RFC3339) }

// heartbeatItem mirrors pg-router's rawItem shape
// (packages/pg-router/internal/query/command.go) — the one this command's
// stdout MUST decode as, since the `desk-heartbeat` query
// [docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md
// section 6.1: `command = { format = "json", argv = ["pg-desk",
// "heartbeat-item"] }`] is a `format = "json"` command query, which decodes
// stdout as a JSON ARRAY of these.
type heartbeatItem struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata"`
}

// heartbeatItemCmd implements `pg-desk heartbeat-item`: prints the single
// pr-pool/pg-router item the `desk-heartbeat` query emits, with the
// current RFC3339 timestamp as its id — "because the queue de-duplicates a
// repeated id while it is retained" [design doc section 6.1]. It touches
// neither the store nor the pipeline (this packet's own Acceptance
// Criteria: "heartbeat-item prints its synthetic query item WITHOUT
// touching the store or invoking the pipeline") — it is a pure data
// generator the SCHEDULER polls; `pg-desk heartbeat` (heartbeat.go) is the
// separate command the scheduler's `desk-heartbeat` ROLE runs in response
// to the event this item's presence triggers, and that command is the one
// that actually stamps meta.last_heartbeat.
var heartbeatItemCmd = &cobra.Command{
	Use:   "heartbeat-item",
	Short: "Print the desk-heartbeat pr-pool item",
	RunE: func(cmd *cobra.Command, args []string) error {
		items := []heartbeatItem{{
			ID:       heartbeatItemNow(),
			Type:     "heartbeat",
			Title:    "pg-desk heartbeat",
			Metadata: map[string]any{},
		}}
		b, err := json.Marshal(items)
		if err != nil {
			return fmt.Errorf("heartbeat-item: marshal json: %w", err)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
		return err
	},
}

func init() {
	rootCmd.AddCommand(heartbeatItemCmd)
}
