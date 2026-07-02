package enrich

// Package enrich — slack_incident.go
//
// "Related active production incident in Slack" urgency signal (pg2-4c5i.27).
//
// Design: mirrors jira_priority.go exactly. An injectable nil-able function
// field on Input (SlackIncidentFunc) is called with context derived from the
// PR. When an active production incident is detected that is related to the PR
// (as assessed by an LLM with Slack access), urgency is bumped by +4 (matching
// the project-broken-main and jira-incident bumps — sufficient on its own for
// "high") and a "slack-incident:<ref>" reason is appended. A nil
// SlackIncidentFunc is a no-op (signal disabled), preserving 100% backward
// compatibility.
//
// Bump scale (consistent with pg2-4c5i.25 and pg2-4c5i.26):
//   - active production incident: +4 (high on its own; same weight as
//     project-broken-main and jira-incident)
//
// DEFERRED — live verification not wired here. The following are explicitly
// deferred to a future design-cycle bead:
//   - Real Slack client and channel/workspace configuration (workspace ID,
//     incident channel IDs, auth tokens) — all config-driven, injected at
//     runtime from the consuming private config (never in this package).
//   - LLM prompt / model choice for incident-relevance assessment; model
//     selection is config-driven (not hardcoded). Default: latest Claude model
//     family, version resolved at deployment time.
//   - Confidence threshold config: consumers MAY apply a minimum confidence
//     before setting ActiveIncident=true in their SlackIncidentFunc
//     implementation; the threshold is config-driven and deployment-specific.
//   - Injection into sync pipeline (wiring SlackIncidentFunc into the enrichment
//     Input during normal sync runs).
//
// Public-repo hygiene: no org-specific Slack workspace IDs, channel IDs, or
// org identifiers appear in this file or its tests. All deployment-specific
// config lives in the consuming private config and is injected at runtime.
// "Slack" as a product name is acceptable; all specifics are config-driven.

import "context"

// SlackIncidentQuery is the context passed to SlackIncidentFunc so the
// LLM/Slack backend can determine whether the PR is related to an active
// production incident. All fields are informational; implementations use
// whichever fields are relevant to their search strategy.
type SlackIncidentQuery struct {
	// PRTitle is the pull request title. Implementations SHOULD use this as the
	// primary search term for incident cross-reference.
	PRTitle string
	// PRBody is the pull request description. MAY be empty.
	PRBody string
	// PRBranch is the branch name. MAY aid matching (e.g. hotfix/* branches).
	PRBranch string
}

// SlackIncidentInfo holds the result of a Slack production-incident lookup for
// a given PR. The zero value (ActiveIncident=false) means no related active
// incident was found; the signal does not fire.
type SlackIncidentInfo struct {
	// ActiveIncident reports whether an active production incident related to
	// this PR was found in Slack. Implementations MUST set this only when there
	// is high confidence that the incident is (a) currently active and (b)
	// related to the PR's change. The confidence threshold is config-driven and
	// deployment-specific; it MUST NOT be baked into this package.
	ActiveIncident bool
	// Ref is an optional identifier for the incident (e.g. a message timestamp,
	// an incident ticket key, or a short human-readable label). Used to populate
	// the urgency reason string "slack-incident:<ref>" for traceability. MAY be
	// empty; when empty the reason string is "slack-incident" (no suffix).
	Ref string
}

// SlackIncidentFunc is the injectable function signature for determining
// whether there is an active production incident related to a PR. The call is
// backed by an LLM with Slack access (non-deterministic, external). A nil
// value disables the signal entirely. The context carries cancellation /
// deadline from the calling enrichment pass.
//
// Public-repo hygiene: pg-pr defines only this generic interface. All
// deployment-specific details (Slack workspace, channel list, auth, LLM model
// choice, confidence threshold) MUST live in the consuming config and be
// injected as a concrete implementation of this type.
type SlackIncidentFunc func(ctx context.Context, query SlackIncidentQuery) (SlackIncidentInfo, error)

// scoreSlackIncident evaluates the "related active Slack production incident"
// urgency signal. It calls SlackIncidentFunc with a query derived from the PR
// context. When an active related incident is detected, it appends a
// "slack-incident:<ref>" reason (or "slack-incident" when Ref is empty) and
// adds +4 to score. A nil SlackIncidentFunc is a no-op. Lookup errors are
// treated as "unknown" — the signal does not fire, matching the error-handling
// convention in broken_project.go and jira_priority.go.
func scoreSlackIncident(ctx context.Context, in Input) (score int, reasons []string) {
	if in.SlackIncidentFunc == nil {
		return 0, nil
	}
	query := SlackIncidentQuery{
		PRTitle:  in.PR.Title,
		PRBody:   in.PR.Body,
		PRBranch: in.PR.Branch,
	}
	info, err := in.SlackIncidentFunc(ctx, query)
	if err != nil {
		// Treat lookup errors as "unknown" — do not fire the signal.
		return 0, nil
	}
	if !info.ActiveIncident {
		return 0, nil
	}
	score = 4
	reason := "slack-incident"
	if info.Ref != "" {
		reason = "slack-incident:" + info.Ref
	}
	reasons = []string{reason}
	return score, reasons
}
