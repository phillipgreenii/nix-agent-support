// Package config loads pg-pr configuration from a YAML file.
//
// Resolution order (highest priority first):
//
//  1. $PG_PR_CONFIG (explicit override; missing file is an error).
//  2. $XDG_CONFIG_HOME/pg-pr/config.yaml.
//  3. ~/.config/pg-pr/config.yaml.
//
// Phase 1 only loads the fields the sync engine needs: self_login,
// worktree_root, and a list of repos with team-member / watch-label
// configuration. Additional fields (issues, cicd, pr_body_template,
// ci_only_attempts_threshold) are parsed when present but are not yet
// consumed.
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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
)

// ErrNoConfig is returned by Load when no config file is found.
var ErrNoConfig = errors.New("config: no config file found")

// Config is the parsed pg-pr configuration.
//
// JSON tags mirror the YAML names so `pg-pr config show --json` and
// `--json`-using tools see snake_case keys (matching the on-disk file).
type Config struct {
	// Path is the absolute path the config was loaded from. Populated by Load.
	Path string `yaml:"-" json:"path,omitempty"`

	SelfLogin               string                `yaml:"self_login" json:"self_login"`
	WorktreeRoot            string                `yaml:"worktree_root" json:"worktree_root"`
	Repos                   []RepoConfig          `yaml:"repos" json:"repos"`
	DaemonInterval          string                `yaml:"daemon_interval,omitempty" json:"daemon_interval,omitempty"`
	CIOnlyAttemptsThreshold int                   `yaml:"ci_only_attempts_threshold,omitempty" json:"ci_only_attempts_threshold,omitempty"`
	Agents                  []agentregistry.Entry `yaml:"agents,omitempty" json:"agents,omitempty"`
}

// RepoConfig is a single repo's configuration.
type RepoConfig struct {
	Path           string   `yaml:"path" json:"path,omitempty"`
	Remote         string   `yaml:"remote" json:"remote"`
	VCS            string   `yaml:"vcs" json:"vcs,omitempty"`
	CICD           []string `yaml:"cicd,omitempty" json:"cicd,omitempty"`
	Issues         string   `yaml:"issues,omitempty" json:"issues,omitempty"`
	Org            string   `yaml:"org,omitempty" json:"org,omitempty"`
	TeamMembers    []string `yaml:"team_members,omitempty" json:"team_members,omitempty"`
	WatchLabels    []string `yaml:"watch_labels,omitempty" json:"watch_labels,omitempty"`
	PRBodyTemplate string   `yaml:"pr_body_template,omitempty" json:"pr_body_template,omitempty"`
	// TicketPatterns is a list of Go regular-expression strings used to
	// extract linked external ticket keys from a PR's branch name, title,
	// and body. Each pattern is applied in order; keys are de-duplicated
	// across all three fields. An empty slice disables ticket-key
	// extraction for this repo. No patterns are hardcoded in pg-pr itself;
	// the consuming config (e.g. phillipg-nix-ziprecruiter) supplies them.
	TicketPatterns []string `yaml:"ticket_patterns,omitempty" json:"ticket_patterns,omitempty"`
}

// Load reads and parses the config file using the resolution order described
// in the package doc. If no config file is found and no explicit
// $PG_PR_CONFIG override is set, Load returns ErrNoConfig wrapped with a
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
	if explicit := env.Getenv("PG_PR_CONFIG"); explicit != "" {
		cfg, err := LoadFile(explicit)
		if err != nil {
			// If PG_PR_CONFIG is set but missing, give a focused message.
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("config: $PG_PR_CONFIG=%s does not exist", explicit)
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

	return nil, fmt.Errorf("%w: looked in %s; create one or set $PG_PR_CONFIG",
		ErrNoConfig, strings.Join(candidates, ", "))
}

// defaultCandidates returns the list of paths Load checks in order.
func defaultCandidates(env envSource) []string {
	var out []string
	if xdg := env.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		out = append(out, filepath.Join(xdg, "pg-pr", "config.yaml"))
	}
	if home, err := env.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".config", "pg-pr", "config.yaml"))
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

// finalize validates required fields and expands ~ in path-like fields.
func finalize(cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if strings.TrimSpace(cfg.SelfLogin) == "" {
		return errors.New("self_login is required")
	}
	if strings.TrimSpace(cfg.WorktreeRoot) == "" {
		return errors.New("worktree_root is required")
	}
	wr, err := expandHome(cfg.WorktreeRoot)
	if err != nil {
		return fmt.Errorf("worktree_root: %w", err)
	}
	cfg.WorktreeRoot = wr

	if len(cfg.Repos) == 0 {
		return errors.New("repos: at least one repo is required")
	}
	seen := make(map[string]struct{})
	for i := range cfg.Repos {
		r := &cfg.Repos[i]
		if strings.TrimSpace(r.Remote) == "" {
			return fmt.Errorf("repos[%d]: remote is required", i)
		}
		if !strings.Contains(r.Remote, "/") {
			return fmt.Errorf("repos[%d]: remote %q is not in owner/name form", i, r.Remote)
		}
		if _, dup := seen[r.Remote]; dup {
			return fmt.Errorf("repos[%d]: duplicate remote %q", i, r.Remote)
		}
		seen[r.Remote] = struct{}{}
		if strings.TrimSpace(r.VCS) == "" {
			r.VCS = "github"
		}
		if r.Path != "" {
			p, err := expandHome(r.Path)
			if err != nil {
				return fmt.Errorf("repos[%d].path: %w", i, err)
			}
			r.Path = p
		}
	}
	return nil
}

// ValidationIssue describes a single issue raised by Validate. Issues are
// classified as Errors (fail validation) or Warnings (informational; do not
// fail). Both surface in `pg-pr config validate` output.
type ValidationIssue struct {
	// Severity is "error" or "warning".
	Severity string `json:"severity"`
	// Path is a dotted/bracketed pointer into the config tree, e.g.
	// "repos[0].path" or "self_login".
	Path string `json:"path"`
	// Message is the human-readable description.
	Message string `json:"message"`
}

// ValidationReport bundles the issues raised by Validate. The HasErrors
// method reports whether any issue has severity "error"; that is the
// signal `pg-pr config validate` uses to decide its exit code.
type ValidationReport struct {
	Issues []ValidationIssue `json:"issues"`
}

// HasErrors reports whether the report contains any issue with severity
// "error".
func (r *ValidationReport) HasErrors() bool {
	for _, i := range r.Issues {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}

// Validate runs the full validation pass over a Config that has already
// been parsed + finalized (i.e. via Load / LoadFile). It re-checks the
// required-field invariants finalize already enforced (cheap; gives
// Validate a single, complete contract) and adds the higher-level
// invariants documented in the spec:
//
//   - each repo.path exists on disk (warning only — gives flexibility for
//     hosts that don't have every clone yet);
//   - each repo.vcs / repo.cicd[i] / repo.issues names a known provider
//     (builtin or `exec:`-style);
//   - each repo.cicd has at least one entry;
//   - team_members entries are non-empty strings.
//
// Validate never mutates cfg. Returns a populated ValidationReport; the
// returned error is non-nil only when cfg itself is nil.
func (cfg *Config) Validate() (*ValidationReport, error) {
	if cfg == nil {
		return nil, errors.New("config: nil")
	}
	rep := &ValidationReport{}
	add := func(severity, path, msg string) {
		rep.Issues = append(rep.Issues, ValidationIssue{
			Severity: severity, Path: path, Message: msg,
		})
	}

	if strings.TrimSpace(cfg.SelfLogin) == "" {
		add("error", "self_login", "required")
	}
	if strings.TrimSpace(cfg.WorktreeRoot) == "" {
		add("error", "worktree_root", "required")
	}
	if len(cfg.Repos) == 0 {
		add("error", "repos", "at least one repo is required")
	}

	known := knownBuiltinProviders()
	for i, r := range cfg.Repos {
		prefix := fmt.Sprintf("repos[%d]", i)
		if strings.TrimSpace(r.Remote) == "" {
			add("error", prefix+".remote", "required")
		} else if !strings.Contains(r.Remote, "/") {
			add("error", prefix+".remote",
				fmt.Sprintf("must be owner/name form, got %q", r.Remote))
		}
		if r.Path != "" {
			if _, err := os.Stat(r.Path); err != nil {
				add("warning", prefix+".path",
					fmt.Sprintf("does not exist on disk: %s", r.Path))
			}
		}
		if r.VCS != "" && !providerKnown(r.VCS, known) {
			add("error", prefix+".vcs",
				fmt.Sprintf("unknown provider %q (expected builtin or 'exec:<binary>')", r.VCS))
		}
		if len(r.CICD) == 0 {
			add("warning", prefix+".cicd",
				"no CI/CD provider configured; ci subcommands will return an error")
		}
		for j, c := range r.CICD {
			if !providerKnown(c, known) {
				add("error", fmt.Sprintf("%s.cicd[%d]", prefix, j),
					fmt.Sprintf("unknown provider %q (expected builtin or 'exec:<binary>')", c))
			}
		}
		if r.Issues != "" && !providerKnown(r.Issues, known) {
			add("error", prefix+".issues",
				fmt.Sprintf("unknown provider %q (expected builtin or 'exec:<binary>')", r.Issues))
		}
		for j, m := range r.TeamMembers {
			if strings.TrimSpace(m) == "" {
				add("error", fmt.Sprintf("%s.team_members[%d]", prefix, j),
					"empty team-member entry")
			}
		}
	}
	return rep, nil
}

// knownBuiltinProviders returns the set of builtin provider names. The
// set is small enough to enumerate inline — keeping it here avoids a
// dependency on the provider packages (which would create an import
// cycle: provider packages depend on config types).
func knownBuiltinProviders() map[string]struct{} {
	return map[string]struct{}{
		"github":         {},
		"github-actions": {},
		"github-issues":  {},
		"jira":           {},
	}
}

// providerKnown reports whether name is a known builtin provider or an
// `exec:<binary>`-style reference. We don't verify the exec binary exists
// on PATH here; that's the job of `pg-pr auth status`.
func providerKnown(name string, builtins map[string]struct{}) bool {
	if strings.HasPrefix(name, "exec:") && len(name) > len("exec:") {
		return true
	}
	_, ok := builtins[name]
	return ok
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
