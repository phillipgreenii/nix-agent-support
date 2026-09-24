// Package config loads pg-desk's configuration from a YAML file.
//
// Resolution order (highest priority first), mirroring
// packages/pg-connector's own ($PG_PR_CONFIG -> XDG -> ~/.config) and
// packages/pg-pr's internal/config before it:
//
//  1. $PG_DESK_CONFIG (explicit override; missing file is an error).
//  2. $XDG_CONFIG_HOME/pg-desk/config.yaml.
//  3. ~/.config/pg-desk/config.yaml.
//
// Config covers every key in the docket design's section 7.8 table
// (docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md
// lines 997-1011): self_login, team_members, watch_labels, repos[]
// (remote, beads_dir), ticket_patterns, agents[] (login, approval_regex,
// policy), approver_allowlist, verdict_generations, check_interpreters,
// ci_only_attempts_threshold, jira (high_priority_values, incident_labels,
// incident_issue_types), category_vocabulary, urgency (labels, keywords,
// thresholds), agent_tracker_backend, actor, sync.mode, heartbeat_period,
// stale_after, serve.addr, serve.log, and open.chrome_bin — all 21 keys are
// typed here now, most consumed by this docket's later packets;
// agent_tracker_backend starts Phase 10, sync.mode/jira.*/ticket_patterns
// start Phase 10/13 respectively (present but unused by this phase's own
// code).
//
// Several fields that lived nested under pg-pr's repos[] entries
// (team_members, watch_labels, ticket_patterns, check_interpreters) are
// flattened to top-level here: Phase 9 supports exactly one repository (see
// docs/behavior/pg-desk/README.md's "Scope" section; multi-repo stays under
// pg2-ynhr.7), so there is no longer a per-repo home for them to nest
// under.
package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNoConfig is returned by Load when no config file is found.
var ErrNoConfig = errors.New("config: no config file found")

// Config is pg-desk's parsed configuration. JSON tags mirror the YAML names
// so a future `pg-desk status`/`doctor` JSON surface sees the same
// snake_case keys as the on-disk file.
type Config struct {
	// Path is the absolute path the config was loaded from. Populated by Load.
	Path string `yaml:"-" json:"path,omitempty"`

	SelfLogin   string   `yaml:"self_login" json:"self_login"`
	TeamMembers []string `yaml:"team_members,omitempty" json:"team_members,omitempty"`
	WatchLabels []string `yaml:"watch_labels,omitempty" json:"watch_labels,omitempty"`

	// Repos is the configured repository list. Phase 9 supports exactly one
	// entry (docs/behavior/pg-desk/README.md); a second entry is not
	// rejected here (that validation belongs to the gather/interpret
	// packets that actually resolve against "the one configured
	// repository"), but nothing in this phase reads past Repos[0].
	Repos []RepoConfig `yaml:"repos" json:"repos"`

	// TicketPatterns is unconsumed until the cross-reference step (Phase 13).
	TicketPatterns []string `yaml:"ticket_patterns,omitempty" json:"ticket_patterns,omitempty"`

	Agents []AgentConfig `yaml:"agents,omitempty" json:"agents,omitempty"`

	ApproverAllowlist  []string            `yaml:"approver_allowlist,omitempty" json:"approver_allowlist,omitempty"`
	VerdictGenerations []VerdictGeneration `yaml:"verdict_generations,omitempty" json:"verdict_generations,omitempty"`

	CheckInterpreters       []CheckInterpreterConfig `yaml:"check_interpreters,omitempty" json:"check_interpreters,omitempty"`
	CIOnlyAttemptsThreshold int                      `yaml:"ci_only_attempts_threshold,omitempty" json:"ci_only_attempts_threshold,omitempty"`

	// Jira enables the layered Jira priority/incident urgency signal.
	// Unconsumed until Phase 13 (present but unused by this phase's own
	// code). No org-specific Jira URLs, project keys, or instance names
	// appear here — all deployment-specific detail is supplied via config.
	Jira *JiraConfig `yaml:"jira,omitempty" json:"jira,omitempty"`

	// CategoryVocabulary maps a category name to the keyword patterns that
	// classify a PR/issue into it (provenance: df-categorize). Consumed by
	// the interpret stage (this docket's packet 5), not by this packet's
	// own code.
	CategoryVocabulary map[string][]string `yaml:"category_vocabulary,omitempty" json:"category_vocabulary,omitempty"`
	// Urgency configures base urgency scoring (labels, keywords, checks
	// rollup, bugfix commits — provenance: internal/enrich). Layered
	// urgency (project health, Jira priority) is Phase 13; this phase's
	// interpret stage (packet 5) runs base urgency only.
	Urgency *UrgencyConfig `yaml:"urgency,omitempty" json:"urgency,omitempty"`

	// AgentTrackerBackend selects which agent tracker pg-desk signals
	// through for every pg-connector issue write and feedback-set
	// attribution. A config key, never a literal, so the beads backend
	// stays as generic as every other pg-connector consumer. Consumed
	// starting Phase 10; present but unused by this phase's own code.
	AgentTrackerBackend string `yaml:"agent_tracker_backend,omitempty" json:"agent_tracker_backend,omitempty"`
	// Actor is the identity pg-desk attributes its own writes to (pg-pr
	// used a hardcoded "pg-pr daemon"; pg-desk takes it from config
	// instead).
	Actor string `yaml:"actor,omitempty" json:"actor,omitempty"`

	// Sync is unconsumed until Phase 10 — Mode is not yet a real
	// configuration key at all this phase (present but unused by this
	// phase's own code).
	Sync SyncConfig `yaml:"sync,omitempty" json:"sync,omitempty"`

	HeartbeatPeriod string `yaml:"heartbeat_period,omitempty" json:"heartbeat_period,omitempty"`
	StaleAfter      string `yaml:"stale_after,omitempty" json:"stale_after,omitempty"`

	Serve ServeConfig `yaml:"serve,omitempty" json:"serve,omitempty"`
	Open  OpenConfig  `yaml:"open,omitempty" json:"open,omitempty"`
}

// RepoConfig is a single configured repository. Phase 9 supports exactly
// one (docs/behavior/pg-desk/README.md's "Scope" section).
type RepoConfig struct {
	Remote string `yaml:"remote" json:"remote"`
	// BeadsDir names which beads workspace gather and sync target for this
	// repo (provenance: pg-pr's repos[].path).
	BeadsDir string `yaml:"beads_dir,omitempty" json:"beads_dir,omitempty"`
}

// AgentConfig declares one registered agent identity for approvals
// classification and bot-verdict parsing (provenance: pg-pr's agents and
// its agent registry policy block).
type AgentConfig struct {
	Login         string `yaml:"login" json:"login"`
	ApprovalRegex string `yaml:"approval_regex,omitempty" json:"approval_regex,omitempty"`
	Policy        string `yaml:"policy,omitempty" json:"policy,omitempty"`
}

// VerdictGeneration describes one generation of the review-comment verdict
// grammar. Shape mirrors packages/pg-pr/internal/config's field of the same
// name; consumed by internal/interpret's computeApprovals (via
// buildVerdictClassifier, converting to []verdict.Generation and compiling
// with internal/verdict.New — that package, ported from
// packages/pg-pr/internal/verdict, is the real grammar parser).
type VerdictGeneration struct {
	ID                string   `yaml:"id" json:"id"`
	BodyMarker        string   `yaml:"body_marker" json:"body_marker"`
	FindingsPatterns  []string `yaml:"findings_patterns,omitempty" json:"findings_patterns,omitempty"`
	AuthorityPatterns []string `yaml:"authority_patterns,omitempty" json:"authority_patterns,omitempty"`
}

// CheckInterpreterConfig declares one entry in the check/status interpreter
// registry (provenance: pg-pr's repos[].check_interpreters, now top-level
// per Phase 9's single-repo flattening). Shape mirrors
// packages/pg-pr/internal/config's field of the same name; unconsumed by
// this packet's own code.
type CheckInterpreterConfig struct {
	Patterns []string `yaml:"patterns,omitempty" json:"patterns,omitempty"`
	Type     string   `yaml:"type" json:"type"`
}

// JiraConfig configures the layered Jira priority/incident urgency signal.
// Unconsumed until Phase 13.
type JiraConfig struct {
	HighPriorityValues []string `yaml:"high_priority_values,omitempty" json:"high_priority_values,omitempty"`
	IncidentLabels     []string `yaml:"incident_labels,omitempty" json:"incident_labels,omitempty"`
	IncidentIssueTypes []string `yaml:"incident_issue_types,omitempty" json:"incident_issue_types,omitempty"`
}

// UrgencyConfig configures urgency scoring. Thresholds maps an urgency
// level name to its numeric cutoff.
type UrgencyConfig struct {
	Labels     []string       `yaml:"labels,omitempty" json:"labels,omitempty"`
	Keywords   []string       `yaml:"keywords,omitempty" json:"keywords,omitempty"`
	Thresholds map[string]int `yaml:"thresholds,omitempty" json:"thresholds,omitempty"`
}

// SyncConfig configures the sync stage (Phase 10). Mode is one of "off",
// "plan", or "apply" once that phase lands; unconsumed by this packet's own
// code.
type SyncConfig struct {
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// ServeConfig configures `pg-desk serve`.
type ServeConfig struct {
	Addr string `yaml:"addr,omitempty" json:"addr,omitempty"`
	Log  string `yaml:"log,omitempty" json:"log,omitempty"`
}

// OpenConfig configures `pg-desk open`. ChromeBin is the ONE
// operator-configured browser binary the composition rule (D10) permits
// pg-desk to exec besides pg-connector — always read from here, never a Go
// string literal (see cmd/pg-desk/composition_test.go).
type OpenConfig struct {
	ChromeBin string `yaml:"chrome_bin,omitempty" json:"chrome_bin,omitempty"`
}

// Load reads and parses the config file using the resolution order
// described in the package doc. If no config file is found and no explicit
// $PG_DESK_CONFIG override is set, Load returns ErrNoConfig wrapped with a
// helpful path string.
func Load(_ context.Context) (*Config, error) {
	return LoadFromEnv(envProcess{})
}

// LoadFile loads from an explicit path. Useful in tests; production code
// should use Load.
func LoadFile(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("config: empty path")
	}
	expanded, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(expanded)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", expanded, err)
	}
	cfg, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", expanded, err)
	}
	cfg.Path = expanded
	if err := finalize(cfg); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", expanded, err)
	}
	return cfg, nil
}

// envSource is the minimal interface Load needs to look up env + home dir.
// Exposed so tests can inject a fixed environment without monkey-patching.
type envSource interface {
	Getenv(string) string
	UserHomeDir() (string, error)
}

type envProcess struct{}

func (envProcess) Getenv(k string) string       { return os.Getenv(k) }
func (envProcess) UserHomeDir() (string, error) { return os.UserHomeDir() }

// LoadFromEnv is the env-injectable variant of Load. Public for tests.
func LoadFromEnv(env envSource) (*Config, error) {
	if explicit := env.Getenv("PG_DESK_CONFIG"); explicit != "" {
		cfg, err := LoadFile(explicit)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("config: $PG_DESK_CONFIG=%s does not exist", explicit)
			}
			return nil, err
		}
		return cfg, nil
	}

	candidates := defaultCandidates(env)
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return LoadFile(p)
		}
	}

	return nil, fmt.Errorf("%w: looked in %s; create one or set $PG_DESK_CONFIG",
		ErrNoConfig, strings.Join(candidates, ", "))
}

// defaultCandidates returns the list of paths Load checks in order.
func defaultCandidates(env envSource) []string {
	var out []string
	if xdg := env.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		out = append(out, filepath.Join(xdg, "pg-desk", "config.yaml"))
	}
	if home, err := env.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".config", "pg-desk", "config.yaml"))
	}
	return out
}

// parse decodes the YAML bytes into a Config without finalization.
func parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(false)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// finalize validates the required fields and expands ~ in path-like
// fields. Deliberately minimal for this packet: self_login and at least
// one repo with a non-empty remote are the only invariants a config-driven,
// single-repo skeleton needs enforced now. Richer validation (matching
// pg-pr's `config validate` report shape) is this docket's own later
// concern, not this packet's.
func finalize(cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if strings.TrimSpace(cfg.SelfLogin) == "" {
		return errors.New("self_login is required")
	}
	if len(cfg.Repos) == 0 {
		return errors.New("repos: at least one repo is required")
	}
	for i := range cfg.Repos {
		if strings.TrimSpace(cfg.Repos[i].Remote) == "" {
			return fmt.Errorf("repos[%d]: remote is required", i)
		}
		if cfg.Repos[i].BeadsDir != "" {
			expanded, err := expandHome(cfg.Repos[i].BeadsDir)
			if err != nil {
				return fmt.Errorf("repos[%d].beads_dir: %w", i, err)
			}
			cfg.Repos[i].BeadsDir = expanded
		}
	}
	if cfg.Serve.Log != "" {
		expanded, err := expandHome(cfg.Serve.Log)
		if err != nil {
			return fmt.Errorf("serve.log: %w", err)
		}
		cfg.Serve.Log = expanded
	}
	if cfg.Open.ChromeBin != "" {
		expanded, err := expandHome(cfg.Open.ChromeBin)
		if err != nil {
			return fmt.Errorf("open.chrome_bin: %w", err)
		}
		cfg.Open.ChromeBin = expanded
	}
	return nil
}

// expandHome expands a leading `~` or `~/` to the current user's home dir.
// Pure-string paths (no `~`) pass through unchanged.
func expandHome(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		return home, nil
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}
