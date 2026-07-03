package enrich

// Package enrich — jira_priority.go
//
// "Linked Jira ticket is high-priority or active incident" urgency signal
// (pg2-4c5i.26).
//
// Design: mirrors broken_project.go exactly. An injectable nil-able function
// field on Input (JiraLookupFunc) is called for each key in
// Input.LinkedTicketKeys. When a ticket is a high-priority issue, urgency is
// bumped by +2 and a "jira-priority:<KEY>" reason is appended. When a ticket
// marks an active production incident, urgency is bumped by +4 (matching the
// project-broken-main bump — sufficient on its own for "high") and a
// "jira-incident:<KEY>" reason is appended. Both can fire for the same ticket
// (both signals contribute their respective bumps). A nil JiraLookupFunc is a
// no-op (signal disabled), preserving 100% backward compatibility.
//
// Bump scale (consistent with pg2-4c5i.25):
//   - high-priority ticket: +2 (medium on its own; lifts the PR above "low")
//   - active-incident ticket: +4 (high on its own; same weight as PR-is-fix)
//
// Live verification deferred: JiraLookupFunc is an injectable type alias.
// The real implementation (querying a Jira REST API via a configured provider)
// is NOT wired here. Callers supply nil to skip the signal entirely; a future
// bead will wire the real Jira client and inject it via config-driven
// dependency injection.
//
// Public-repo hygiene: no org-specific Jira base URLs, project keys, auth
// tokens, or instance identifiers appear in this file. All config lives in the
// consuming private config and is injected at runtime.

import "context"

// JiraTicketInfo holds the priority and incident information for one Jira
// ticket. The zero value (both false) means the ticket is routine; the signal
// does not fire for it.
type JiraTicketInfo struct {
	// HighPriority reports whether the ticket has a high (or critical) priority
	// designation in Jira. Implementations MUST map the Jira priority field to
	// this boolean according to their deployment's priority-threshold config
	// (e.g. "P1 or above" → true). The mapping is config-driven and never
	// baked into this package.
	HighPriority bool
	// ActiveIncident reports whether the ticket represents a live production
	// incident. Implementations MUST derive this from whatever field their Jira
	// instance uses (e.g. a label, an issue-type, or a custom field). The
	// semantics are deployment-specific and injected at runtime.
	ActiveIncident bool
}

// JiraLookupFunc is the injectable function signature for looking up priority
// and incident information for a Jira ticket key. Implementations query an
// external Jira instance via a configured provider. A nil value disables the
// signal entirely. The context carries cancellation / deadline from the calling
// enrichment pass.
//
// Public-repo hygiene: pg-pr defines only this generic interface. All
// deployment-specific details (base URL, auth, priority thresholds, incident
// field mapping, project key whitelist) MUST live in the consuming config and
// be injected as a concrete implementation of this type.
type JiraLookupFunc func(ctx context.Context, ticketKey string) (JiraTicketInfo, error)

// scoreJiraPriority evaluates the "linked Jira ticket is high-priority or
// active incident" urgency signal for each key in in.LinkedTicketKeys.
//
// Scoring per ticket:
//   - active incident: +4 and reason "jira-incident:<KEY>"
//   - high priority: +2 and reason "jira-priority:<KEY>"
//   - both: +4 (incident) + +2 (priority) = +6 and both reason strings
//
// A nil JiraLookupFunc is a no-op. Lookup errors are treated as "unknown" —
// the signal does not fire for that ticket, matching the broken_project.go
// error-handling convention.
func scoreJiraPriority(ctx context.Context, in Input) (score int, reasons []string) {
	if in.JiraLookupFunc == nil {
		return 0, nil
	}
	if len(in.LinkedTicketKeys) == 0 {
		return 0, nil
	}
	for _, key := range in.LinkedTicketKeys {
		info, err := in.JiraLookupFunc(ctx, key)
		if err != nil {
			// Treat lookup errors as "unknown" — do not fire the signal.
			continue
		}
		if info.ActiveIncident {
			score += 4
			reasons = append(reasons, "jira-incident:"+key)
		}
		if info.HighPriority {
			score += 2
			reasons = append(reasons, "jira-priority:"+key)
		}
	}
	return score, reasons
}
