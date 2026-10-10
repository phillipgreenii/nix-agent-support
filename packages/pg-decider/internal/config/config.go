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
	"regexp"
	"sort"
	"strings"
)

// EnvVar names the environment variable holding the config file path.
const EnvVar = "PG_DECIDER_CONFIG"

const (
	// DefaultActor attributes the decider's writes when the file sets none.
	DefaultActor = "pg-decider"
	// DefaultEscalateAfter is K when escalate_after is absent.
	DefaultEscalateAfter = 3
)

// DefaultFocusPriority is the priority a focus bead gets for a source whose
// tracker priority is unmapped, and for a PR (which has none).
const DefaultFocusPriority = "P2"

// defaultFocusPriorityMap is focus_priority_map when the file sets none.
var defaultFocusPriorityMap = map[string]string{
	"Highest": "P0", "High": "P1", "Medium": "P2", "Low": "P3", "Lowest": "P4",
}

// validFocusPriorities are the values focus_priority_map may map to.
var validFocusPriorities = map[string]bool{"P0": true, "P1": true, "P2": true, "P3": true, "P4": true}

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
	// AreaLabels are rules deriving area labels for a PR's merge-request
	// anchor and its review-pr / process-feedback children from the PR title
	// or branch. Empty (the default) labels nothing. See AreaLabelRule.
	AreaLabels []AreaLabelRule `json:"area_labels,omitempty"`
	// BeadIDPattern is bead_id_pattern, the same name and meaning as pg-desk's
	// key: a regular expression telling a bead id from any other issue id (an
	// issue whose id matches is a bead, every other issue is not). Empty means
	// unset; a non-empty pattern must compile or Load fails. Read it through
	// BeadIDRegexp.
	BeadIDPattern string `json:"bead_id_pattern,omitempty"`
	// FocusBeadsQuery is focus_beads_query: the name of the pg-connector named
	// query that lists focus beads in every status, closed included, which the
	// focus rule's dedup lookup reads. Empty means unset.
	FocusBeadsQuery string `json:"focus_beads_query,omitempty"`
	// FocusPriorityMap is focus_priority_map: tracker priority name to P0..P4,
	// the priority of a focus bead minted for that source. When set it
	// replaces the default (Highest:P0, High:P1, Medium:P2, Low:P3, Lowest:P4)
	// whole; read it through FocusPriority.
	FocusPriorityMap map[string]string `json:"focus_priority_map,omitempty"`
}

// BeadIDRegexp returns the compiled bead_id_pattern, or (nil, nil) when it is
// unset. Load has already validated the pattern, so a Config that came from
// Load never returns an error here; a hand-built Config might. The pattern is
// unanchored: a deployment that needs an exact match supplies an anchored one.
func (c *Config) BeadIDRegexp() (*regexp.Regexp, error) {
	if c == nil || c.BeadIDPattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile(c.BeadIDPattern)
	if err != nil {
		return nil, fmt.Errorf("bead_id_pattern %q: %w", c.BeadIDPattern, err)
	}
	return re, nil
}

// FocusPriority is the priority (P0..P4) of a focus bead minted for a source
// whose tracker priority is name: the configured focus_priority_map (or the
// default map when none is set), and P2 for a name the map does not hold,
// including the empty name a PR source carries.
func (c *Config) FocusPriority(name string) string {
	m := defaultFocusPriorityMap
	if c != nil && c.FocusPriorityMap != nil {
		m = c.FocusPriorityMap
	}
	if p, ok := m[name]; ok {
		return p
	}
	return DefaultFocusPriority
}

func validateFocusKeys(c *Config) error {
	if c.BeadIDPattern != "" {
		if strings.TrimSpace(c.BeadIDPattern) == "" {
			return fmt.Errorf("bead_id_pattern %q must not be blank", c.BeadIDPattern)
		}
		if _, err := c.BeadIDRegexp(); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(c.FocusPriorityMap))
	for n := range c.FocusPriorityMap {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if strings.TrimSpace(n) == "" {
			return fmt.Errorf("focus_priority_map holds a blank priority name")
		}
		if v := c.FocusPriorityMap[n]; !validFocusPriorities[v] {
			return fmt.Errorf("focus_priority_map[%q] is %q; it must be one of P0, P1, P2, P3, P4", n, v)
		}
	}
	return nil
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
	if err := validateAreaLabels(c.AreaLabels); err != nil {
		return nil, fmt.Errorf("config: %q: %w", path, err)
	}
	if err := validateFocusKeys(&c); err != nil {
		return nil, fmt.Errorf("config: %q: %w", path, err)
	}
	return &c, nil
}
