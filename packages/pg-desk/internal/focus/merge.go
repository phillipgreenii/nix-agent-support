package focus

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The merge flag of `focus select` (spec section 7.2 step 3):
// --merge <keepKey>=<absorbedKey>[,<keepKey>=<absorbedKey>...], repeatable.
// A merge records an EXTERNAL references link from the kept entity to the
// absorbed one whose reason is MergeReason; it writes no focus_selection row
// and no change record, whether or not the kept key is in the plan. The link
// persists, so a later select, run without the flag or by another caller,
// reads the absorbed key as absorbed (Inputs.Absorbed) and does not return it
// as an ordinary candidate. +<absorbedKey> reverses a merge: the link is
// removed again.
//
// Telemetry (D24): no OpenTelemetry or Prometheus signal, no logging; the
// link row is the whole record.

// ParseMerges parses the values of repeated --merge flags into pairs. Every
// failure is an ErrUsage (exit 1, nothing applied).
func ParseMerges(values []string, cfg *config.Config) ([]Merge, error) {
	const remedy = "write --merge <keepKey>=<absorbedKey>"
	var out []Merge
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			kept, absorbed, ok := strings.Cut(part, "=")
			if !ok || strings.TrimSpace(kept) == "" || strings.TrimSpace(absorbed) == "" || strings.Contains(absorbed, "=") {
				return nil, usageErr(remedy, "cannot read merge %q", part)
			}
			kk, err := ParseKey(kept, cfg)
			if err != nil {
				return nil, err
			}
			ak, err := ParseKey(absorbed, cfg)
			if err != nil {
				return nil, err
			}
			out = append(out, Merge{Kept: kk, Absorbed: ak})
		}
	}
	return out, nil
}

// PersistedMerges returns the merges recorded in the store: the external
// references links whose reason is MergeReason, in key order.
func PersistedMerges(in Inputs) []Merge {
	var out []Merge
	for _, l := range in.Links {
		if isMergeLink(l) {
			out = append(out, Merge{
				Kept:     Key{Type: l.FromType, ID: l.FromID},
				Absorbed: Key{Type: l.ToType, ID: l.ToID},
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Absorbed != out[j].Absorbed {
			return keyLess(out[i].Absorbed, out[j].Absorbed)
		}
		return keyLess(out[i].Kept, out[j].Kept)
	})
	return out
}

func isMergeLink(l store.XrefLink) bool {
	return l.Relation == relationReferences && strings.HasPrefix(l.Origin, externalOriginPrefix) && l.Reason == MergeReason
}

// RecordMerges writes the external focus:merge link of every pair, as actor.
// Recording a pair again refreshes it and keeps its first-seen time.
func RecordMerges(st *store.Store, repo string, merges []Merge, actor, at string) error {
	if actor == "" {
		return fmt.Errorf("focus: record merge: no actor available")
	}
	for _, m := range merges {
		if err := st.AddExternalXref(store.XrefLink{
			Repo: repo, FromType: m.Kept.Type, FromID: m.Kept.ID,
			ToType: m.Absorbed.Type, ToID: m.Absorbed.ID,
			Relation: relationReferences, Actor: actor, ActedAt: at, Reason: MergeReason,
			FirstSeen: at, LastConfirmed: at,
		}); err != nil {
			return fmt.Errorf("focus: record merge %s into %s: %w", m.Absorbed, m.Kept, err)
		}
	}
	return nil
}

// RemoveMerges removes the recorded focus:merge link of every pair, whichever
// actor recorded it (the merge belongs to the plan, not to one caller).
func RemoveMerges(st *store.Store, repo string, merges []Merge) error {
	for _, m := range merges {
		links, err := st.ListXrefLinksFrom(repo, m.Kept.Type, m.Kept.ID)
		if err != nil {
			return fmt.Errorf("focus: remove merge %s into %s: %w", m.Absorbed, m.Kept, err)
		}
		for _, l := range links {
			if !isMergeLink(l) || l.ToType != m.Absorbed.Type || l.ToID != m.Absorbed.ID {
				continue
			}
			actor := strings.TrimPrefix(l.Origin, externalOriginPrefix)
			if _, err := st.RemoveExternalXref(repo, l.FromType, l.FromID, l.ToType, l.ToID, l.Relation, actor); err != nil {
				return fmt.Errorf("focus: remove merge %s into %s: %w", m.Absorbed, m.Kept, err)
			}
		}
	}
	return nil
}
