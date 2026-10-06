package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Fields an AreaLabelRule may match against.
const (
	// AreaFieldTitle matches the PR title (the default).
	AreaFieldTitle = "title"
	// AreaFieldBranch matches the PR head branch name.
	AreaFieldBranch = "branch"
)

// AreaLabelRule maps a pattern over one PR field to the area labels a PR
// matching it carries (bead pg2-lvoye). Every rule is deployment-supplied
// config: nothing org-specific (scopes, ticket keys, label names) is
// hard-coded in this repo.
//
// Example (scope-style titles and ticket-key branches):
//
//	area_labels:
//	  - pattern: '^[a-z]+\(widgets/api\)'
//	    labels: [widgets-api, widgets]
//	  - pattern: '(?i)PROJ-[0-9]+'
//	    field: branch
//	    labels: [proj]
type AreaLabelRule struct {
	// Pattern is a Go regexp (RE2) searched (unanchored) in the field.
	Pattern string `yaml:"pattern" json:"pattern"`
	// Field is "title" (default) or "branch".
	Field string `yaml:"field,omitempty" json:"field,omitempty"`
	// Labels are added to the bead when the pattern matches.
	Labels []string `yaml:"labels" json:"labels"`
}

// validateAreaLabels fails config load on a rule that could never apply: an
// empty or uncompilable pattern, an unknown field, or no labels.
func validateAreaLabels(rules []AreaLabelRule) error {
	for i, r := range rules {
		if strings.TrimSpace(r.Pattern) == "" {
			return fmt.Errorf("area_labels[%d]: pattern is required", i)
		}
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("area_labels[%d]: pattern %q: %w", i, r.Pattern, err)
		}
		switch r.Field {
		case "", AreaFieldTitle, AreaFieldBranch:
		default:
			return fmt.Errorf("area_labels[%d]: field %q must be %q or %q", i, r.Field, AreaFieldTitle, AreaFieldBranch)
		}
		if len(cleanLabels(r.Labels)) == 0 {
			return fmt.Errorf("area_labels[%d]: labels must name at least one label", i)
		}
	}
	return nil
}

// AreaLabelsFor returns the sorted, de-duplicated union of the labels of
// every area_labels rule matching the PR's title or branch. nil when none
// match or none are configured. A rule whose pattern does not compile is
// skipped (finalize rejects such a config at load, so this only guards a
// hand-built Config).
func (c *Config) AreaLabelsFor(title, branch string) []string {
	if c == nil {
		return nil
	}
	set := map[string]bool{}
	for _, r := range c.AreaLabels {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue
		}
		subject := title
		if r.Field == AreaFieldBranch {
			subject = branch
		}
		if !re.MatchString(subject) {
			continue
		}
		for _, l := range cleanLabels(r.Labels) {
			set[l] = true
		}
	}
	return sortedKeys(set)
}

// AreaVocabulary returns the sorted union of every label any area_labels
// rule can emit. It identifies which labels on a parent bead are "area"
// labels (as opposed to operator/agent labels such as co-owned or pbase:N),
// so children copy exactly those.
func (c *Config) AreaVocabulary() []string {
	if c == nil {
		return nil
	}
	set := map[string]bool{}
	for _, r := range c.AreaLabels {
		for _, l := range cleanLabels(r.Labels) {
			set[l] = true
		}
	}
	return sortedKeys(set)
}

func cleanLabels(in []string) []string {
	var out []string
	for _, l := range in {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
