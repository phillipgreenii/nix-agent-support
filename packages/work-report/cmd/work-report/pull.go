package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pull"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/telemetry"
)

func init() { registerCommand(newPullCmd) }

// pullNow is the clock the pull verb resolves its range against; tests replace
// it.
var pullNow = time.Now

// pullJSON is the --output json document.
type pullJSON struct {
	Sources []pull.OutcomeRow `json:"sources"`
}

// newPullCmd builds `pull`: fetch activity from pg-connector for a range and
// append it to the store. Exit 0 when every attempted source succeeded, 2 when
// any degraded, 3 when all did, computed from work-report's own outcome rows.
func newPullCmd() *cobra.Command {
	var rangeSpec string
	var sources []string
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Pull activity from pg-connector into the store",
		Long: "Execs `pg-connector activity list` for the range, validates and maps each item " +
			"to an entry, and appends it to the store (an item already stored unchanged is not " +
			"appended again). One outcome row per source is stored and logged. A pull never " +
			"fails as a whole for one source: the exit code is 0 when every attempted source " +
			"succeeded, 2 when any degraded, and 3 when all did.\n\nRange forms: " + rangespec.Forms + ".",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, rawOut, storeFlag := globalFlags(cmd)
			format, err := outputFormat(rawOut)
			if err != nil {
				return err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			loc, err := cfg.Location()
			if err != nil {
				return err
			}
			now := pullNow()
			rng, err := rangespec.Resolve(rangeSpec, now, loc)
			if err != nil {
				return err
			}
			opts := pull.Opts{Range: rng, Sources: sources, Now: now}
			deps := pull.Deps{Cfg: cfg, Conn: pgconn.NewExec(), Log: telemetry.New("")}

			st, err := store.Open(config.StorePath(cfg, storeFlag))
			var res pull.Result
			switch {
			case errors.Is(err, store.ErrLocked):
				res = pull.Unavailable(deps, opts, fmt.Sprintf("store locked: %v", err))
			case err != nil:
				return err
			default:
				defer func() { _ = st.Close() }()
				deps.Store = st
				res, err = pull.Run(cmd.Context(), deps, opts)
				if err != nil {
					return err
				}
			}

			if format == "json" {
				err = writeJSON(cmd.OutOrStdout(), pullJSON{Sources: nonNilRows(res.Rows)})
			} else {
				err = writePullHuman(cmd.OutOrStdout(), res.Rows)
			}
			if err != nil {
				return err
			}
			if code := res.ExitCode(); code != 0 {
				return &exitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rangeSpec, "range", "today", "range to pull: "+rangespec.Forms)
	cmd.Flags().StringArrayVar(&sources, "source", nil, "pull only this backend (repeatable); default is every source in one fan-out call")
	return cmd
}

func nonNilRows(rows []pull.OutcomeRow) []pull.OutcomeRow {
	if rows == nil {
		return []pull.OutcomeRow{}
	}
	return rows
}

func writePullHuman(w io.Writer, rows []pull.OutcomeRow) error {
	var b strings.Builder
	if len(rows) == 0 {
		b.WriteString("no sources reported\n")
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "%s: %s", r.Source, r.Status)
		if r.Status != pull.StatusDisabled {
			fmt.Fprintf(&b, " (%d stored, %d unchanged, %d rejected)", r.Count, r.Unchanged, r.Rejected)
		}
		if r.Truncated {
			b.WriteString(" [truncated]")
		}
		if r.Reason != "" {
			fmt.Fprintf(&b, ": %s", r.Reason)
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
