package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/branch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/plugin/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues/githubissues"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues/jira"
)

// issueFlags holds the parsed CLI flags for `pg-pr issue ...`.
type issueFlags struct {
	provider   string
	jsonOutput bool
}

var isFlags issueFlags

// newIssueLoader is overridable so tests can inject a fake config without
// touching $PG_PR_CONFIG / $XDG_CONFIG_HOME.
var newIssueLoader = func(ctx context.Context) (*config.Config, error) {
	return config.Load(ctx)
}

// newIssueProvider is overridable so tests can substitute a fake
// issues.Provider for a given name. Production resolves builtins inline and
// exec:<binary> via scriptout.
var newIssueProvider = func(name string) (issues.Provider, error) {
	switch {
	case name == "jira":
		return jira.New(), nil
	case name == "github-issues":
		return githubissues.New(), nil
	case strings.HasPrefix(name, "exec:"):
		bin := strings.TrimPrefix(name, "exec:")
		if bin == "" {
			return nil, errors.New("issue: empty exec binary name")
		}
		return scriptout.NewExecIssuesProvider(bin), nil
	default:
		return nil, fmt.Errorf("issue: unknown provider %q", name)
	}
}

// newIssueBranchDetect is overridable so tests can skip cwd-based detection.
var newIssueBranchDetect = func(ctx context.Context) (*api.BranchInfo, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return branch.Detect(ctx, cwd, branch.Options{})
}

var issueCmd = &cobra.Command{
	Use:   "issue",
	Short: "Inspect external issue-tracker tickets",
	Long: `Read tickets from the configured issues provider (jira or
github-issues). Auto-detects which provider to use from the current
repo's config; --provider overrides.`,
}

var issueShowCmd = &cobra.Command{
	Use:   "show <ticket-id>",
	Short: "Show a single ticket from the configured issues provider",
	Long: `Resolves the issues provider in this order:

  1. --provider flag (e.g. "jira", "github-issues", "exec:my-binary").
  2. The current repo's "issues:" config (from branch.Detect on cwd).
  3. Error: no issues provider configured.

Output is human-readable by default; JSON via --json or PGPR_OUTPUT=json.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ticket := strings.TrimSpace(args[0])
		if ticket == "" {
			return errors.New("issue show: ticket id is required")
		}
		name, err := resolveIssueProvider(cmd.Context(), isFlags.provider)
		if err != nil {
			return err
		}
		prov, err := newIssueProvider(name)
		if err != nil {
			return err
		}
		issue, err := prov.GetIssue(cmd.Context(), ticket)
		if err != nil {
			return fmt.Errorf("issue show %s: %w", ticket, err)
		}
		if output.Resolve(isFlags.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), issue)
		}
		return renderIssue(cmd.OutOrStdout(), name, issue)
	},
}

// resolveIssueProvider returns the provider name to use for the current
// invocation. The explicit --provider flag wins; otherwise we look at the
// current repo's config via branch.Detect.
func resolveIssueProvider(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	info, err := newIssueBranchDetect(ctx)
	if err != nil {
		return "", fmt.Errorf("issue: detect current repo: %w", err)
	}
	cfg, err := newIssueLoader(ctx)
	if err != nil {
		return "", fmt.Errorf("issue: load config: %w", err)
	}
	for _, r := range cfg.Repos {
		if r.Remote == info.Repo {
			if r.Issues == "" {
				return "", fmt.Errorf("issue: repo %q has no issues provider configured (set --provider or add 'issues:' to repos[].config)", info.Repo)
			}
			return r.Issues, nil
		}
	}
	return "", fmt.Errorf("issue: current repo %q is not in the pg-pr config; pass --provider", info.Repo)
}

// renderIssue prints the human-readable view: one key/value per line.
func renderIssue(w io.Writer, providerName string, issue *api.Issue) error {
	if issue == nil {
		_, err := fmt.Fprintln(w, "(no issue returned)")
		return err
	}
	_, err := fmt.Fprintf(w,
		"provider: %s\nid: %s\ntitle: %s\nstate: %s\nurl: %s\n",
		providerName, issue.ID, issue.Title, issue.State, issue.URL)
	return err
}

func init() {
	issueShowCmd.Flags().StringVar(&isFlags.provider, "provider", "",
		"Override the issues provider (jira, github-issues, exec:<binary>)")
	issueShowCmd.Flags().BoolVar(&isFlags.jsonOutput, "json", false,
		"Emit machine-readable JSON (PGPR_OUTPUT=json env also honored)")

	issueCmd.AddCommand(issueShowCmd)
	rootCmd.AddCommand(issueCmd)
}
