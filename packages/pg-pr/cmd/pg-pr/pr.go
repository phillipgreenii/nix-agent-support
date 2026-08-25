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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/gitlocal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs/github"
	"github.com/spf13/cobra"
)

// prFlags holds the parsed CLI flags for the `pg-pr pr` subcommands.
type prFlags struct {
	jsonOutput  bool
	repo        string
	base        string
	reviewers   bool // pr list: augment each PR with the live reviewer roster + labels
	forceReload bool // pr view: run a live SyncPR refresh before rendering (pg2-4dz88.6.4)
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

// loadConfigForRepoPath is overridable so tests can supply a synthetic
// config without writing a YAML file. Production wires it to config.Load.
var loadConfigForRepoPath = func(ctx context.Context) (*config.Config, error) {
	return config.Load(ctx)
}

// resolveRepoPath returns the absolute monorepo root path for the given
// owner/name repo identifier so callers can target the matching bd
// workspace. Resolution strategy:
//
//  1. Look up the repo in the loaded pg-pr config; use RepoConfig.Path
//     when set.
//  2. Otherwise, fall back to branch.Detect(cwd) — if cwd is inside any
//     git worktree, use its root.
//  3. If neither succeeds, return an empty path with no error. Callers
//     that require a valid bd workspace should treat empty as "use the
//     process cwd"; commands that must be deterministic should error.
//
// Errors from config loading are non-fatal: pg-pr is usable without a
// config file (e.g., one-shot `pr view` on a foreign repo).
func resolveRepoPath(ctx context.Context, repo string) string {
	if repo == "" {
		return ""
	}
	if cfg, err := loadConfigForRepoPath(ctx); err == nil && cfg != nil {
		for _, r := range cfg.Repos {
			if r.Remote == repo && r.Path != "" {
				return r.Path
			}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	info, err := branch.Detect(ctx, cwd, branch.Options{})
	if err != nil {
		return ""
	}
	// Only trust the detected worktree when it actually corresponds to the
	// repo we're operating on — otherwise we'd write to the wrong workspace.
	if info.Repo == repo && info.WorktreeRoot != "" {
		return info.WorktreeRoot
	}
	return ""
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

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
	for _, c := range []*cobra.Command{prFilesCmd, prCommitsCmd} {
		c.Flags().BoolVar(&prF.jsonOutput, "json", false,
			"Emit machine-readable JSON instead of human-readable output")
	}
	prFilesCmd.Flags().StringVar(&prF.base, "base", "origin/main",
		"Base reference for comparison")
	prCommitsCmd.Flags().StringVar(&prF.base, "base", "origin/main",
		"Base reference for comparison")

	prCmd.AddCommand(prFilesCmd, prCommitsCmd)
	rootCmd.AddCommand(prCmd)
}
