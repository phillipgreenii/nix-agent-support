// dedup.go: the "nothing new" rule [Binding decisions: "'Nothing new'
// rule" bullet] — given a finding and the existing `escalated`-labeled
// beads returned by connector.go's listEscalated, decides whether to
// create a fresh bead, comment+update an existing one, or write nothing.
//
// Metadata keys this probe reads back on a matched bead
// (pgRouterEscalationStateKey/pgRouterEscalationEpisodeCountKey) are its
// own implementation choice for tracking "what did we last tell bd about
// this finding" — the design's own text describes the comparison as
// "since the matched bead's last comment", but `pg-connector issue list`
// (this dedup check's ONLY allowed data source, per the same Binding
// decisions paragraph) returns each issue's metadata, not its comment
// history, so this probe tracks the comparison-relevant fields as
// metadata it itself writes on every create/update, rather than parsing
// free-text comment bodies it cannot otherwise retrieve through this
// call. No design citation for this specific mechanism.
package main

import "strconv"

const (
	metaFingerprint      = "pg_router_escalation_fingerprint"
	metaFingerprintAlias = "pg_router_escalation_fingerprint_aliases"
	metaState            = "pg_router_escalation_state"
	metaEpisodeCount     = "pg_router_escalation_episode_count"
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
// metadata, or semicolon-joined alias list, equals f.Fingerprint —
// exact-match only, no fuzzy matching [design: "Fingerprint metadata
// key" paragraph].
func matchExisting(f finding, existing []connectorIssue) *connectorIssue {
	for i := range existing {
		e := &existing[i]
		if e.Metadata[metaFingerprint] == f.Fingerprint {
			return e
		}
		for _, alias := range splitAliases(e.Metadata[metaFingerprintAlias]) {
			if alias == f.Fingerprint {
				return e
			}
		}
	}
	return nil
}

func splitAliases(joined string) []string {
	if joined == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(joined); i++ {
		if joined[i] == ';' {
			out = append(out, joined[start:i])
			start = i + 1
		}
	}
	out = append(out, joined[start:])
	return out
}

func joinAliases(aliases []string) string {
	out := ""
	for i, a := range aliases {
		if i > 0 {
			out += ";"
		}
		out += a
	}
	return out
}

// decideAction implements the "nothing new" rule per finding kind
// [Binding decisions: "'Nothing new' rule" bullet]:
//   - no matching bead at all -> always actionCreate.
//   - kindGrafanaAlert: skip unless state or episode count changed since
//     the matched bead's own last recorded values [bullet 1].
//   - kindQueueGrowth: skip unless the value moved into a new severity
//     band since the last note (f.State already carries the CURRENT
//     band; see checkQueueGrowth) [bullet 3, queue-growth half].
//   - kindBinaryHashMismatch: file only when the hash differs from the
//     one already on the matched bead — this probe's OWN dedup rule, the
//     design leaves this kind's rule genuinely undecided [Binding
//     decisions: hash-mismatch paragraph]. "File" is read here as "write
//     to bd" in the SAME comment+update sense every other kind uses
//     (never overwrite the body — Body template's closing sentence
//     applies across every kind, not just grafana/queue-growth), not as
//     a literal second bead sharing the fixed binary-hash-mismatch
//     fingerprint (matchExisting would have found the first one anyway).
func decideAction(f finding, existing []connectorIssue) (dedupAction, *connectorIssue) {
	match := matchExisting(f, existing)
	if match == nil {
		return actionCreate, nil
	}
	switch f.Kind {
	case kindGrafanaAlert:
		if match.Metadata[metaState] != f.State || match.Metadata[metaEpisodeCount] != strconv.Itoa(f.EpisodeCount) {
			return actionUpdate, match
		}
	case kindQueueGrowth, kindBinaryHashMismatch:
		if match.Metadata[metaState] != f.State {
			return actionUpdate, match
		}
	}
	return actionSkip, match
}

// trackedMetadata is what create/update writes on every finding, so a
// LATER run's decideAction above can compare against it — the fingerprint
// (+ aliases for grafana findings) plus the kind-specific comparison
// field(s).
func trackedMetadata(f finding) map[string]string {
	m := map[string]string{metaFingerprint: f.Fingerprint}
	if len(f.Aliases) > 0 {
		m[metaFingerprintAlias] = joinAliases(f.Aliases)
	}
	m[metaState] = f.State
	if f.Kind == kindGrafanaAlert {
		m[metaEpisodeCount] = strconv.Itoa(f.EpisodeCount)
	}
	return m
}
