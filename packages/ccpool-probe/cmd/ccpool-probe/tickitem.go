// tickitem.go: the "tick-item" verb — a trivial, side-effect-free item
// emitter so pg-router has something to dispatch on a schedule [design:
// "pg-router-probe and ccpool-probe" two-bullet list, first bullet].
// Mirrors packages/pg-router-probe/cmd/pg-router-probe/tickitem.go and,
// through it, packages/pg-desk/cmd/pg-desk/heartbeat_item.go's own
// heartbeatItem shape exactly (a `format = "json"` command query decodes
// stdout as a JSON array of these) — tick-item touches nothing (no ccpool
// call, no pg-connector call, no snapshot read/write); every real side
// effect lives in run.go's run command instead.
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// tickItemNow is the clock; overridable in tests.
var tickItemNow = func() string { return time.Now().UTC().Format(time.RFC3339) }

// tickItem mirrors pg-desk's heartbeatItem / pg-router-probe's own
// tickItem / pg-router's own rawItem shape
// (packages/pg-router/internal/query/command.go): id/type/title/metadata.
type tickItem struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata"`
}

func newTickItemCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tick-item",
		Short: "Print the ccpool-probe schedule item, performing no other side effect",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			items := []tickItem{{
				ID:       tickItemNow(),
				Type:     "ccpool-probe-tick",
				Title:    "ccpool-probe tick",
				Metadata: map[string]any{},
			}}
			b, err := json.Marshal(items)
			if err != nil {
				return usageErrorf("tick-item: marshal json: %v", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			return err
		},
	}
}
