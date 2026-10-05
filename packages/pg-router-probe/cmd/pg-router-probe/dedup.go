// dedup.go: the "nothing new" rule [Binding decisions: "'Nothing new'
// rule" bullet] — given a finding and the existing `escalated`-labeled
// beads (in EVERY non-closed state, human-labeled included) returned by
// connector.go's listEscalated, decides whether to
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

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	metaFingerprint      = "pg_router_escalation_fingerprint"
	metaFingerprintAlias = "pg_router_escalation_fingerprint_aliases"
	metaState            = "pg_router_escalation_state"
	metaEpisodeCount     = "pg_router_escalation_episode_count"
)

// dedupAction is what run.go should do about one finding, given the
// existing escalated beads.
type dedupAction int

const (
	actionSkip dedupAction = iota
	actionCreate
	actionUpdate // non-grafana kinds: refresh tracked metadata + full-body comment
	// The remaining actions are kindGrafanaAlert-only (pg2-3tt2e):
	actionSeed        // open match, no snapshot state yet: record state, write nothing
	actionRecurrence  // open match, startsAt changed: comment now, episode++
	actionStillFiring // open match, same startsAt, last noted >= interval ago: comment
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

// dedupContext carries the per-run inputs the grafana rows of the decision
// table need besides the finding and the beads: the fingerprint's snapshot
// state, the clock, and the still-firing throttle.
type dedupContext struct {
	state               alertState
	haveState           bool
	now                 time.Time
	stillFiringInterval time.Duration
}

// decideAction implements the "nothing new" rule per finding kind
// [Binding decisions: "'Nothing new' rule" bullet]. existing is the
// non-closed escalated beads only; a closed predecessor never makes a
// finding "already known" (closed matches only feed the body reference,
// see newestClosedMatch).
//   - no matching open bead -> always actionCreate.
//   - kindGrafanaAlert (pg2-3tt2e), with an open match:
//     no snapshot state for the fingerprint -> actionSeed (first tick after
//     deploy; beads filed before this logic carry episode_count=0 and MUST
//     NOT get a comment burst);
//     startsAt differs from the last recorded one -> actionRecurrence
//     (also catches a resolve->refire inside one tick);
//     same startsAt and last noted >= stillFiringInterval ago ->
//     actionStillFiring; otherwise actionSkip. An open match wins over any
//     closed match because only open beads are passed in here.
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
//
// Re-surface side effect (pg2-3tt2e Work 7, DECISION: accept). Any
// comment or metadata write bumps the bead's updated_at, which
// pg-connector's change ledger hashes, so a still-firing comment on an
// open, non-human escalated bead is re-emitted on the escalated-work feed
// and can re-dispatch the triager once per stillFiringInterval. That is
// accepted rather than suppressed: the comment is the only signal that an
// unresolved, still-firing escalation is being ignored, the throttle
// (--still-firing-interval, default 6h) bounds the rate, and suppressing
// it would need this probe to evaluate escalated-work's ready set
// (blockers, claims), which its dedup-only data source does not expose.
// Beads that also carry the human label are excluded from that feed, so
// the same comments reach them without a re-dispatch. An operator who
// finds the re-dispatch noisy raises --still-firing-interval.
func decideAction(f finding, existing []connectorIssue, dc dedupContext) (dedupAction, *connectorIssue) {
	match := matchExisting(f, existing)
	if match == nil {
		return actionCreate, nil
	}
	switch f.Kind {
	case kindGrafanaAlert:
		if !dc.haveState {
			return actionSeed, match
		}
		if !f.StartsAt.IsZero() && !f.StartsAt.Equal(dc.state.LastStartsAt) {
			return actionRecurrence, match
		}
		if dc.now.Sub(dc.state.LastNoted) >= dc.stillFiringInterval {
			return actionStillFiring, match
		}
	case kindQueueGrowth, kindBinaryHashMismatch:
		if match.Metadata[metaState] != f.State {
			return actionUpdate, match
		}
	}
	return actionSkip, match
}

// newestClosedMatch returns the most recently updated issue in closed
// whose fingerprint (or alias) matches f, or nil. "Newest" is by the
// issue's own updated_at (for a closed bead, effectively its close time);
// an issue with a missing/unparseable updated_at ranks oldest, and ties
// keep the earlier list entry.
func newestClosedMatch(f finding, closed []connectorIssue) *connectorIssue {
	var best *connectorIssue
	var bestAt time.Time
	for i := range closed {
		c := &closed[i]
		if matchExisting(f, closed[i:i+1]) == nil {
			continue
		}
		at, _ := time.Parse(time.RFC3339, c.UpdatedAt)
		if best == nil || at.After(bestAt) {
			best, bestAt = c, at
		}
	}
	return best
}

// trackedMetadata is what create/update writes on every finding, so a
// LATER run's decideAction above can compare against it — the fingerprint
// (+ aliases for grafana findings) plus the kind-specific comparison
// field(s). A grafana bead is created covering one episode; recurrence
// bumps the count (recurrenceMetadata).
func trackedMetadata(f finding) map[string]string {
	m := map[string]string{metaFingerprint: f.Fingerprint}
	if len(f.Aliases) > 0 {
		m[metaFingerprintAlias] = joinAliases(f.Aliases)
	}
	m[metaState] = f.State
	if f.Kind == kindGrafanaAlert {
		m[metaEpisodeCount] = "1"
	}
	return m
}

// recurrenceMetadata is the metadata a recurrence writes on the matched
// open bead: state, and the episode count incremented from the bead's
// own value. A bead filed before this logic carries 0 (it already covers
// one episode), so the count never goes below 1 before incrementing.
func recurrenceMetadata(f finding, match *connectorIssue) map[string]string {
	prev, _ := strconv.Atoi(match.Metadata[metaEpisodeCount])
	prev = max(prev, 1)
	return map[string]string{
		metaState:        f.State,
		metaEpisodeCount: strconv.Itoa(prev + 1),
	}
}

// recordEpisode returns st updated for a note about the episode starting
// at startsAt, made at now: the episode is remembered (once) and
// LastNoted advances. A zero startsAt (response carried none) leaves the
// episode identity untouched.
func recordEpisode(st alertState, startsAt, now time.Time) alertState {
	episodes := append([]time.Time(nil), st.Episodes...)
	if !startsAt.IsZero() {
		st.LastStartsAt = startsAt
		seen := false
		for _, e := range episodes {
			if e.Equal(startsAt) {
				seen = true
				break
			}
		}
		if !seen {
			episodes = append(episodes, startsAt)
		}
	}
	st.Episodes = episodes
	st.LastNoted = now
	return st
}

// stillFiringComment renders the Work 5 comment text for a recurrence or
// a still-firing note: "still firing since <startsAt RFC3339>
// (<duration>); episodes in last 7d: <n>; current value: <__values__>."
// st is the state AFTER recordEpisode for this note.
func stillFiringComment(f finding, st alertState, now time.Time) string {
	startsAt := f.StartsAt
	if startsAt.IsZero() {
		startsAt = st.LastStartsAt
	}
	dur := "unknown"
	if !startsAt.IsZero() {
		dur = formatDuration(now.Sub(startsAt))
	}
	values := f.Values
	if values == "" {
		values = "unknown"
	}
	return fmt.Sprintf("still firing since %s (%s); episodes in last 7d: %d; current value: %s",
		formatStartsAt(startsAt), dur, episodesInWindow(st, now), values)
}

// episodesInWindow counts the recorded episodes within alertEpisodeWindow
// of now. The current episode always counts, so a long-running alert
// never reports zero.
func episodesInWindow(st alertState, now time.Time) int {
	cutoff := now.Add(-alertEpisodeWindow)
	n := 0
	for _, e := range st.Episodes {
		if !e.Before(cutoff) || e.Equal(st.LastStartsAt) {
			n++
		}
	}
	return max(n, 1)
}

// formatDuration renders d compactly ("5m", "2h30m", "3d4h"), truncated
// to the minute; negative or sub-minute durations render as "0m".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "0m"
	}
	d = d.Truncate(time.Minute)
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int(d / time.Minute)
	var b strings.Builder
	if days > 0 {
		fmt.Fprintf(&b, "%dd", days)
	}
	if hours > 0 {
		fmt.Fprintf(&b, "%dh", hours)
	}
	if mins > 0 && days == 0 {
		fmt.Fprintf(&b, "%dm", mins)
	}
	return b.String()
}
