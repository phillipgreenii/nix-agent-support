package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/branch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/gitlocal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs/github"
	"github.com/spf13/cobra"
)

// prFlags holds the parsed CLI flags for the `pg-pr pr` subcommands.
type prFlags struct {
	jsonOutput bool
	repo       string
	base       string
}

var prF prFlags

// vcsProviderFor returns a VCS Provider for the given repo. The Phase 2
// surface only supports github; future phases can route by repo config.
// Exposed as a var so tests can substitute a fake provider.
var vcsProviderFor = func(_ string) vcs.Provider {
	return github.New()
}

// resolveRepo returns the repo identifier to use for an API call. Priority:
//  1. --repo flag.
//  2. branch.Detect against cwd.
func resolveRepo(ctx context.Context, flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get cwd: %w", err)
	}
	info, err := branch.Detect(ctx, cwd, branch.Options{})
	if err != nil {
		return "", fmt.Errorf("auto-detect repo: %w; pass --repo owner/name", err)
	}
	if info.Repo == "" {
		return "", fmt.Errorf("auto-detect repo: no GitHub remote found; pass --repo owner/name")
	}
	return info.Repo, nil
}

// ----------------------------------------------------------------------
// Cobra wiring
// ----------------------------------------------------------------------

var prCmd = &cobra.Command{
	Use:   "pr",
	Short: "Inspect pull requests",
	Long: `Read-only inspection of pull requests.

The repo is auto-detected from the current directory's git remote when
not specified via --repo. The base ref used by 'files' and 'commits'
defaults to origin/main.`,
}

var prShowCmd = &cobra.Command{
	Use:   "show <pr>",
	Short: "Show a PR's metadata",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prF.repo)
		if err != nil {
			return err
		}
		p := vcsProviderFor(repo)
		pr, err := p.GetPR(ctx, repo, num)
		if err != nil {
			return err
		}
		if output.Resolve(prF.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), pr)
		}
		return renderPR(cmd.OutOrStdout(), pr)
	},
}

var prInfoCmd = &cobra.Command{
	Use:     "info <pr>",
	Aliases: []string{"pr-info"},
	Short:   "Show full PR metadata (alias of show with extra fields)",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// In Phase 2, info is the same as show. Phase 3 will add labels /
		// reviewers / checks pulled from a richer API call.
		return prShowCmd.RunE(cmd, args)
	},
}

var prFilesCmd = &cobra.Command{
	Use:   "files",
	Short: "List changed files between base and HEAD",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		files, err := gitlocal.ChangedFiles(cmd.Context(), nil, cwd, prF.base)
		if err != nil {
			return err
		}
		if output.Resolve(prF.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), map[string]any{"files": files})
		}
		return renderFiles(cmd.OutOrStdout(), files)
	},
}

var prCommitsCmd = &cobra.Command{
	Use:   "commits",
	Short: "List commits between base and HEAD",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		commits, err := gitlocal.Commits(cmd.Context(), nil, cwd, prF.base)
		if err != nil {
			return err
		}
		if output.Resolve(prF.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), map[string]any{"commits": commits})
		}
		return renderCommits(cmd.OutOrStdout(), commits)
	},
}

func renderPR(w io.Writer, p *api.PR) error {
	state := p.State
	if p.Draft && state == "open" {
		state = "draft"
	}
	merged := "no"
	if p.Merged {
		merged = "yes"
	}
	_, err := fmt.Fprintf(w,
		"repo:   %s\nnumber: %d\nstate:  %s\nbranch: %s\nbase:   %s\nauthor: %s\nmerged: %s\nurl:    %s\n",
		p.Repo, p.Number, state, p.Branch, p.Base, p.Author, merged, p.URL)
	return err
}

func renderFiles(w io.Writer, files []gitlocal.FileChange) error {
	if len(files) == 0 {
		_, err := fmt.Fprintln(w, "(no changed files)")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ADD\tDEL\tPATH"); err != nil {
		return err
	}
	for _, f := range files {
		add := strconv.Itoa(f.Additions)
		del := strconv.Itoa(f.Deletions)
		if f.Binary {
			add, del = "-", "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", add, del, f.Path); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func renderCommits(w io.Writer, commits []gitlocal.Commit) error {
	if len(commits) == 0 {
		_, err := fmt.Fprintln(w, "(no commits)")
		return err
	}
	for _, c := range commits {
		short := c.SHA
		if len(short) > 12 {
			short = short[:12]
		}
		if _, err := fmt.Fprintf(w, "%s  %s\n", short, c.Subject); err != nil {
			return err
		}
		if c.Author != "" {
			if _, err := fmt.Fprintf(w, "    %s\n", c.Author); err != nil {
				return err
			}
		}
		if strings.TrimSpace(c.Body) != "" {
			for line := range strings.SplitSeq(strings.TrimSpace(c.Body), "\n") {
				if _, err := fmt.Fprintf(w, "    %s\n", line); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func init() {
	for _, c := range []*cobra.Command{prShowCmd, prInfoCmd, prFilesCmd, prCommitsCmd} {
		c.Flags().BoolVar(&prF.jsonOutput, "json", false,
			"Emit machine-readable JSON instead of human-readable output")
	}
	prShowCmd.Flags().StringVar(&prF.repo, "repo", "",
		"Repository in owner/name form (defaults to auto-detected remote)")
	prInfoCmd.Flags().StringVar(&prF.repo, "repo", "",
		"Repository in owner/name form (defaults to auto-detected remote)")
	prFilesCmd.Flags().StringVar(&prF.base, "base", "origin/main",
		"Base reference for comparison")
	prCommitsCmd.Flags().StringVar(&prF.base, "base", "origin/main",
		"Base reference for comparison")

	prCmd.AddCommand(prShowCmd, prInfoCmd, prFilesCmd, prCommitsCmd)
	rootCmd.AddCommand(prCmd)
}
