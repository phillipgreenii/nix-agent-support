package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
)

// configFlags holds the parsed CLI flags for `pg-pr config ...`.
type configFlags struct {
	jsonOutput bool
}

var cfFlags configFlags

// newConfigLoader is overridable so tests can inject a fake config.
var newConfigLoader = func(ctx context.Context) (*config.Config, error) {
	return config.Load(ctx)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect or validate pg-pr configuration",
	Long: `Admin commands for the resolved pg-pr config file.

The config is resolved in this order (highest priority first):
  1. $PG_PR_CONFIG (explicit override)
  2. $XDG_CONFIG_HOME/pg-pr/config.yaml
  3. ~/.config/pg-pr/config.yaml`,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the resolved config",
	Long: `Loads the config file (per the resolution order on the parent help)
and prints it after env / flag overrides. Human-readable by default; emit
JSON with --json or PGPR_OUTPUT=json.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := newConfigLoader(cmd.Context())
		if err != nil {
			return fmt.Errorf("config show: %w", err)
		}
		if output.Resolve(cfFlags.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), cfg)
		}
		return renderConfig(cmd.OutOrStdout(), cfg)
	},
}

var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Load + validate the config",
	Long: `Loads the config file and runs the full validation pass:
required fields, repo path existence (warning only), known provider
references, non-empty cicd lists, etc.

Exit code: 0 if valid (warnings ok), non-zero if any error.

Useful for nix activation scripts to fail fast on bad config.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := newConfigLoader(cmd.Context())
		if err != nil {
			return fmt.Errorf("config validate: %w", err)
		}
		report, err := cfg.Validate()
		if err != nil {
			return fmt.Errorf("config validate: %w", err)
		}
		if output.Resolve(cfFlags.jsonOutput) {
			if werr := writeJSON(cmd.OutOrStdout(), report); werr != nil {
				return werr
			}
		} else if rerr := renderReport(cmd.OutOrStdout(), cfg, report); rerr != nil {
			return rerr
		}
		if report.HasErrors() {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return fmt.Errorf("config invalid: %d error(s)", countErrors(report))
		}
		return nil
	},
}

// renderConfig prints the human-readable view of cfg: scalar fields as
// `key: value` lines, then a per-repo block.
func renderConfig(w io.Writer, cfg *config.Config) error {
	if cfg == nil {
		_, err := fmt.Fprintln(w, "(no config)")
		return err
	}
	if cfg.Path != "" {
		if _, err := fmt.Fprintf(w, "path: %s\n", cfg.Path); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w,
		"self_login: %s\nworktree_root: %s\n",
		cfg.SelfLogin, cfg.WorktreeRoot); err != nil {
		return err
	}
	if cfg.DaemonInterval != "" {
		if _, err := fmt.Fprintf(w, "daemon_interval: %s\n", cfg.DaemonInterval); err != nil {
			return err
		}
	}
	if cfg.CIOnlyAttemptsThreshold != 0 {
		if _, err := fmt.Fprintf(w, "ci_only_attempts_threshold: %d\n", cfg.CIOnlyAttemptsThreshold); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "repos: %d\n", len(cfg.Repos)); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "  REMOTE\tVCS\tISSUES\tCICD\tPATH"); err != nil {
		return err
	}
	for _, r := range cfg.Repos {
		cicd := "-"
		if len(r.CICD) > 0 {
			cicd = joinComma(r.CICD)
		}
		issues := r.Issues
		if issues == "" {
			issues = "-"
		}
		path := r.Path
		if path == "" {
			path = "-"
		}
		if _, err := fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			r.Remote, r.VCS, issues, cicd, path); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// renderReport prints validation issues as a tabular table; emits a
// single "ok" line when there are no issues.
func renderReport(w io.Writer, cfg *config.Config, report *config.ValidationReport) error {
	if cfg != nil && cfg.Path != "" {
		if _, err := fmt.Fprintf(w, "config: %s\n", cfg.Path); err != nil {
			return err
		}
	}
	if len(report.Issues) == 0 {
		_, err := fmt.Fprintln(w, "ok: no validation issues")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SEVERITY\tPATH\tMESSAGE"); err != nil {
		return err
	}
	for _, issue := range report.Issues {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", issue.Severity, issue.Path, issue.Message); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// countErrors returns the number of report issues with severity "error".
func countErrors(r *config.ValidationReport) int {
	var n int
	for _, i := range r.Issues {
		if i.Severity == "error" {
			n++
		}
	}
	return n
}

// joinComma joins ss with ", ". Local helper to keep this file self-contained.
func joinComma(ss []string) string {
	switch len(ss) {
	case 0:
		return ""
	case 1:
		return ss[0]
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += ", " + s
	}
	return out
}

func init() {
	configShowCmd.Flags().BoolVar(&cfFlags.jsonOutput, "json", false,
		"Emit machine-readable JSON (PGPR_OUTPUT=json env also honored)")
	configValidateCmd.Flags().BoolVar(&cfFlags.jsonOutput, "json", false,
		"Emit machine-readable JSON (PGPR_OUTPUT=json env also honored)")

	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configValidateCmd)
	rootCmd.AddCommand(configCmd)
}
