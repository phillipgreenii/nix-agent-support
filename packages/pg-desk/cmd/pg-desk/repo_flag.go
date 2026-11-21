package main

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// repoFlag is the global --repo OWNER/REPO flag: it makes the named
// configured repository the primary one for the invocation (docs/behavior/
// pg-desk/config.md, "Choosing a repository"). Empty means the configured
// primary (the first repos[] entry).
var repoFlag string

func init() {
	rootCmd.PersistentFlags().StringVar(&repoFlag, "repo", "",
		"OWNER/REPO of a configured repos[] entry to act on instead of the primary (first) one")
}

// selectRepo applies --repo to a loaded configuration. With the flag unset,
// or naming the primary, it returns cfg unchanged; an unconfigured name is an
// error naming the flag.
func selectRepo(cfg *config.Config, name string) (*config.Config, error) {
	if name == "" {
		return cfg, nil
	}
	sel, err := cfg.WithPrimary(name)
	if err != nil {
		return nil, &flagRepoError{err: err}
	}
	return sel, nil
}

type flagRepoError struct{ err error }

func (e *flagRepoError) Error() string { return "--repo: " + e.err.Error() }
func (e *flagRepoError) Unwrap() error { return e.err }

// loadConfigForRepo is the production configuration loader of every verb:
// config.Load plus the --repo selection.
func loadConfigForRepo(ctx context.Context) (*config.Config, error) {
	cfg, err := config.Load(ctx)
	if err != nil {
		return nil, err
	}
	return selectRepo(cfg, repoFlag)
}
