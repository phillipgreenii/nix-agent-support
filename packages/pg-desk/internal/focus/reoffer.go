package focus

import (
	"fmt"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Reoffered returns the PRs whose focus bead is still in play although the
// plan dropped them: the focus_selected annotation reads none (or is unset),
// and the linked bead is open and unclaimed (plain open, marker stale, or its
// source terminal), so the decider's hold rule has not run for the PR since
// the removal. A PR whose bead is held, claimed, closed, has open children,
// or whose source is hidden or suppressed is not returned: the decider would
// do nothing for it. The order is the keys' order.
//
// It reads the store only (no tracker call).
func Reoffered(d LockDeps) ([]Key, error) {
	repo := d.repo()
	in, err := Load(d.Store, d.Config, repo, d.now())
	if err != nil {
		return nil, fmt.Errorf("focus: reoffer: read the store: %w", err)
	}
	seen := map[Key]bool{}
	var keys []Key
	for _, l := range in.Links {
		if l.Relation != relationSource || l.FromType != entityTypeIssue || l.ToType != entityTypePR {
			continue
		}
		k := Key{Type: l.ToType, ID: l.ToID}
		if seen[k] {
			continue
		}
		seen[k] = true
		if a := in.Annotations[k][store.AnnotationFocusSelected]; a != "" && a != annotationNoneWord {
			continue
		}
		keys = append(keys, k)
	}
	live := BuildLive(in, keys, Ranking{}, LiveOptions{Watched: Watched{}})
	var out []Key
	for _, k := range keys {
		switch live.Beads[k].State {
		case StateOpen, StateOpenMarkerStale, StateSourceDone:
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return keyLess(out[i], out[j]) })
	return out, nil
}
