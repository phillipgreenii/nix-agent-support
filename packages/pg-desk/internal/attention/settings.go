package attention

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// RuleSettings is one rule kind's resolved tuning.
type RuleSettings struct {
	Enabled  bool
	Severity Severity
	// SeverityConfigured is true when the configuration set the severity
	// explicitly (a rule MAY otherwise vary its default by cause).
	SeverityConfigured bool
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
		if cfg, ok := c.Rules[kind]; ok {
			if cfg.Enabled != nil {
				rs.Enabled = *cfg.Enabled
			}
			if cfg.Severity != "" {
				rs.Severity = Severity(cfg.Severity)
				rs.SeverityConfigured = true
			}
		}
		out.Rules[kind] = rs
	}
	return out, nil
}
