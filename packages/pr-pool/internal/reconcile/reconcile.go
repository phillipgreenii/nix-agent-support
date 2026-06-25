// Package reconcile is pr-pool's observability guard against silently stranded
// feedback cycles. Discovery filters the feedback role with `bd ready --label
// mine` (pg2-ktqh): only self-owned cycles stamped `mine` are dispatched. But a
// pre-existing open self-cycle that was never stamped is, by that filter,
// indistinguishable from a team cycle and is SILENTLY skipped — so a
// missed/partial backfill idles the pool with ZERO signal (the pg2-eo4n
// incident: drain reported feedback=0 despite 13 ready self-owned cycles).
//
// StrandedSelfCycles turns that silent skip into a LOUD signal: it counts open
// `process-feedback:` cycles whose PARENT merge-request bead's metadata.author ==
// self but which LACK the `mine` label, and emits a WARN naming each. It is an
// ADDITIVE read-only guard — it does NOT mutate beads and does NOT change the
// `--label mine` discovery behavior; it only observes and reports.
package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
)

// feedbackTitlePrefix is the cycle-identity marker the feedback role's discovery
// query also keys on (roles.BuiltinRoleSet → BeadsReady.TitlePrefix). A bead is a
// feedback cycle iff its title carries this prefix.
const feedbackTitlePrefix = "process-feedback:"

// mineLabel is the stamp discovery requires (roles.BuiltinRoleSet → BeadsReady
// Labels). A self-owned cycle missing it is invisible to the feedback role.
const mineLabel = "mine"

// authorMetaKey is the key under a merge-request bead's metadata that pg-pr
// records the PR author login under — the same field the worker's authorship
// preamble asserts ("metadata.author is me").
const authorMetaKey = "author"

// StrandedSelfCycles returns the IDs of OPEN `process-feedback:` cycles that are
// self-owned (their parent merge-request bead's metadata.author == self) but LACK
// the `mine` label, and emits a WARN naming each. These are exactly the cycles
// discovery's `bd ready --label mine` filter silently skips.
//
// Self-attribution cost: one `bd show <parent>` per DISTINCT parent of an
// unstamped feedback cycle (cached, so repeated parents cost one call). This runs
// off the hot dispatch loop — a backlog of a few cycles is a handful of reads — so
// the cost is bounded and acceptable for an observability pass.
//
// A bd failure PROPAGATES (it must not masquerade as "nothing stranded"; same
// contract as discovery, pg2-qq9v). When self is unknown ("") nothing can be
// classified, so it returns nil without consulting any parent.
func StrandedSelfCycles(ctx context.Context, br beads.Runner, self string) ([]string, error) {
	// List open beads once; client-side filter to feedback cycles missing `mine`.
	// (bd ready would already exclude blocked cycles; we deliberately use list so a
	// blocked-but-stranded cycle is still surfaced — the operator should know it
	// exists even if it is not yet ready.)
	open, err := beads.List(ctx, br, "--status", "open")
	if err != nil {
		return nil, fmt.Errorf("reconcile: list open cycles: %w", err)
	}

	authorCache := map[string]string{} // parent id -> metadata.author (cached)
	var stranded []string
	for _, iss := range open {
		if !strings.HasPrefix(iss.Title, feedbackTitlePrefix) {
			continue // not a feedback cycle
		}
		if iss.HasLabel(mineLabel) {
			continue // already discoverable — not stranded
		}
		if self == "" || iss.Parent == "" {
			// Cannot attribute authorship (unknown self, or a parentless cycle):
			// do NOT guess. A parentless feedback cycle is its own anomaly but is
			// out of scope for the self-ownership guard.
			continue
		}
		author, err := parentAuthor(ctx, br, iss.Parent, authorCache)
		if err != nil {
			return nil, err
		}
		if author == self {
			stranded = append(stranded, iss.ID)
		}
	}
	sort.Strings(stranded)

	if len(stranded) > 0 {
		// LOUD, observable signal — the whole point of the guard. Naming the cycles
		// (and self) gives the operator an actionable backfill list:
		//   bd update <id> --add-label mine
		slog.Warn("stranded self-owned feedback cycles: parent author is self but `mine` label is missing, so discovery silently skips them — stamp them with `bd update <id> --add-label mine` to make them discoverable",
			"count", len(stranded), "self", self, "cycles", stranded)
	}
	return stranded, nil
}

// parentAuthor returns the metadata.author of the parent merge-request bead,
// memoizing per parent id so repeated parents cost one `bd show`. A bd failure
// propagates; an absent/non-string author yields "" (which never equals a real
// self login, so the cycle is conservatively NOT flagged).
func parentAuthor(ctx context.Context, br beads.Runner, parentID string, cache map[string]string) (string, error) {
	if a, ok := cache[parentID]; ok {
		return a, nil
	}
	parent, err := beads.ShowObj(ctx, br, parentID)
	if err != nil {
		return "", fmt.Errorf("reconcile: show parent %s: %w", parentID, err)
	}
	author, _ := parent.Metadata[authorMetaKey].(string)
	cache[parentID] = author
	return author, nil
}
