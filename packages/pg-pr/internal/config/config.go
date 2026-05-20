// Package config loads pg-pr configuration from $XDG_CONFIG_HOME/pg-pr/config.yaml
// (or $PG_PR_CONFIG if set). Phase 0 stub; YAML parsing lands in Phase 1.
package config

import (
	"context"
	"errors"
)

var ErrNotImplemented = errors.New("config: not implemented in this phase")

// Config is the parsed pg-pr configuration.
type Config struct {
	SelfLogin               string
	WorktreeRoot            string
	Repos                   []RepoConfig
	DaemonInterval          string
	CIOnlyAttemptsThreshold int
}

// RepoConfig is a single repo's configuration.
type RepoConfig struct {
	Path           string
	Remote         string
	VCS            string
	CICD           []string
	Issues         string
	Org            string
	TeamMembers    []string
	WatchLabels    []string
	PRBodyTemplate string
}

// Load reads and parses the config file. Phase 0 stub.
func Load(_ context.Context) (*Config, error) { return nil, ErrNotImplemented }
