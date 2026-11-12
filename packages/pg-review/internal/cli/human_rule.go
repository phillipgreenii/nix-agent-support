package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/metrics"
)

func init() { registerCommand(groupHuman, newHumanRuleCmd) }

var rulingNames = map[string]string{
	"adopted":  ledger.RulingAdopted,
	"deferred": ledger.RulingDeferred,
	"rejected": ledger.RulingRejected,
	"reopen":   ledger.RulingReopen,
}

// beadFiler builds the tracker seam of the Adopted flow; tests replace it.
var beadFiler = func(cfg *config.Effective) ledger.BeadFiler {
	return ledger.BdFiler{Command: cfg.Tracker.Command}
}

func newHumanRuleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rule <id>... <ruling>",
		Short: "Rule on one or more shown items: adopted, deferred, rejected or reopen",
		Long: `Rule on items you have shown (human show). Each item must have been shown
since it last changed, else the command exits 6.

--reconsider-when takes clauses separated by ";" or ",":
  date:<YYYY-MM-DD or RFC3339>   rate-multiple:<x>   cost:<x>
  target-changed                 version-changed`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ruling, ok := rulingNames[strings.ToLower(args[len(args)-1])]
			if !ok {
				return usageErrorf("the last argument must be a ruling: adopted, deferred, rejected or reopen, got %q", args[len(args)-1])
			}
			ids, err := parseIDs(args[:len(args)-1])
			if err != nil {
				return err
			}
			note, _ := cmd.Flags().GetString("note")
			when, _ := cmd.Flags().GetString("reconsider-when")
			useful, _ := cmd.Flags().GetString("useful")
			if when != "" && !ledger.ValidCondition(when) {
				return usageErrorf("--reconsider-when %q has no recognised clause (date:<day>, rate-multiple:<x>, cost:<x>, target-changed, version-changed; separated by ; or ,)", when)
			}
			switch useful {
			case "", "yes", "no":
			default:
				return usageErrorf("--useful must be yes or no, got %q", useful)
			}
			cfg, err := config.Load(config.EnvFromOS(), assetsFS())
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			l, err := ledger.Open(ctx)
			if err != nil {
				return mapHumanError(err)
			}
			defer func() { _ = l.Close() }()
			reqs := make([]ledger.RuleRequest, 0, len(ids))
			for _, id := range ids {
				rq := ledger.RuleRequest{Number: id, Ruling: ruling, Rationale: note, ReconsiderWhen: when, Useful: useful}
				if ruling == ledger.RulingAdopted {
					it, err := l.ItemByNumber(ctx, id)
					if err != nil {
						return mapHumanError(fmt.Errorf("item %d: %w", id, err))
					}
					rq.TargetTracker = cfg.Tracker.Dir
					rq.RepoLabel = cfg.Tracker.LabelFor(it.Writeup.Targets)
				}
				reqs = append(reqs, rq)
			}
			results, rerr := l.RuleItems(ctx, reqs, cfg.Limits.ItemsMaxPending)
			if results == nil && rerr != nil {
				return mapHumanError(rerr)
			}
			if ruling == ledger.RulingAdopted {
				// File synchronously once; a failure leaves the ruling standing
				// with bead_state pending for maintain to retry.
				if _, err := ledger.FileAdoptedBeads(ctx, l, beadFiler(cfg)); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "work bead not filed yet: %v\n", err)
				}
			}
			// Gauges follow every ruling; best effort, never changes the outcome.
			metrics.Emit(ctx, l, l.Now())
			lines := make([]string, 0, len(results))
			for _, r := range results {
				line := fmt.Sprintf("%d  %s  -> %s", r.Number, oneLine(r.Title, 160), r.State)
				if r.Unchanged {
					line += " (unchanged)"
				}
				if ruling == ledger.RulingAdopted && r.DecisionID != 0 {
					if d, err := l.GetDecision(ctx, r.DecisionID); err == nil {
						line += "  bead: " + beadSummary(d)
					}
				}
				lines = append(lines, line)
			}
			if err := printJSONOrText(cmd, results, strings.Join(lines, "\n")); err != nil {
				return err
			}
			if rerr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", rerr)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.String("note", "", "a note to store with the ruling")
	f.String("reconsider-when", "", "when to reconsider, e.g. date:2026-12-01;target-changed")
	f.String("useful", "", "whether the item was useful: yes or no")
	return cmd
}

func beadSummary(d ledger.Decision) string {
	if d.BeadState == ledger.BeadFiled {
		return "filed " + oneLine(d.WorkBead, 60)
	}
	return d.BeadState
}
