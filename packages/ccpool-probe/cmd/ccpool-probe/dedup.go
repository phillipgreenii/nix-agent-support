// dedup.go: the "nothing new" rule [Binding decisions: "'Nothing new'
// rule" bullet] — given a finding and the existing `escalated`-labeled
// beads returned by connector.go's listEscalated, decides whether to
// create a fresh bead, comment+update an existing one, or write nothing.
//
// metaState is this probe's own implementation choice for tracking "what
// did we last tell bd about this finding" — the design's own text
// describes the comparison as "since the matched bead's last comment",
// but `pg-connector issue list` (this dedup check's ONLY allowed data
// source, per the same Binding decisions paragraph) returns each issue's
// metadata, not its comment history, so this probe tracks the
// comparison-relevant field as metadata it itself writes on every
// create/update, rather than parsing free-text comment bodies it cannot
// otherwise retrieve through this call. Identical mechanism to
// packages/pg-router-probe/cmd/pg-router-probe/dedup.go, minus the
// grafana-alert-specific aliases/episode-count fields this probe has no
// equivalent of.
package main

const (
	metaFingerprint = "pg_router_escalation_fingerprint"
	metaState       = "pg_router_escalation_state"
)

// dedupAction is what run.go should do about one finding, given the
// existing escalated-work beads.
type dedupAction int

const (
	actionSkip dedupAction = iota
	actionCreate
	actionUpdate
)

// matchExisting finds the first existing issue whose own fingerprint
// metadata equals f.Fingerprint — exact-match only, no fuzzy matching
// [design: "Fingerprint metadata key" paragraph].
func matchExisting(f finding, existing []connectorIssue) *connectorIssue {
	for i := range existing {
		e := &existing[i]
		if e.Metadata[metaFingerprint] == f.Fingerprint {
			return e
		}
	}
	return nil
}

// decideAction implements the "nothing new" rule [Binding decisions:
// "'Nothing new' rule" bullet]:
//   - no matching bead at all -> always actionCreate. For kindZombieDrift
//     this is the common path a band CHANGE takes, since the band is
//     baked into the fingerprint itself (fingerprint.go's
//     zombieDriftFingerprint) — a new band is, by construction, a fresh
//     fingerprint with no existing match.
//   - a matching bead exists but its own recorded state metadata differs
//     from f.State -> actionUpdate (comment + refresh metadata). For
//     kindZombieDrift this is effectively unreachable today (a matching
//     fingerprint already implies the same band string), kept only so
//     the mechanism generalizes if a future band ever shares a
//     fingerprint with a differing State. For kindNeedsInput, f.State is
//     currently always the fixed "needs_input" literal (see checks.go's
//     own finding.State doc comment on why), so this is likewise
//     unreachable in practice today — this run always lands on
//     actionSkip below once a bead already exists for that session,
//     which correctly realizes "file once per stuck session, stay quiet
//     while it remains stuck."
//   - otherwise -> actionSkip (nothing new).
func decideAction(f finding, existing []connectorIssue) (dedupAction, *connectorIssue) {
	match := matchExisting(f, existing)
	if match == nil {
		return actionCreate, nil
	}
	if match.Metadata[metaState] != f.State {
		return actionUpdate, match
	}
	return actionSkip, match
}

// trackedMetadata is what create/update writes on every finding, so a
// LATER run's decideAction above can compare against it.
func trackedMetadata(f finding) map[string]string {
	return map[string]string{
		metaFingerprint: f.Fingerprint,
		metaState:       f.State,
	}
}
