package jira

// adapter.go — JiraLookupFunc adapter (pg2-jpfw.4).
//
// NewJiraLookupFunc wraps the in-repo Jira issues.Provider (this package's
// Provider) and returns an enrich.JiraLookupFunc. The mapping is driven
// entirely by AdapterConfig: no org-specific Jira URLs, project keys, or
// hardcoded strings appear here.
//
// Mapping rules (all comparisons are case-insensitive):
//
//   - HighPriority  → api.Issue.Priority matches one of AdapterConfig.HighPriorityValues
//   - ActiveIncident → api.Issue.Labels contains any of AdapterConfig.IncidentLabels, OR
//     api.Issue.IssueType matches any of AdapterConfig.IncidentIssueTypes
//
// If neither IncidentLabels nor IncidentIssueTypes is configured, ActiveIncident
// is never true (documented TODO: add a custom-field path when Jira deployments
// use a different incident signal).
//
// Public-repo hygiene: all config-driven; no org-specific values here.

import (
	"context"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/enrich"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
)

// AdapterConfig drives the mapping from api.Issue fields to enrich.JiraTicketInfo.
// All comparisons are case-insensitive. All fields are optional (zero-value config
// disables the corresponding signal rather than panicking).
//
// Public-repo hygiene: populate these from your deployment's config file; do not
// hardcode org-specific priority names or incident labels in code.
type AdapterConfig struct {
	// HighPriorityValues is the list of api.Issue.Priority strings that map to
	// JiraTicketInfo.HighPriority=true. Comparison is case-insensitive.
	// Example: ["Highest", "High"] (the exact strings depend on your Jira config).
	// Empty → HighPriority is never true.
	HighPriorityValues []string `yaml:"high_priority_values,omitempty" json:"high_priority_values,omitempty"`

	// IncidentLabels is the list of api.Issue.Labels values that indicate an
	// active production incident. If any label matches (case-insensitive),
	// JiraTicketInfo.ActiveIncident is set to true.
	// Example: ["incident", "sev1"]. Empty → label-based incident detection is disabled.
	IncidentLabels []string `yaml:"incident_labels,omitempty" json:"incident_labels,omitempty"`

	// IncidentIssueTypes is the list of api.Issue.IssueType values that indicate
	// an active incident. If the issue type matches any entry (case-insensitive),
	// JiraTicketInfo.ActiveIncident is set to true.
	// Example: ["Incident"]. Empty → issue-type-based incident detection is disabled.
	//
	// TODO(pg2-jpfw): add a CustomField path for Jira deployments that use a
	// custom field (e.g. "Incident Status") instead of labels/issue-type.
	IncidentIssueTypes []string `yaml:"incident_issue_types,omitempty" json:"incident_issue_types,omitempty"`
}

// NewJiraLookupFunc constructs an enrich.JiraLookupFunc that calls the given
// issues.Provider and maps the result to enrich.JiraTicketInfo using cfg.
//
// The returned function is safe for concurrent use (the provider's GetIssue is
// called once per ticket key per enrichment pass; no shared mutable state).
func NewJiraLookupFunc(p issues.Provider, cfg AdapterConfig) enrich.JiraLookupFunc {
	// Precompute lowercase sets for O(1) lookup.
	highPriSet := toLowerSet(cfg.HighPriorityValues)
	incidentLabelSet := toLowerSet(cfg.IncidentLabels)
	incidentTypeSet := toLowerSet(cfg.IncidentIssueTypes)

	return func(ctx context.Context, ticketKey string) (enrich.JiraTicketInfo, error) {
		issue, err := p.GetIssue(ctx, ticketKey)
		if err != nil {
			return enrich.JiraTicketInfo{}, err
		}

		var info enrich.JiraTicketInfo

		// HighPriority: Priority field matches a configured high-priority value.
		if len(highPriSet) > 0 {
			if _, ok := highPriSet[strings.ToLower(issue.Priority)]; ok {
				info.HighPriority = true
			}
		}

		// ActiveIncident: any label matches a configured incident label.
		if len(incidentLabelSet) > 0 {
			for _, lbl := range issue.Labels {
				if _, ok := incidentLabelSet[strings.ToLower(lbl)]; ok {
					info.ActiveIncident = true
					break
				}
			}
		}
		// ActiveIncident: issue type matches a configured incident type.
		if !info.ActiveIncident && len(incidentTypeSet) > 0 {
			if _, ok := incidentTypeSet[strings.ToLower(issue.IssueType)]; ok {
				info.ActiveIncident = true
			}
		}

		return info, nil
	}
}

// toLowerSet converts a slice of strings to a lowercase set for O(1) lookup.
func toLowerSet(ss []string) map[string]struct{} {
	if len(ss) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		m[strings.ToLower(s)] = struct{}{}
	}
	return m
}
