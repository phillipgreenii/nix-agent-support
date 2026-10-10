package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/degraded"
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

// formatPGRouter is the --output value that makes pull print pg-router items.
const formatPGRouter = "pg-router"

// pullOutputFormat is outputFormat plus the pg-router mode only pull offers.
func pullOutputFormat(raw string) (string, error) {
	if raw == formatPGRouter {
		return formatPGRouter, nil
	}
	f, err := outputFormat(raw)
	if err != nil {
		return "", fmt.Errorf("--output %q is not supported here: want json, human or %s", raw, formatPGRouter)
	}
	return f, nil
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
			"succeeded, 2 when any degraded, and 3 when all did.\n\n" +
			"A degraded source is backed by one open escalation bead (created, appended to, " +
			"or closed again through `pg-connector issue`) in every output mode. --output " +
			"pg-router prints a JSON array of pg-router items, one per degraded source and " +
			"none for a healthy pull, and exits non-zero only when the pull could not run at all.\n\n" +
			"Range forms: " + rangespec.Forms + ".",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, rawOut, storeFlag := globalFlags(cmd)
			format, err := pullOutputFormat(rawOut)
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

			var items []degraded.Item
			if !res.CouldNotRun {
				// the tracker may be unreachable and a transient lock must not open
				// beads, so a pull that could not run reconciles nothing.
				var recErr error
				items, recErr = degraded.Reconcile(cmd.Context(), deps.Conn, degraded.ResolveBackend(os.Getenv(degraded.EnvBackend)), res.Rows, rng,
					now.In(loc), repullCommand(rangeSpec, sources))
				if recErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "work-report: degraded-source bead reconcile: %v\n", recErr)
				}
			}

			switch format {
			case formatPGRouter:
				if items == nil {
					items = []degraded.Item{}
				}
				if err := writeJSON(cmd.OutOrStdout(), items); err != nil {
					return err
				}
				if res.CouldNotRun {
					return &exitError{code: 1, msg: "work-report: the pull could not run: " + couldNotRunReason(res.Rows)}
				}
				return nil // per-source degradation is data, not a failure
			case "json":
				err = writeJSON(cmd.OutOrStdout(), pullJSON{Sources: nonNilRows(res.Rows)})
			default:
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

// repullCommand renders the exact command that repeats this pull's range and
// source pins, for the body of a degraded-source bead.
func repullCommand(rangeSpec string, sources []string) string {
	parts := []string{"work-report", "pull", "--range", shellQuote(rangeSpec)}
	for _, s := range sources {
		parts = append(parts, "--source", shellQuote(s))
	}
	return strings.Join(parts, " ")
}

// shellQuote single-quotes s unless it is made only of shell-safe characters.
func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:/@+=,", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// couldNotRunReason is the first degraded row's reason.
func couldNotRunReason(rows []pull.OutcomeRow) string {
	for _, r := range rows {
		if r.Status == pull.StatusDegraded && r.Reason != "" {
			return r.Reason
		}
	}
	return "see the pull rows"
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
