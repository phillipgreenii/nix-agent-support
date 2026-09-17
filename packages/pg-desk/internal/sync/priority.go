package sync

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file ports the conflict-priority nudge verbatim from
// packages/pg-pr/pkg/beads/mergerequest.go's mergeRequestPriorityDelta/
// nudgedPriority/parsePbase (design section 7.5's Anchor rule: "the
// conflict-priority nudge and its pbase baseline applied via issue update
// ... a straight port of the EXISTING pg-pr/beadsbridge logic"). pg-desk's
// go.mod does not depend on packages/pg-pr (interpret.go's own package doc:
// every pg-pr concept this docket ports is RE-IMPLEMENTED, never imported),
// so this is a re-implementation against pg-connector's generic
// schema.Issue.Priority string (backend-native — "P0".."P4" for the beads
// backend, per pg-connector-issue-beads/internal/backend.go's
// PriorityVocabulary) rather than pg-pr's own int scale.

// pbaseLabelPrefix stashes the pre-conflict-adjustment priority on an
// anchor bead, ported verbatim from pkg/beads/mergerequest.go's
// pbaseLabelPrefix.
const pbaseLabelPrefix = "pbase:"

// bdDefaultPriority is bd's own documented default priority for a bead
// created with no explicit `-p` (ported from
// pkg/beads/mergerequest.go's bdDefaultPriority) — the seed used when no
// anchor bead exists yet.
const bdDefaultPriority = 2

var priorityRE = regexp.MustCompile(`^P([0-4])$`)

// parsePriority decodes a beads-backend priority string ("P0".."P4") into
// its int scale (0=highest). ok is false for any other value (a different
// backend's native scale, or an empty/unset priority) — callers fall back
// to bdDefaultPriority in that case, mirroring the "no bead exists yet"
// seeding pkg/beads/mergerequest.go's ReconcileMergeRequest performs.
func parsePriority(s string) (int, bool) {
	m := priorityRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// formatPriority renders p ("P0".."P4") clamped into bd's valid [0,4] range
// — ported from pkg/beads/mergerequest.go's clampMergeRequestPriority,
// folded into the render step since pg-connector's wire priority is always
// a string.
func formatPriority(p int) string {
	if p < 0 {
		p = 0
	}
	if p > 4 {
		p = 4
	}
	return fmt.Sprintf("P%d", p)
}

// parsePbase extracts the stashed baseline priority from a `pbase:<n>`
// label — ported verbatim from pkg/beads/mergerequest.go's parsePbase.
func parsePbase(labels []string) (int, bool) {
	for _, l := range labels {
		if strings.HasPrefix(l, pbaseLabelPrefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(l, pbaseLabelPrefix)); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// nudgedPriority returns the conflict-adjusted priority: mine/co-owned
// raise (toward 0), team lowers (toward 4). Clamped to [0,4] — ported
// verbatim from pkg/beads/mergerequest.go's nudgedPriority.
func nudgedPriority(p int, actsAsMine bool) int {
	if actsAsMine {
		if p > 0 {
			return p - 1
		}
		return 0
	}
	if p < 4 {
		return p + 1
	}
	return 4
}

// priorityDelta is the pure decision half of the pbase nudge — ported
// verbatim from pkg/beads/mergerequest.go's mergeRequestPriorityDelta:
// given the anchor's CURRENT priority/labels, whether it actsAsMine (mine
// or co-owned; team otherwise), and whether the PR currently has a
// conflict, it returns the label/priority mutations needed, without
// issuing any call. mine/co-owned raise (toward 0, clamp 0); team lowers
// (toward 4, clamp 4).
func priorityDelta(curPriority int, curLabels []string, actsAsMine, hasConflict bool) (addLabels, removeLabels []string, priority int, setPriority bool) {
	baseline, hasBaseline := parsePbase(curLabels)
	switch {
	case hasConflict && !hasBaseline:
		// First conflicting tick: stash current priority, then nudge.
		addLabels = []string{pbaseLabelPrefix + strconv.Itoa(curPriority)}
		desired := nudgedPriority(curPriority, actsAsMine)
		if desired != curPriority {
			return addLabels, nil, desired, true
		}
		return addLabels, nil, 0, false
	case hasConflict && hasBaseline:
		return nil, nil, 0, false // already adjusted this conflict episode — idempotent no-op
	case !hasConflict && hasBaseline:
		// Conflict cleared: restore baseline, drop the marker.
		removeLabels = []string{pbaseLabelPrefix + strconv.Itoa(baseline)}
		if curPriority != baseline {
			return nil, removeLabels, baseline, true
		}
		return nil, removeLabels, 0, false
	default:
		return nil, nil, 0, false // no conflict, no baseline — nothing to do
	}
}
