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
	jiraprovider "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues/jira"
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

	// ApproverAllowlist is the set of logins whose verdict is allowed to
	// count toward PR approval. It is a SEPARATE set from the agent
	// registration set (Agents / agentregistry.Entry.Login): being a
	// registered, ingested agent does NOT imply approver status, and this
	// list is never implied by an agent entry's ApprovalRegex being
	// non-empty — membership here is always explicit. Absent/empty disables
	// the mechanism with no error, so an existing deployment config with
	// only login+approval_regex agent entries keeps loading exactly as
	// before this field was added, and none of its entries silently start
	// counting as approvers. Nothing is hardcoded in pg-pr itself; the
	// consuming config (e.g. phillipg-nix-ziprecruiter) supplies the actual
	// logins allowed to approve, as distinct from agents that are ingested
	// for findings only.
	ApproverAllowlist []string `yaml:"approver_allowlist,omitempty" json:"approver_allowlist,omitempty"`

	// VerdictGenerations is the ORDERED list of per-generation
	// verdict-grammar declarations (see VerdictGeneration). Declaration
	// order is preserved through YAML decoding and is load-bearing: a
	// future sibling leaf — the grammar-consuming parser, explicitly out of
	// scope here — resolves ties by "highest declared generation wins", so
	// this layer must hand that leaf the generations in the order they were
	// declared. Absent/empty disables the mechanism with no error.
	VerdictGenerations []VerdictGeneration `yaml:"verdict_generations,omitempty" json:"verdict_generations,omitempty"`

	// Jira, when non-nil, enables the Jira priority/incident urgency signal
	// (pg2-jpfw.4). When nil (the default), the signal is disabled and
	// behaviour is identical to before this bead.
	//
	// Public-repo hygiene: no org-specific Jira URLs, project keys, auth
	// tokens, or instance names appear in this struct. All deployment-specific
	// details MUST be supplied via the config file.
	Jira *JiraConfig `yaml:"jira,omitempty" json:"jira,omitempty"`
}

// JiraConfig configures the Jira priority/incident urgency signal (pg2-jpfw.4).
//
// The signal is enabled only when config.Jira is non-nil. The binary name for
// the subprocess-backed Jira provider is controlled by the PGPR_JIRA_BINARY
// environment variable (default "jira") — see pkg/provider/issues/jira.
//
// Example YAML section (all values are fictional/generic):
//
//	jira:
//	  high_priority_values: [Highest, High]
//	  incident_labels: [incident]
//	  incident_issue_types: [Incident]
//
// Public-repo hygiene: populate high_priority_values, incident_labels, and
// incident_issue_types from your own Jira instance's terminology. No defaults
// are baked in here.
type JiraConfig struct {
	// AdapterCfg drives the mapping from api.Issue fields to JiraTicketInfo.
	// Embedded inline in YAML so the top-level jira: keys map directly to
	// AdapterConfig fields (high_priority_values, incident_labels,
	// incident_issue_types).
	jiraprovider.AdapterConfig `yaml:",inline" json:",inline"`
}

// VerdictGeneration describes one generation of the review-comment verdict
// grammar. A "generation" is a labeled format a bot-authored review comment
// body can take; declaring generations in config (rather than in Go) means
// a future bot-format change is a config edit, not a Go change.
//
// Each generation carries its own anchor/marker (BodyMarker) used to
// recognize a comment body as belonging to this generation, and two
// SEPARATE pattern sets: FindingsPatterns (the findings axis — e.g. how
// many outstanding review findings the body reports) and AuthorityPatterns
// (the authority axis — e.g. an explicit approve / request-changes
// verdict). Keeping the two axes as separate pattern sets lets a comment
// carry findings without an authority verdict, or vice versa.
//
// This type defines only the SHAPE a generation declaration takes; the
// parser that walks a comment body against declared generations and
// produces (findings, authority) results is a separate, out-of-scope leaf
// (bead pg2-4dz88.1.3's scope notes).
type VerdictGeneration struct {
	// ID identifies this generation (e.g. "v1", "v2"). Free-form; the
	// (out-of-scope) consuming parser uses it to report which generation a
	// comment matched, and to break ties by declared order.
	ID string `yaml:"id" json:"id"`
	// BodyMarker is the anchor/marker substring that identifies a comment
	// body as belonging to this generation (e.g. a generated-comment marker
	// unique to this bot-format version).
	BodyMarker string `yaml:"body_marker" json:"body_marker"`
	// FindingsPatterns is a list of Go regular-expression strings matched
	// against the comment body to extract the findings axis for this
	// generation. Nothing is hardcoded in pg-pr itself; the consuming
	// config supplies the actual patterns for its own bot's format.
	FindingsPatterns []string `yaml:"findings_patterns,omitempty" json:"findings_patterns,omitempty"`
	// AuthorityPatterns is a list of Go regular-expression strings matched
	// against the comment body to extract the authority axis (the
	// approve/request-changes verdict) for this generation.
	AuthorityPatterns []string `yaml:"authority_patterns,omitempty" json:"authority_patterns,omitempty"`
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
	// ExcludedCIChecks is a list of Go regular-expression strings matched
	// against each CI check's name. A matched check is EXCLUDED from the CI
	// failure rollup entirely (never fails/pends/passes it) — see
	// internal/cirollup. Nothing is hardcoded in pg-pr; the consuming config
	// (e.g. phillipg-nix-ziprecruiter) supplies patterns such as `^policy-bot`.
	ExcludedCIChecks []string `yaml:"excluded_ci_checks,omitempty" json:"excluded_ci_checks,omitempty"`
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

	for j, login := range cfg.ApproverAllowlist {
		if strings.TrimSpace(login) == "" {
			add("error", fmt.Sprintf("approver_allowlist[%d]", j),
				"empty allowlist entry")
		}
	}

	for i, g := range cfg.VerdictGenerations {
		prefix := fmt.Sprintf("verdict_generations[%d]", i)
		if strings.TrimSpace(g.ID) == "" {
			add("error", prefix+".id", "required")
		}
		if strings.TrimSpace(g.BodyMarker) == "" {
			add("error", prefix+".body_marker", "required")
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
