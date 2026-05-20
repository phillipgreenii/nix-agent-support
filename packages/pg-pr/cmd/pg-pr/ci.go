// CI subcommands: runs / logs / rerun-failed. The CICD providers are
// loaded from pg-pr config (one repo can have multiple providers; results
// are merged). Builtin provider is `github-actions`; `exec:*` providers
// surface a clear "not yet wired" error in this phase.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/cicd"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/cicd/ghactions"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs/github"
	"github.com/spf13/cobra"
)

// ciFlags holds the parsed CLI flags for the `pg-pr ci` subcommands.
type ciFlags struct {
	jsonOutput bool
	repo       string
}

var ciF ciFlags

// cicdProvidersForRepo resolves the configured cicd providers for a repo.
// Returns a list of (name, Provider) tuples in the configured order.
// `exec:<name>` providers are not yet wired; we surface a clear error if
// any are referenced.
//
// Production-only by default; tests override the var directly.
var cicdProvidersForRepo = func(ctx context.Context, repo string) ([]ciNamedProvider, error) {
	cfg, err := config.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	for i := range cfg.Repos {
		if cfg.Repos[i].Remote == repo {
			return buildCICDProviders(cfg.Repos[i].CICD)
		}
	}
	return nil, fmt.Errorf("repo %s is not configured in pg-pr config; add a repos: entry with a cicd: list", repo)
}

// ciNamedProvider pairs a CICD provider with its config name.
type ciNamedProvider struct {
	Name     string
	Provider cicd.Provider
}

// buildCICDProviders instantiates each configured cicd provider name.
func buildCICDProviders(names []string) ([]ciNamedProvider, error) {
	if len(names) == 0 {
		return nil, errors.New("no cicd providers configured for repo (set `cicd:` in repo config)")
	}
	out := make([]ciNamedProvider, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		switch {
		case n == ghactions.ProviderName || n == "ghactions" || n == "github_actions":
			p := ghactions.New()
			// Wire the github VCS as PRResolver so ListRuns can resolve
			// PR # -> head branch.
			p.SetPRResolver(githubPRResolver{})
			out = append(out, ciNamedProvider{Name: ghactions.ProviderName, Provider: p})
		case strings.HasPrefix(n, "exec:"):
			return nil, fmt.Errorf("cicd provider %q: exec:* providers are not yet wired in this phase (script-out protocol lands in a follow-up); use the builtin %s", n, ghactions.ProviderName)
		default:
			return nil, fmt.Errorf("unknown cicd provider %q (builtins: %s; exec:* lands later)", n, ghactions.ProviderName)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no cicd providers resolved")
	}
	return out, nil
}

// githubPRResolver adapts the github VCS provider's GetPR to the
// ghactions.PRResolver interface so ListRuns can find the head branch.
type githubPRResolver struct{}

func (githubPRResolver) PRHeadBranch(ctx context.Context, repo string, number int) (string, error) {
	pr, err := github.New().GetPR(ctx, repo, number)
	if err != nil {
		return "", err
	}
	return pr.Branch, nil
}

// ----------------------------------------------------------------------
// Cobra wiring
// ----------------------------------------------------------------------

var ciCmd = &cobra.Command{
	Use:   "ci",
	Short: "Inspect CI/CD runs for PRs",
	Long: `Query the CI/CD providers configured for the PR's repo (per the
pg-pr config). Repos with multiple cicd entries fan out and results merge.

The builtin provider is github-actions. exec:* providers will be wired
via the script-out protocol in a follow-up.`,
}

var ciRunsCmd = &cobra.Command{
	Use:   "runs <pr>",
	Short: "List CI runs for a PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, ciF.repo)
		if err != nil {
			return err
		}
		providers, err := cicdProvidersForRepo(ctx, repo)
		if err != nil {
			return err
		}

		all := make([]api.CIRun, 0)
		var listErrs []string
		for _, p := range providers {
			runs, lerr := p.Provider.ListRuns(ctx, repo, num)
			if lerr != nil {
				listErrs = append(listErrs, fmt.Sprintf("%s: %v", p.Name, lerr))
				continue
			}
			all = append(all, runs...)
		}
		if ciF.jsonOutput {
			payload := map[string]any{"runs": all}
			if len(listErrs) > 0 {
				payload["errors"] = listErrs
			}
			return writeJSON(cmd.OutOrStdout(), payload)
		}
		if err := renderCIRuns(cmd.OutOrStdout(), all); err != nil {
			return err
		}
		for _, e := range listErrs {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: %s\n", e)
		}
		// Return error only if every provider failed and there are no runs.
		if len(listErrs) > 0 && len(all) == 0 {
			return fmt.Errorf("all cicd providers failed: %s", strings.Join(listErrs, "; "))
		}
		return nil
	},
}

var ciLogsCmd = &cobra.Command{
	Use:   "logs <run-id>",
	Short: "Fetch logs for a CI run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, ciF.repo)
		if err != nil {
			return err
		}
		providers, err := cicdProvidersForRepo(ctx, repo)
		if err != nil {
			return err
		}
		// Logs are fetched by run-id which is provider-scoped. The user
		// passes a run-id; we try each provider until one succeeds. In
		// practice repos typically configure a single cicd provider, so
		// this works fine even though it's not strictly cross-provider.
		var lastErr error
		for _, p := range providers {
			raw, lerr := p.Provider.GetLogs(ctx, args[0])
			if lerr != nil {
				lastErr = lerr
				continue
			}
			_, err := cmd.OutOrStdout().Write(raw)
			return err
		}
		if lastErr != nil {
			return fmt.Errorf("get logs: %w", lastErr)
		}
		return errors.New("no cicd provider could fetch logs")
	},
}

var ciRerunFailedCmd = &cobra.Command{
	Use:   "rerun-failed <pr>",
	Short: "Re-run the most recent failed CI run for a PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, ciF.repo)
		if err != nil {
			return err
		}
		providers, err := cicdProvidersForRepo(ctx, repo)
		if err != nil {
			return err
		}
		var rerunErrs []string
		anyOK := false
		for _, p := range providers {
			if rerr := p.Provider.RerunFailed(ctx, repo, num); rerr != nil {
				rerunErrs = append(rerunErrs, fmt.Sprintf("%s: %v", p.Name, rerr))
				continue
			}
			anyOK = true
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"ok Triggered rerun-failed via %s for %s#%d\n", p.Name, repo, num)
		}
		if !anyOK {
			return fmt.Errorf("rerun-failed: all providers failed: %s",
				strings.Join(rerunErrs, "; "))
		}
		for _, e := range rerunErrs {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: %s\n", e)
		}
		return nil
	},
}

// renderCIRuns prints a human-readable table of CI runs.
func renderCIRuns(w io.Writer, runs []api.CIRun) error {
	if len(runs) == 0 {
		_, err := fmt.Fprintln(w, "(no CI runs)")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PROVIDER\tNAME\tSTATUS\tCONCLUSION\tID\tURL"); err != nil {
		return err
	}
	for _, r := range runs {
		concl := r.Conclusion
		if concl == "" {
			concl = "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Provider, r.Name, r.Status, concl, r.ID, r.URL); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// resetCIFlags clears mutable state between cobra tests.
func resetCIFlags() {
	ciF = ciFlags{}
}

func init() {
	for _, c := range []*cobra.Command{ciRunsCmd, ciLogsCmd, ciRerunFailedCmd} {
		c.Flags().StringVar(&ciF.repo, "repo", "",
			"Repository in owner/name form (defaults to auto-detected remote)")
	}
	ciRunsCmd.Flags().BoolVar(&ciF.jsonOutput, "json", false,
		"Emit machine-readable JSON instead of human-readable output")

	ciCmd.AddCommand(ciRunsCmd, ciLogsCmd, ciRerunFailedCmd)
	rootCmd.AddCommand(ciCmd)
}
