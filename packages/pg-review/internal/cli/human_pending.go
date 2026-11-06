package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

// MaintainPeriod is how often maintain runs; a heartbeat older than twice it is stale.
const MaintainPeriod = 5 * time.Minute

func init() { registerCommand(groupHuman, newHumanPendingCmd) }

// pendingBanner is the first line of `pending`.
type pendingBanner struct {
	Pending       int      `json:"pending"`
	OldestAgeSecs float64  `json:"oldest_age_seconds"`
	Held          []string `json:"lenses_held"`
	Stale         []string `json:"lenses_stale"`
	LastRuns      []string `json:"last_runs"`
	MaintainStale string   `json:"maintain,omitempty"`
}

func (b pendingBanner) line() string {
	none := func(s []string) string {
		if len(s) == 0 {
			return "none"
		}
		return strings.Join(s, ",")
	}
	parts := []string{
		fmt.Sprintf("Pending: %d", b.Pending),
		"oldest: " + map[bool]string{true: "-", false: ageText(b.OldestAgeSecs)}[b.Pending == 0],
		"held: " + none(b.Held),
		"stale: " + none(b.Stale),
		"last runs: " + none(b.LastRuns),
	}
	if b.MaintainStale != "" {
		parts = append(parts, "maintain: "+b.MaintainStale)
	}
	return strings.Join(parts, " | ")
}

type pendingItem struct {
	Number   int64  `json:"id"`
	Lens     string `json:"lens"`
	Title    string `json:"title"`
	Impact   string `json:"impact"`
	Evidence int    `json:"evidence"`
}

func buildBanner(ctx context.Context, l *ledger.Ledger, items []ledger.Item) (pendingBanner, error) {
	now := l.Now()
	b := pendingBanner{Pending: len(items)}
	for _, it := range items {
		if age := now.Sub(it.UpdatedAt).Seconds(); age > b.OldestAgeSecs {
			b.OldestAgeSecs = age
		}
	}
	lenses, err := l.Lenses(ctx)
	if err != nil {
		return b, err
	}
	for _, lens := range lenses {
		p, err := l.IsLensPaused(ctx, lens)
		if err != nil {
			return b, err
		}
		if p {
			b.Held = append(b.Held, lens)
		}
	}
	if b.Stale, err = l.StaleLenses(ctx); err != nil {
		return b, err
	}
	runs, err := l.LastRuns(ctx)
	if err != nil {
		return b, err
	}
	for _, r := range runs {
		b.LastRuns = append(b.LastRuns, fmt.Sprintf("%s %s %s", r.Lens, r.State, r.At.Format("2006-01-02")))
	}
	hb, err := l.Heartbeat(ctx)
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		b.MaintainStale = "never ran"
	case err != nil:
		return b, err
	case now.Sub(hb) > 2*MaintainPeriod:
		b.MaintainStale = "stale, last ran " + ageText(now.Sub(hb).Seconds()) + " ago"
	}
	return b, nil
}

func newHumanPendingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "List review items awaiting a ruling",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			decided, _ := cmd.Flags().GetBool("decided")
			cfg, err := config.Load(config.EnvFromOS(), assetsFS())
			if err != nil {
				return err
			}
			l, err := ledger.Open(ctx)
			if err != nil {
				return mapHumanError(err)
			}
			defer l.Close()
			out := cmd.OutOrStdout()
			jsonOut := false
			if f, err := OutputFormat(cmd); err != nil {
				return err
			} else {
				jsonOut = f == outputJSON
			}
			if decided {
				es, err := l.DecidedItems(ctx)
				if err != nil {
					return mapHumanError(err)
				}
				if jsonOut {
					return printJSONOrText(cmd, es, "")
				}
				for _, e := range es {
					line := fmt.Sprintf("%d  [%s]  %s %s  %s", e.Number, oneLine(e.Lens, 40), e.Ruling, e.Decided.Format("2006-01-02"), oneLine(e.Title, 120))
					if e.WorkBead != "" {
						line += "  bead: " + oneLine(e.WorkBead, 40)
					}
					fmt.Fprintln(out, line)
				}
				return nil
			}
			// Auto-defer first: an item nobody ruled on leaves the list.
			if _, err := l.AutoDefer(ctx, cfg.Limits.ItemsAutoDeferDays); err != nil {
				return mapHumanError(err)
			}
			items, err := l.PendingItems(ctx)
			if err != nil {
				return mapHumanError(err)
			}
			banner, err := buildBanner(ctx, l, items)
			if err != nil {
				return mapHumanError(err)
			}
			var rows []pendingItem
			for _, it := range items {
				rows = append(rows, pendingItem{it.Number, it.Lens, it.Writeup.Title, it.Writeup.Impact, it.Evidence})
			}
			if jsonOut {
				return printJSONOrText(cmd, map[string]any{"banner": banner, "items": rows}, "")
			}
			fmt.Fprintln(out, banner.line())
			for _, r := range rows {
				fmt.Fprintf(out, "%d  [%s]  %s  impact: %s\n", r.Number, oneLine(r.Lens, 40), oneLine(r.Title, 160), oneLine(r.Impact, 100))
			}
			return nil
		},
	}
	cmd.Flags().Bool("decided", false, "list decided items instead of pending ones")
	return cmd
}
