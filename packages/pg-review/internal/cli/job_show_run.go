package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

func init() { registerCommand(groupJob, newJobShowRunCmd) }

func newJobShowRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show-run <id>",
		Short: "Show one run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return usageErrorf("%q is not a run id", args[0])
			}
			cfg, err := config.Load(config.EnvFromOS(), assetsFS())
			if err != nil {
				return err
			}
			l, err := ledger.Open(cmd.Context())
			if err != nil {
				return ExitErrorf(exitcode.Ledger, "%v", err)
			}
			defer l.Close()
			r, err := l.GetRun(cmd.Context(), id)
			if errors.Is(err, ledger.ErrNotFound) {
				return ExitErrorf(exitcode.ValidationRejected, "run %d not found", id)
			}
			if err != nil {
				return err
			}
			ttl := cfg.Limits.EvidenceTTLDays
			base := r.EndedAt
			if base.IsZero() {
				base = r.CreatedAt
			}
			expiry := base.AddDate(0, 0, ttl)
			if !time.Now().Before(expiry) {
				return ExitErrorf(exitcode.ValidationRejected, "run %d expired on %s", id, expiry.Format("2006-01-02"))
			}
			return printJSONOrText(cmd, r, showRunText(r, expiry, ttl))
		},
	}
}

func showRunText(r ledger.Run, expiry time.Time, ttl int) string {
	exit := "-"
	switch r.State {
	case ledger.RunFiled:
		exit = "0"
	case ledger.RunSkipped:
		exit = "3"
	case ledger.RunFailed:
		exit = "1"
	}
	ts := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "run %d   lens %s   scope %s   state %s   exit %s\n", r.ID, r.Lens, r.Scope, r.State, exit)
	fmt.Fprintf(&b, "  window   %s .. %s      run key %s\n", r.WindowFrom, r.WindowTo, orDash(r.RunKey))
	fmt.Fprintf(&b, "  started  %s   ended %s (%.1fm)   profile %s   model %s   prompt hash %s\n",
		ts(r.CreatedAt), ts(r.EndedAt), r.ElapsedMinutes, orDash(r.Profile), orDash(r.Model), orDash(r.PromptHash))
	fmt.Fprintf(&b, "  analyst  %s   queries %d   repair replies %d\n", orDash(r.Analyst), r.QueryCount, r.RepairCount)
	fmt.Fprintf(&b, "  items    filed %d, deduplicated %d, skipped as already decided %d\n", r.ItemsFiled, r.ItemsDeduplicated, r.ItemsSkipped)
	if r.SkipReason != "" {
		fmt.Fprintf(&b, "  reason   %s\n", r.SkipReason)
	}
	fmt.Fprintf(&b, "  evidence kept until %s (evidence_ttl_days %d)", expiry.Format("2006-01-02"), ttl)
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
