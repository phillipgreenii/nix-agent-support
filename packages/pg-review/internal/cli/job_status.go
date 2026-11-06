package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

func init() { registerCommand(groupJob, newJobStatusCmd) }

// skippedWindow is how far back status counts Skipped runs.
const skippedWindow = 24 * time.Hour

// StatusLastRun is the newest run of one lens and scope.
type StatusLastRun struct {
	Lens  string `json:"lens"`
	Scope string `json:"scope"`
	State string `json:"state"`
	At    string `json:"at"`
	RunID int64  `json:"run_id"`
}

// Status is the `job status --json` document.
type Status struct {
	LastRuns       []StatusLastRun `json:"last_runs"`
	Pending        int             `json:"pending"`
	Waiting        int             `json:"waiting"`
	OutboxBacklog  int             `json:"outbox_backlog"`
	OutboxFailed   int             `json:"outbox_failed"`
	PausedLenses   []string        `json:"paused_lenses"`
	StaleLenses    []string        `json:"stale_lenses"`
	Scopes         []string        `json:"scopes"`
	SkippedRuns24h int             `json:"skipped_runs_24h"`
	Heartbeat      string          `json:"last_maintain_at,omitempty"`
	HeartbeatStale bool            `json:"maintain_stale"`
	HeartbeatNote  string          `json:"maintain,omitempty"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func buildStatus(cmd *cobra.Command, l *ledger.Ledger, cfg *config.Effective) (Status, error) {
	ctx := cmd.Context()
	var s Status
	runs, err := l.LastRunsByScope(ctx)
	if err != nil {
		return s, err
	}
	s.LastRuns = []StatusLastRun{}
	for _, r := range runs {
		s.LastRuns = append(s.LastRuns, StatusLastRun{r.Lens, r.Scope, r.State, r.At.UTC().Format(time.RFC3339), r.RunID})
	}
	c, err := l.Counts(ctx)
	if err != nil {
		return s, err
	}
	s.Pending, s.Waiting = c.Pending, c.Waiting
	due, err := l.DueBeadDecisions(ctx)
	if err != nil {
		return s, err
	}
	s.OutboxBacklog = len(due)
	if s.OutboxFailed, err = l.OutboxFailed(ctx); err != nil {
		return s, err
	}
	lenses, err := l.Lenses(ctx)
	if err != nil {
		return s, err
	}
	for _, lens := range lenses {
		p, err := l.IsLensPaused(ctx, lens)
		if err != nil {
			return s, err
		}
		if p {
			s.PausedLenses = append(s.PausedLenses, lens)
		}
	}
	s.PausedLenses = nonNil(s.PausedLenses)
	st, err := l.StaleLenses(ctx)
	if err != nil {
		return s, err
	}
	s.StaleLenses = nonNil(st)
	s.Scopes = []string{}
	for _, sc := range cfg.Scopes {
		s.Scopes = append(s.Scopes, sc.Name)
	}
	if s.SkippedRuns24h, err = l.SkippedSince(ctx, l.Now().Add(-skippedWindow)); err != nil {
		return s, err
	}
	hb, err := l.Heartbeat(ctx)
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		s.HeartbeatStale, s.HeartbeatNote = true, "never ran"
	case err != nil:
		return s, err
	default:
		s.Heartbeat = hb.UTC().Format(time.RFC3339)
		if age := l.Now().Sub(hb); age > 2*MaintainPeriod {
			s.HeartbeatStale, s.HeartbeatNote = true, "stale, last ran "+ageText(age.Seconds())+" ago"
		}
	}
	return s, nil
}

func none(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ",")
}

func (s Status) short() string {
	parts := []string{
		fmt.Sprintf("pending %d", s.Pending),
		fmt.Sprintf("outbox %d", s.OutboxBacklog),
		"paused: " + none(s.PausedLenses),
		"stale: " + none(s.StaleLenses),
	}
	if s.HeartbeatStale {
		parts = append(parts, "maintain: "+s.HeartbeatNote)
	}
	return strings.Join(parts, " | ")
}

func (s Status) text() string {
	var b strings.Builder
	b.WriteString("Last runs:\n")
	if len(s.LastRuns) == 0 {
		b.WriteString("  none\n")
	}
	for _, r := range s.LastRuns {
		fmt.Fprintf(&b, "  %s / %s: %s at %s (run %d)\n", r.Lens, r.Scope, r.State, r.At, r.RunID)
	}
	fmt.Fprintf(&b, "Pending: %d   Waiting: %d\n", s.Pending, s.Waiting)
	fmt.Fprintf(&b, "Outbox backlog: %d   failed: %d\n", s.OutboxBacklog, s.OutboxFailed)
	fmt.Fprintf(&b, "Paused lenses: %s\n", none(s.PausedLenses))
	fmt.Fprintf(&b, "Stale lenses: %s\n", none(s.StaleLenses))
	fmt.Fprintf(&b, "Scopes: %s\n", none(s.Scopes))
	fmt.Fprintf(&b, "Skipped runs (24h): %d\n", s.SkippedRuns24h)
	if s.HeartbeatNote != "" {
		fmt.Fprintf(&b, "Maintain: %s\n", s.HeartbeatNote)
	} else {
		fmt.Fprintf(&b, "Maintain: last ran %s\n", s.Heartbeat)
	}
	return strings.TrimRight(b.String(), "\n")
}

func newJobStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show run, queue and heartbeat status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(config.EnvFromOS(), assetsFS())
			if err != nil {
				return err
			}
			l, err := ledger.Open(cmd.Context())
			if err != nil {
				return ExitErrorf(exitcode.Ledger, "%v", err)
			}
			defer l.Close()
			s, err := buildStatus(cmd, l, cfg)
			if err != nil {
				return mapHumanError(err)
			}
			if short, _ := cmd.Flags().GetBool("short"); short {
				fmt.Fprintln(cmd.OutOrStdout(), s.short())
				return nil
			}
			if j, _ := cmd.Flags().GetBool("json"); j {
				return printStatusJSON(cmd, s)
			}
			return printJSONOrText(cmd, s, s.text())
		},
	}
	cmd.Flags().Bool("json", false, "print JSON")
	cmd.Flags().Bool("short", false, "print a one-line form for a shell prompt")
	return cmd
}

func printStatusJSON(cmd *cobra.Command, s Status) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return nil
}
