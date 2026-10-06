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
// ci_only_attempts_threshold, review_exempt_checks, jira (high_priority_values, incident_labels,
// incident_issue_types), category_vocabulary, urgency (labels, keywords,
// thresholds), agent_tracker_backend, actor, sync.mode, heartbeat_period,
// stale_after, serve.addr, serve.log, and open.chrome_bin — all 21 keys are
// typed here now, most consumed by this docket's later packets;
// agent_tracker_backend starts Phase 10, sync.mode/jira.*/ticket_patterns
// start Phase 10/13 respectively (present but unused by this phase's own
// code). sync.retry (max_retries, initial_backoff, max_backoff) postdates
// that table: it bounds the automatic retry of a recorded sync_error (bead
// pg2-xb6fs; see SyncRetryConfig).
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
	"sort"
	"strconv"
	"strings"
	"time"

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

	SelfLogin string `yaml:"self_login" json:"self_login"`
	// SelfIssueOwner is the bd owner identity treated as "mine" for issue
	// entities (the issue-type counterpart of SelfLogin).
	SelfIssueOwner string   `yaml:"self_issue_owner,omitempty" json:"self_issue_owner,omitempty"`
	TeamMembers    []string `yaml:"team_members,omitempty" json:"team_members,omitempty"`
	WatchLabels    []string `yaml:"watch_labels,omitempty" json:"watch_labels,omitempty"`

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

	// ReviewExemptChecks lists CI JOB names (matched exactly, case-sensitive)
	// whose failure alone does not make a PR unreviewable: when every failed
	// job of every failed run on the PR's head is in this list, the PR is
	// treated as reviewable even though its CI state still reads "failure".
	// Empty (the default) exempts nothing. Deployment-specific names are
	// supplied here, never hard-coded.
	ReviewExemptChecks []string `yaml:"review_exempt_checks,omitempty" json:"review_exempt_checks,omitempty"`

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

	// AreaLabels derives area labels for the merge-request (anchor) bead from
	// the PR title or branch, and is the vocabulary the review-pr /
	// process-feedback children copy from their anchor (bead pg2-lvoye). Empty
	// (the default) labels nothing. Labels are only ever ADDED: a label an
	// operator or agent put on a bead is never removed by sync.
	AreaLabels []AreaLabelRule `yaml:"area_labels,omitempty" json:"area_labels,omitempty"`

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

	// Links configures the read-only `pg-desk links` verb (bead pg2-apuyx).
	Links LinksConfig `yaml:"links,omitempty" json:"links,omitempty"`

	// Attention configures the read-time attention evaluator
	// (docs/behavior/pg-desk/attention.md, "Configuration"). A missing block
	// is valid: every rule kind then takes its built-in default.
	Attention AttentionConfig `yaml:"attention,omitempty" json:"attention,omitempty"`

	// Watch, Sweep, Hydration, ChangeLogRetentionRaw and
	// ConsumerStaleAfterRaw are the entity-change-flow keys (design 9.10,
	// docs/superpowers/specs/2026-09-29-entity-change-flow-design.md). Read
	// them through the typed accessors (WatchQueries, ThreadActiveWindow,
	// SweepMaxAge, ReconcileAge, SweepMaxPerPoll, HydrationMaxPerPoll, ChangeLogRetention,
	// ConsumerStaleAfter), which own the documented defaults.
	Watch     WatchConfig     `yaml:"watch,omitempty" json:"watch,omitempty"`
	Sweep     SweepConfig     `yaml:"sweep,omitempty" json:"sweep,omitempty"`
	Hydration HydrationConfig `yaml:"hydration,omitempty" json:"hydration,omitempty"`
	// ChangeLogRetentionRaw is change_log_retention (a duration such as
	// "14d"); empty means unset.
	ChangeLogRetentionRaw string `yaml:"change_log_retention,omitempty" json:"change_log_retention,omitempty"`
	// ConsumerStaleAfterRaw is consumer_stale_after (a duration such as
	// "7d"); empty means unset.
	ConsumerStaleAfterRaw string `yaml:"consumer_stale_after,omitempty" json:"consumer_stale_after,omitempty"`
}

// Defaults for the entity-change-flow keys (design 9.10, 8.4, 8.5).
// change_log_retention and consumer_stale_after have no default here on
// purpose: their accessors return zero when unset so the store's own
// DefaultChangeLogRetention / DefaultConsumerStaleAfter stay the single home
// of those numbers.
const (
	DefaultThreadActiveWindow  = 7 * 24 * time.Hour
	DefaultSweepMaxAge         = 6 * time.Hour
	DefaultReconcileAge        = 30 * time.Minute
	DefaultSweepMaxPerPoll     = 20
	DefaultHydrationMaxPerPoll = 50
)

// WatchConfig is config.yaml's watch block: per entity type, the named
// pg-connector queries whose results form the watched set.
type WatchConfig struct {
	PR     WatchTypeConfig   `yaml:"pr,omitempty" json:"pr,omitempty"`
	Issue  WatchTypeConfig   `yaml:"issue,omitempty" json:"issue,omitempty"`
	Thread WatchThreadConfig `yaml:"thread,omitempty" json:"thread,omitempty"`
}

// WatchTypeConfig is the watch.pr / watch.issue block.
type WatchTypeConfig struct {
	Queries []string `yaml:"queries,omitempty" json:"queries,omitempty"`
}

// WatchThreadConfig is the watch.thread block.
type WatchThreadConfig struct {
	Queries []string `yaml:"queries,omitempty" json:"queries,omitempty"`
	// ActiveWindow is how long a thread stays active after its last
	// activity (a duration such as "7d"); empty means DefaultThreadActiveWindow.
	ActiveWindow string `yaml:"active_window,omitempty" json:"active_window,omitempty"`
}

// SweepConfig is config.yaml's sweep block (the rolling re-hydration sweep).
type SweepConfig struct {
	// MaxAge is how stale a row may be before the sweep re-hydrates it
	// (a duration such as "6h"); empty means DefaultSweepMaxAge.
	MaxAge string `yaml:"max_age,omitempty" json:"max_age,omitempty"`
	// MaxPerPoll caps sweep re-hydrations per poll (and, separately, the
	// local reconcile tier's re-emits per poll); nil means
	// DefaultSweepMaxPerPoll. A pointer so an explicit 0 is rejected rather
	// than mistaken for unset.
	MaxPerPoll *int `yaml:"max_per_poll,omitempty" json:"max_per_poll,omitempty"`
	// ReconcileAge is how old an active entity's latest change_log row may be
	// before the LOCAL reconcile tier re-emits a reconcile record for it (a
	// duration such as "30m"; no remote call, so it MAY be lowered); empty
	// means DefaultReconcileAge.
	ReconcileAge string `yaml:"reconcile_age,omitempty" json:"reconcile_age,omitempty"`
}

// HydrationConfig is config.yaml's hydration block.
type HydrationConfig struct {
	// MaxPerPoll caps hydrations per poll; nil means
	// DefaultHydrationMaxPerPoll.
	MaxPerPoll *int `yaml:"max_per_poll,omitempty" json:"max_per_poll,omitempty"`
}

// DefaultInProgressStatuses is the status set jira.in_progress_statuses
// defaults to: the classic default Jira workflow's in-progress status.
var DefaultInProgressStatuses = []string{"In Progress"}

// InProgressStatuses returns jira.in_progress_statuses, or
// DefaultInProgressStatuses when none is configured. Safe on a nil Config.
func (c *Config) InProgressStatuses() []string {
	if c != nil && c.Jira != nil && len(c.Jira.InProgressStatuses) > 0 {
		return c.Jira.InProgressStatuses
	}
	return DefaultInProgressStatuses
}

// WatchQueries returns the configured pg-connector query names for an entity
// type ("pr", "issue" or "thread"); nil for a type with none or an unknown
// type.
func (c *Config) WatchQueries(entityType string) []string {
	switch entityType {
	case "pr":
		return c.Watch.PR.Queries
	case "issue":
		return c.Watch.Issue.Queries
	case "thread":
		return c.Watch.Thread.Queries
	}
	return nil
}

// ThreadActiveWindow returns watch.thread.active_window (default 7 days).
func (c *Config) ThreadActiveWindow() time.Duration {
	return durationOrDefault(c.Watch.Thread.ActiveWindow, DefaultThreadActiveWindow)
}

// SweepMaxAge returns sweep.max_age (default 6h).
func (c *Config) SweepMaxAge() time.Duration {
	return durationOrDefault(c.Sweep.MaxAge, DefaultSweepMaxAge)
}

// ReconcileAge returns sweep.reconcile_age (default 30m): the age of an
// active entity's latest change_log row past which the local reconcile tier
// re-emits a reconcile record for it.
func (c *Config) ReconcileAge() time.Duration {
	return durationOrDefault(c.Sweep.ReconcileAge, DefaultReconcileAge)
}

// SweepMaxPerPoll returns sweep.max_per_poll (default 20).
func (c *Config) SweepMaxPerPoll() int {
	if c.Sweep.MaxPerPoll == nil {
		return DefaultSweepMaxPerPoll
	}
	return *c.Sweep.MaxPerPoll
}

// HydrationMaxPerPoll returns hydration.max_per_poll (default 50).
func (c *Config) HydrationMaxPerPoll() int {
	if c.Hydration.MaxPerPoll == nil {
		return DefaultHydrationMaxPerPoll
	}
	return *c.Hydration.MaxPerPoll
}

// ChangeLogRetention returns change_log_retention, or ZERO when unset so the
// caller passes zero to (*Store).PruneChangeLog and the store's default
// applies.
func (c *Config) ChangeLogRetention() time.Duration {
	return durationOrDefault(c.ChangeLogRetentionRaw, 0)
}

// ConsumerStaleAfter returns consumer_stale_after, or ZERO when unset (see
// ChangeLogRetention).
func (c *Config) ConsumerStaleAfter() time.Duration {
	return durationOrDefault(c.ConsumerStaleAfterRaw, 0)
}

// durationOrDefault parses a value already validated by finalize; an empty
// or (impossible after validation) unparseable value yields def.
func durationOrDefault(v string, def time.Duration) time.Duration {
	if strings.TrimSpace(v) == "" {
		return def
	}
	d, err := parseDayDuration(v)
	if err != nil {
		return def
	}
	return d
}

// parseDayDuration is time.ParseDuration plus a leading whole-or-fractional
// day component: "7d", "14d", "1d12h". The result MUST be positive.
func parseDayDuration(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	var days time.Duration
	rest := v
	if i := strings.Index(v, "d"); i >= 0 {
		n, err := strconv.ParseFloat(v[:i], 64)
		if err != nil || n < 0 || v[:i] == "" {
			return 0, fmt.Errorf("invalid duration %q", v)
		}
		days = time.Duration(n * float64(24*time.Hour))
		rest = v[i+1:]
	}
	var d time.Duration
	if rest != "" {
		var err error
		d, err = time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", v, err)
		}
		if d < 0 {
			return 0, fmt.Errorf("invalid duration %q", v)
		}
	}
	d += days
	if d <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", v)
	}
	return d, nil
}

// validateChangeFlow validates the entity-change-flow keys; every error
// names the offending key.
func validateChangeFlow(cfg *Config) error {
	for _, t := range []struct {
		key     string
		queries []string
	}{
		{"watch.pr.queries", cfg.Watch.PR.Queries},
		{"watch.issue.queries", cfg.Watch.Issue.Queries},
		{"watch.thread.queries", cfg.Watch.Thread.Queries},
	} {
		seen := map[string]bool{}
		for i, q := range t.queries {
			if strings.TrimSpace(q) == "" {
				return fmt.Errorf("%s[%d]: query name must not be empty", t.key, i)
			}
			if seen[q] {
				return fmt.Errorf("%s: duplicate query name %q", t.key, q)
			}
			seen[q] = true
		}
	}
	for _, d := range []struct{ key, val string }{
		{"watch.thread.active_window", cfg.Watch.Thread.ActiveWindow},
		{"sweep.max_age", cfg.Sweep.MaxAge},
		{"sweep.reconcile_age", cfg.Sweep.ReconcileAge},
		{"change_log_retention", cfg.ChangeLogRetentionRaw},
		{"consumer_stale_after", cfg.ConsumerStaleAfterRaw},
	} {
		if strings.TrimSpace(d.val) == "" {
			continue
		}
		if _, err := parseDayDuration(d.val); err != nil {
			return fmt.Errorf("%s: %w", d.key, err)
		}
	}
	for _, n := range []struct {
		key string
		val *int
	}{
		{"sweep.max_per_poll", cfg.Sweep.MaxPerPoll},
		{"hydration.max_per_poll", cfg.Hydration.MaxPerPoll},
	} {
		if n.val != nil && *n.val <= 0 {
			return fmt.Errorf("%s %d must be positive", n.key, *n.val)
		}
	}
	return nil
}

// AttentionConfig is config.yaml's attention block. The set of valid rule
// kinds is owned by internal/attention's registry, not by this package, so
// an unknown key under Rules is rejected by attention.Resolve (which every
// attention consumer calls right after loading the config); this package
// validates only the value vocabularies it can know.
type AttentionConfig struct {
	// Rules maps a rule kind (for example "pr.own-ci-failing") to its tuning.
	Rules map[string]AttentionRuleConfig `yaml:"rules,omitempty" json:"rules,omitempty"`
	// Ordering configures the grouping and ordering stage of the evaluator.
	Ordering AttentionOrderingConfig `yaml:"ordering,omitempty" json:"ordering,omitempty"`
}

// AttentionRuleConfig tunes one rule kind. A nil Enabled and an empty
// Severity mean "the rule's built-in default".
type AttentionRuleConfig struct {
	Enabled  *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Severity string `yaml:"severity,omitempty" json:"severity,omitempty"`
	// StaleAfterDays is the threshold, in calendar days, of a time-based rule
	// (for example issue.stale-in-progress): the rule raises once the
	// triggering age reaches it. Nil means the rule's built-in default. It is
	// valid only on a rule kind that has the parameter (attention.Resolve
	// rejects it elsewhere) and MUST be positive.
	StaleAfterDays *int `yaml:"stale_after_days,omitempty" json:"stale_after_days,omitempty"`
}

// AttentionTiesDefault is the one tie rule the evaluator implements for
// attention.ordering.ties: groups are ordered by their most urgent item's
// severity descending, then group size descending, then entity id.
const AttentionTiesDefault = "severity descending, then group size descending, then entity id"

// AttentionOrderingConfig is the attention.ordering block.
type AttentionOrderingConfig struct {
	// Ties is attention.ordering.ties; empty means AttentionTiesDefault, which
	// is also the only value the vocabulary admits.
	Ties string `yaml:"ties,omitempty" json:"ties,omitempty"`
}

// Validate rejects a tie rule the evaluator does not implement, so a
// deployment cannot believe it re-ordered the feed when it did not.
func (o AttentionOrderingConfig) Validate() error {
	if o.Ties == "" || o.Ties == AttentionTiesDefault {
		return nil
	}
	return fmt.Errorf("attention.ordering.ties %q is not supported; the only supported rule is %q (or leave it unset)", o.Ties, AttentionTiesDefault)
}

// AttentionSeverities is the closed severity vocabulary of
// attention.rules.<kind>.severity, lowest first.
var AttentionSeverities = []string{"low", "medium", "high"}

// validateAttention checks the value vocabularies of the attention block.
func validateAttention(a AttentionConfig) error {
	if err := a.Ordering.Validate(); err != nil {
		return err
	}
	kinds := make([]string, 0, len(a.Rules))
	for k := range a.Rules {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		if d := a.Rules[k].StaleAfterDays; d != nil && *d <= 0 {
			return fmt.Errorf("attention.rules.%s.stale_after_days %d must be positive", k, *d)
		}
		sev := a.Rules[k].Severity
		if sev == "" {
			continue
		}
		ok := false
		for _, v := range AttentionSeverities {
			if sev == v {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("attention.rules.%s.severity %q must be one of %s", k, sev, strings.Join(AttentionSeverities, ", "))
		}
	}
	return nil
}

// LinksConfig is config.yaml's links block.
type LinksConfig struct {
	// IssueURLTemplate turns an issue-tracker key (one matching
	// ticket_patterns) into its web URL: a http(s) URL containing exactly one
	// %s, which is replaced by the URL-escaped key. Empty means no template;
	// the verb then falls back to the URL stored with the issue's snapshot.
	// No tracker instance name belongs in code: it arrives only here.
	IssueURLTemplate string `yaml:"issue_url_template,omitempty" json:"issue_url_template,omitempty"`
}

// validateIssueURLTemplate checks links.issue_url_template: empty is fine;
// otherwise it MUST be an absolute http(s) URL holding exactly one %s and no
// other printf verb or whitespace, so a rendered link can never be a
// non-web URI.
func validateIssueURLTemplate(tmpl string) error {
	if tmpl == "" {
		return nil
	}
	if !strings.HasPrefix(tmpl, "http://") && !strings.HasPrefix(tmpl, "https://") {
		return fmt.Errorf("must start with http:// or https://, got %q", tmpl)
	}
	if strings.ContainsAny(tmpl, " \t\r\n") {
		return fmt.Errorf("must not contain whitespace, got %q", tmpl)
	}
	if strings.Count(tmpl, "%s") != 1 || strings.Count(tmpl, "%") != 1 {
		return fmt.Errorf("must contain exactly one %%s and no other %% sequence, got %q", tmpl)
	}
	return nil
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
	// InProgressStatuses names the tracker status values that mean "work is
	// in progress" (matched case-insensitively). Jira's own status category
	// is not exposed by the connector, so a deployment whose workflow names
	// differ lists them here; empty means DefaultInProgressStatuses. Attention
	// rules over issues read the derived category, never a status name.
	InProgressStatuses []string `yaml:"in_progress_statuses,omitempty" json:"in_progress_statuses,omitempty"`
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
	// Retry bounds the automatic retry of a recorded sync_error (bead
	// pg2-xb6fs). Every key is optional; see SyncRetryConfig.Resolve for the
	// defaults.
	Retry SyncRetryConfig `yaml:"retry,omitempty" json:"retry,omitempty"`
}

// Defaults for sync.retry (bead pg2-xb6fs). A transient sync failure is
// retried with exponential backoff — 1m, 2m, 4m, 8m, 16m, then 30m for
// every later retry — for at most 10 automatic retries, after which the row
// stays a sync_error (exhausted) until an operator acts. The backoff is the
// EARLIEST a retry may run: reconcile, the scheduled pass that performs
// retries, only runs as often as its external scheduler invokes it.
const (
	DefaultSyncRetryMaxRetries     = 10
	DefaultSyncRetryInitialBackoff = time.Minute
	DefaultSyncRetryMaxBackoff     = 30 * time.Minute
)

// SyncRetryConfig is config.yaml's sync.retry block.
type SyncRetryConfig struct {
	// MaxRetries bounds automatic retries after the original failure
	// (default DefaultSyncRetryMaxRetries). 0 disables automatic retry;
	// negative is rejected. A pointer so an explicit 0 is distinguishable
	// from an absent key.
	MaxRetries *int `yaml:"max_retries,omitempty" json:"max_retries,omitempty"`
	// InitialBackoff is the wait before the first retry, doubled for each
	// later one (a Go time.ParseDuration string; default "1m").
	InitialBackoff string `yaml:"initial_backoff,omitempty" json:"initial_backoff,omitempty"`
	// MaxBackoff caps the doubled wait (a Go time.ParseDuration string;
	// default "30m"). MUST NOT be below InitialBackoff.
	MaxBackoff string `yaml:"max_backoff,omitempty" json:"max_backoff,omitempty"`
}

// Resolve applies the defaults and parses the durations. It fails on a
// negative max_retries, an unparseable or non-positive duration, or a
// max_backoff below initial_backoff; finalize calls it so a bad value fails
// config load (and therefore every command and doctor) loudly.
func (r SyncRetryConfig) Resolve() (maxRetries int, initialBackoff, maxBackoff time.Duration, err error) {
	maxRetries = DefaultSyncRetryMaxRetries
	if r.MaxRetries != nil {
		if *r.MaxRetries < 0 {
			return 0, 0, 0, fmt.Errorf("max_retries %d must not be negative", *r.MaxRetries)
		}
		maxRetries = *r.MaxRetries
	}
	initialBackoff, err = parsePositiveDuration("initial_backoff", r.InitialBackoff, DefaultSyncRetryInitialBackoff)
	if err != nil {
		return 0, 0, 0, err
	}
	maxBackoff, err = parsePositiveDuration("max_backoff", r.MaxBackoff, DefaultSyncRetryMaxBackoff)
	if err != nil {
		return 0, 0, 0, err
	}
	if maxBackoff < initialBackoff {
		return 0, 0, 0, fmt.Errorf("max_backoff %s must not be below initial_backoff %s", maxBackoff, initialBackoff)
	}
	return maxRetries, initialBackoff, maxBackoff, nil
}

// parsePositiveDuration parses v (def when empty) and rejects a value that
// is not a positive duration.
func parsePositiveDuration(name, v string, def time.Duration) (time.Duration, error) {
	if strings.TrimSpace(v) == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s %q must be positive", name, v)
	}
	return d, nil
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
			if err := validateBeadsDir(cfg.Repos[i].Remote, expanded); err != nil {
				return err
			}
		}
	}
	if _, _, _, err := cfg.Sync.Retry.Resolve(); err != nil {
		return fmt.Errorf("sync.retry: %w", err)
	}
	if err := validateChangeFlow(cfg); err != nil {
		return err
	}
	if err := validateAreaLabels(cfg.AreaLabels); err != nil {
		return err
	}
	if err := validateIssueURLTemplate(cfg.Links.IssueURLTemplate); err != nil {
		return fmt.Errorf("links.issue_url_template: %w", err)
	}
	if err := validateAttention(cfg.Attention); err != nil {
		return err
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

// validateBeadsDir fails loudly when a configured beads_dir is not a real
// beads workspace, so a stale path (e.g. left behind by a checkout move) stops
// startup / `doctor` with an error naming the repo and path, instead of
// surfacing later as a swallowed per-event `bd` chdir failure. A beads
// workspace is a directory carrying config.yaml or metadata.json.
func validateBeadsDir(remote, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("repos[%s].beads_dir %q is not usable: %w (was the checkout moved? update pg-desk config)", remote, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repos[%s].beads_dir %q is not a directory", remote, dir)
	}
	for _, marker := range []string{"config.yaml", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("repos[%s].beads_dir %q is not a beads workspace (no config.yaml or metadata.json)", remote, dir)
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
