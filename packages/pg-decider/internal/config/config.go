// Package config loads pg-decider's own configuration (entity-change-flow
// design 9.10; packet pg2-2j5ac.52.18.9).
//
// # Location and format
//
// The configuration is ONE JSON file named by the environment variable
// PG_DECIDER_CONFIG (JSON so this module adds no third-party dependency; the
// deployment phase renders the file). An unset or empty variable yields the
// zero Config; a variable naming a missing or unparseable file is an error.
// Unknown keys are tolerated so a later packet can add one.
//
// # sync: settings copied from pg-desk
//
// pg-desk's sync: block holds two settings: sync.mode and sync.retry.
// Neither is copied. sync.mode is replaced by the plan and apply
// subcommands, and the sync.retry backoff is replaced by the
// K-consecutive-failures escalation (escalate_after). No other sync: rule
// setting is read by the logic the ported rules replace (every cfg. read in
// packages/pg-desk/internal/sync is AgentTrackerBackend, Repos[0].BeadsDir,
// Sync.Mode and Sync.Retry), so NONE were needed. The two non-sync keys those
// writes do consume are carried here: agent_tracker_backend and beads_dir.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// EnvVar names the environment variable holding the config file path.
const EnvVar = "PG_DECIDER_CONFIG"

const (
	// DefaultActor attributes the decider's writes when the file sets none.
	DefaultActor = "pg-decider"
	// DefaultEscalateAfter is K when escalate_after is absent.
	DefaultEscalateAfter = 3
)

// Config is the decider's configuration.
type Config struct {
	// AgentTrackerBackend selects the tracker backend passed as --backend to
	// every pg-connector issue exec.
	AgentTrackerBackend string `json:"agent_tracker_backend,omitempty"`
	// BeadsDir is exported as PG_CONNECTOR_ISSUE_BEADS_DIR on every issue exec
	// (the same meaning as pg-desk's first configured repo's beads_dir).
	BeadsDir string `json:"beads_dir,omitempty"`
	// Actor is the identity attributed to the decider's writes; read it
	// through ActorOrDefault.
	Actor string `json:"actor,omitempty"`
	// EscalateAfter is the consecutive-failure threshold; nil means 3 and an
	// explicit value below 1 makes Load fail. Read it through K.
	EscalateAfter *int `json:"escalate_after,omitempty"`
}

// K is the effective escalation threshold.
func (c *Config) K() int {
	if c == nil || c.EscalateAfter == nil {
		return DefaultEscalateAfter
	}
	return *c.EscalateAfter
}

// ActorOrDefault is the actor attributed to the decider's writes: the
// configured actor, or "pg-decider" when none is set (pg-desk's annotate verb
// requires one).
func (c *Config) ActorOrDefault() string {
	if c == nil || c.Actor == "" {
		return DefaultActor
	}
	return c.Actor
}

// Load reads the JSON file named by PG_DECIDER_CONFIG.
func Load() (*Config, error) {
	path := os.Getenv(EnvVar)
	if path == "" {
		return &Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s=%q: %w", EnvVar, path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	if c.EscalateAfter != nil && *c.EscalateAfter < 1 {
		return nil, fmt.Errorf("config: escalate_after in %q is %d; it must be at least 1", path, *c.EscalateAfter)
	}
	return &c, nil
}
