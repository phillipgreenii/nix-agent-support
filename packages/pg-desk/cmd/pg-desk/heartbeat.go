package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// heartbeatNow is the clock; overridable in tests.
var heartbeatNow = func() string { return time.Now().UTC().Format(time.RFC3339) }

// heartbeatCmd implements `pg-desk heartbeat`: stamps meta.last_heartbeat
// [docs/behavior/pg-desk/operator-commands.md]. This is the command the
// `desk-heartbeat` ccpool role runs (design section 6.1's
// `command.argv = ["pg-desk", "heartbeat"]`) each time the `desk-heartbeat`
// query (heartbeat_item.go) emits a fresh item — it consumes
// meta.last_heartbeat's own staleness bound (packet 7's serve).
var heartbeatCmd = &cobra.Command{
	Use:   "heartbeat",
	Short: "Stamp the store's last-heartbeat time",
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := deskStoreOpen()
		if err != nil {
			return fmt.Errorf("heartbeat: open store: %w", err)
		}
		defer func() { _ = st.Close() }()

		if err := st.SetMeta(store.MetaKeyLastHeartbeat, heartbeatNow()); err != nil {
			return fmt.Errorf("heartbeat: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(heartbeatCmd)
}
