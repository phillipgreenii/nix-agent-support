package attention

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// RuleSettings is one rule kind's resolved tuning.
type RuleSettings struct {
	Enabled  bool
	Severity Severity
	// SeverityConfigured is true when the configuration set the severity
	// explicitly (a rule MAY otherwise vary its default by cause).
	SeverityConfigured bool
	// StaleAfter is the threshold of a time-based rule (a rule that has a
	// DefaultStaleAfter): the configured attention.rules.<kind>.stale_after_days
	// in days, else the rule's default. Zero for a rule with no such
	// parameter.
	StaleAfter time.Duration
	// DueSoon is the look-ahead window of a due-date rule (a rule that has a
	// DefaultDueSoon): the configured attention.rules.<kind>.due_soon_days in
	// days, else the rule's default. Zero for a rule with no such parameter.
	DueSoon time.Duration
}

// dueSoonRule is implemented by a due-date rule whose look-ahead window is
// tunable through attention.rules.<kind>.due_soon_days.
type dueSoonRule interface {
	// DefaultDueSoon is the window used when the configuration sets none.
	DefaultDueSoon() time.Duration
}

// staleAfterRule is implemented by a time-based rule whose threshold is
// tunable through attention.rules.<kind>.stale_after_days.
type staleAfterRule interface {
	// DefaultStaleAfter is the threshold used when the configuration sets none.
	DefaultStaleAfter() time.Duration
}

// Settings is the resolved attention configuration: one entry per registered
// rule kind.
type Settings struct {
	Rules map[string]RuleSettings
}

// Resolve turns the attention block of the configuration into Settings. A
// kind missing from the block takes its built-in default (enabled, the rule's
// default severity), so a deployment with no attention block works. A kind
// the registry does not know is an error naming it and the known kinds.
func Resolve(c config.AttentionConfig) (Settings, error) {
	if err := c.Ordering.Validate(); err != nil {
		return Settings{}, err
	}
	known := map[string]Rule{}
	for _, r := range registeredRules() {
		known[r.Kind()] = r
	}
	given := make([]string, 0, len(c.Rules))
	for k := range c.Rules {
		given = append(given, k)
	}
	sort.Strings(given)
	for _, k := range given {
		if _, ok := known[k]; !ok {
			return Settings{}, fmt.Errorf("attention.rules.%s: unknown rule kind (known: %s)", k, strings.Join(RuleKinds(), ", "))
		}
	}
	out := Settings{Rules: make(map[string]RuleSettings, len(known))}
	for kind, r := range known {
		rs := RuleSettings{Enabled: true, Severity: r.DefaultSeverity()}
		sa, hasStaleAfter := r.(staleAfterRule)
		if hasStaleAfter {
			rs.StaleAfter = sa.DefaultStaleAfter()
		}
		ds, hasDueSoon := r.(dueSoonRule)
		if hasDueSoon {
			rs.DueSoon = ds.DefaultDueSoon()
		}
		if cfg, ok := c.Rules[kind]; ok {
			if cfg.Enabled != nil {
				rs.Enabled = *cfg.Enabled
			}
			if cfg.Severity != "" {
				rs.Severity = Severity(cfg.Severity)
				rs.SeverityConfigured = true
			}
			if cfg.StaleAfterDays != nil {
				if !hasStaleAfter {
					return Settings{}, fmt.Errorf("attention.rules.%s.stale_after_days: this rule kind has no such parameter (rules with it: %s)", kind, strings.Join(staleAfterKinds(known), ", "))
				}
				rs.StaleAfter = time.Duration(*cfg.StaleAfterDays) * 24 * time.Hour
			}
			if cfg.DueSoonDays != nil {
				if !hasDueSoon {
					return Settings{}, fmt.Errorf("attention.rules.%s.due_soon_days: this rule kind has no such parameter (rules with it: %s)", kind, strings.Join(dueSoonKinds(known), ", "))
				}
				rs.DueSoon = time.Duration(*cfg.DueSoonDays) * 24 * time.Hour
			}
		}
		out.Rules[kind] = rs
	}
	return out, nil
}

// staleAfterKinds lists, sorted, the rule kinds that take stale_after_days.
func staleAfterKinds(known map[string]Rule) []string {
	var out []string
	for k, r := range known {
		if _, ok := r.(staleAfterRule); ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// dueSoonKinds lists, sorted, the rule kinds that take due_soon_days.
func dueSoonKinds(known map[string]Rule) []string {
	var out []string
	for k, r := range known {
		if _, ok := r.(dueSoonRule); ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
