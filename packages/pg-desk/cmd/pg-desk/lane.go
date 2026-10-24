package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/lanestate"
)

// laneNow is the clock the lane-state write path stamps with; tests replace it.
var laneNow = func() time.Time { return time.Now().UTC() }

func init() {
	g := typeGroup(entityTypePR)
	g.AddCommand(newLaneRecordCmd())
	g.AddCommand(newMarkInactiveCmd())
}

// newLaneRecordCmd builds `pg-desk pr lane-record [--input FILE]`: the
// pg-desk-owned write path for the PR lane's persisted state. The composed
// command (another binary) calls it once after each run, with a JSON document
// (lanestate.RunInput) on standard input. pg-desk owns the key names, the
// atomic counter increments and the lane-level mode rules, so the caller never
// touches the store.
func newLaneRecordCmd() *cobra.Command {
	var input string
	c := &cobra.Command{
		Use:   "lane-record",
		Short: "Record one PR lane run (counters, per-PR and lane-level state)",
		Long: `Record the outcome of one run of the PR lane's composed command in the store's
meta key and value table. No schema change.

The run is a JSON document on standard input (or --input FILE):

  {"pr": "OWNER/REPO#N", "mode": "plan|apply|off",
   "events": [{"stage": "hydrate|decide", "class": "<class>"}],
   "residual": 0, "last_outcome": "<label>",
   "decisions": [{"rule": "...", "action": "...", "outcome": "...", "reason": "...", "count": 1}],
   "lookup_items": 0, "lookup_ceiling": 0}

pr accepts any reference "pr show" accepts and is stored normalized. A value
outside its closed vocabulary rejects the whole document and writes nothing.
Counters are incremented in one SQL statement each, so concurrent runs lose no
increment. The lane-level mode, the flip time and the PR's own state are
written only when the run's mode equals the configured pr_lane.mode, so a
by-hand plan run after the flip leaves an apply-mode residual alone. Exit 0
recorded; 1 any error (nothing recorded).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaneRecord(cmd, input)
		},
	}
	c.Flags().StringVar(&input, "input", "-", "File holding the run's JSON document (- for standard input)")
	return c
}

func runLaneRecord(cmd *cobra.Command, input string) error {
	var r io.Reader = cmd.InOrStdin()
	if input != "-" {
		f, err := os.Open(input)
		if err != nil {
			return fmt.Errorf("lane-record: %w", err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var in lanestate.RunInput
	if err := dec.Decode(&in); err != nil {
		return fmt.Errorf("lane-record: decode run: %w", err)
	}
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("lane-record: load config: %w", err)
	}
	_, id, err := resolveTypedRef(cfg, entityTypePR, in.PR)
	if err != nil {
		return fmt.Errorf("lane-record: %w", err)
	}
	in.PR = id
	if err := in.Validate(); err != nil {
		return fmt.Errorf("lane-record: %w", err)
	}
	st, err := openNewSchemaStore("lane-record")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cmd.SilenceUsage = true
	if err := lanestate.Record(st, cfg.PRLaneMode(), laneNow(), in); err != nil {
		return fmt.Errorf("lane-record: %w", err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "recorded pr %s: mode %s\n", id, in.Mode)
	return err
}

// newMarkInactiveCmd builds `pg-desk pr mark-inactive <id>`: mark one PR
// inactive in the store and count it, so a PR the source reports not found
// (and that has no open anchor) drops out of the never-hydrated set. It is a
// store write, never a tracker write, so a run in either lane mode may call it.
func newMarkInactiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mark-inactive <id>",
		Short: "Mark one PR inactive in the store and count it",
		Long: `Set the PR's active flag to 0 and add one to the marked-inactive counter, in one
transaction. The snapshot, version and hydrated_at are untouched, so a later
successful hydration brings the PR back (it writes active = 1). A PR that is
absent or already inactive is left alone, counts nothing and exits 0. This is
a store write only: it never calls the tracker or the connector, so a run in
either lane mode may call it. Exit 0 done; 1 any error.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMarkInactive(cmd, args[0])
		},
	}
}

func runMarkInactive(cmd *cobra.Command, ref string) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("mark-inactive: load config: %w", err)
	}
	repo, id, err := resolveTypedRef(cfg, entityTypePR, ref)
	if err != nil {
		return fmt.Errorf("mark-inactive: %w", err)
	}
	st, err := openNewSchemaStore("mark-inactive")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cmd.SilenceUsage = true
	changed, err := lanestate.MarkInactive(st, repo, id)
	if err != nil {
		return fmt.Errorf("mark-inactive: %w", err)
	}
	if !changed {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "pr %s is absent or already inactive; nothing to do\n", id)
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "marked pr %s inactive\n", id)
	return err
}
