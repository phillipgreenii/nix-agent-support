package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/auth"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
)

// authFlags holds the parsed CLI flags for `pg-pr auth status`.
type authFlags struct {
	jsonOutput bool
}

var auFlags authFlags

// newAuthLoader is overridable so tests can inject a fake config without
// touching $PG_PR_CONFIG / $XDG_CONFIG_HOME.
var newAuthLoader = func(ctx context.Context) (*config.Config, error) {
	return config.Load(ctx)
}

// newAuthChecker is overridable so tests can inject deterministic runners
// for `gh`, HTTP, and exec providers.
var newAuthChecker = func(ctx context.Context, cfg *config.Config) ([]auth.Status, error) {
	return auth.CheckAll(ctx, cfg)
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Inspect provider authentication state",
	Long:  `Admin commands for diagnosing pg-pr provider authentication.`,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report auth state per configured provider",
	Long: `Iterates over every provider referenced in the loaded config
(VCS, CICD list, Issues) and reports OK / MISSING / EXPIRED /
INSUFFICIENT_SCOPES per provider.

Builtins (github, github-actions, github-issues, jira) are checked inline.
exec:<binary> providers are invoked via the scriptout 'auth_status' op.

Exit code is non-zero when any provider is not OK so this command can be
used in pre-commit hooks / CI gates.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		cfg, err := newAuthLoader(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		statuses, err := newAuthChecker(ctx, cfg)
		if err != nil {
			return err
		}
		if output.Resolve(auFlags.jsonOutput) {
			if err := writeJSON(cmd.OutOrStdout(), statuses); err != nil {
				return err
			}
		} else if err := renderAuthTable(cmd.OutOrStdout(), statuses); err != nil {
			return err
		}
		// Non-zero exit when any provider failed. We use SilenceUsage so
		// cobra doesn't print usage on this error type — the table itself
		// is the diagnostic.
		if failing := countFailing(statuses); failing > 0 {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return fmt.Errorf("%d of %d provider(s) failed auth check", failing, len(statuses))
		}
		return nil
	},
}

// renderAuthTable prints the human-readable view: one provider per line in
// a column-aligned table.
func renderAuthTable(w io.Writer, statuses []auth.Status) error {
	if len(statuses) == 0 {
		_, err := fmt.Fprintln(w, "No providers configured.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PROVIDER\tSTATE\tDETAIL"); err != nil {
		return err
	}
	for _, s := range statuses {
		detail := s.Detail
		if detail == "" {
			detail = "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Provider, s.State, detail); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// countFailing returns the number of statuses whose State is not OK.
func countFailing(statuses []auth.Status) int {
	var n int
	for _, s := range statuses {
		if !s.IsOK() {
			n++
		}
	}
	return n
}

// ErrAuthFailed is unused at runtime but kept so external callers (or
// future code) have a stable sentinel to errors.Is against.
var ErrAuthFailed = errors.New("auth: one or more providers failed")

func init() {
	authStatusCmd.Flags().BoolVar(&auFlags.jsonOutput, "json", false,
		"Emit machine-readable JSON (PGPR_OUTPUT=json env also honored)")

	authCmd.AddCommand(authStatusCmd)
	rootCmd.AddCommand(authCmd)
}
