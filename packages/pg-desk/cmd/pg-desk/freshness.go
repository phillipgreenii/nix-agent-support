package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/freshness"
)

// freshnessNow is the clock; overridable in tests.
var freshnessNow = func() time.Time { return time.Now().UTC() }

// freshnessCmd implements `pg-desk freshness --json`
// [docs/behavior/pg-desk/freshness.md]: the per-source data age, read from
// the store only (INV-FRESH-1; no network, no connector call, no write). A
// consumer such as the menu bar plugin calls it on every refresh, whether or
// not its feed has items, and renders a row only for a stale source. Exit 0
// whenever the store is readable; 1 otherwise.
var freshnessCmd = &cobra.Command{
	Use:   "freshness [--json]",
	Short: "Print the age of each source's last successful fetch (read-only, local store only)",
	Long: `Print, as JSON, the per-source data age: for each connector source, the time of its
last SUCCESSFUL origin fetch, its age in seconds, and whether that is past the staleness
threshold (freshness.source_stale_after, default 15m). A source with no recorded success has
null last_success_at and age_seconds and is stale. Reads the store only: no network, no
connector call, no write. Exit 0 whenever the store is readable; exit 1 otherwise. Output is
always JSON.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return execFreshness(cmd)
	},
}

func init() {
	freshnessCmd.Flags().Bool("json", false,
		"Accepted for symmetry with the other verbs; the output is always JSON")
	rootCmd.AddCommand(freshnessCmd)
}

func execFreshness(cmd *cobra.Command) error {
	// An unloadable config degrades to the default threshold and labels
	// rather than failing the verb: a freshness indicator that disappears
	// because of a config error would turn a stale source into a false
	// all-clear. The failure is named on stderr.
	cfg, cfgErr := deskConfigLoad(cmd.Context())
	if cfgErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "freshness: config not loaded, using defaults: %v\n", cfgErr)
		cfg = &config.Config{}
	}
	st, err := deskStoreOpenReadOnly()
	if err != nil {
		return fmt.Errorf("freshness: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	rep, err := freshness.Build(st, cfg, freshnessNow())
	if err != nil {
		return fmt.Errorf("freshness: %w", err)
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("freshness: marshal json: %w", err)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return err
}
