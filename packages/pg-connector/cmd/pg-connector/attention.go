// attention.go: the "pg-connector attention list" Tier-1-only CLI verb.
//
// attention.sources is a top-level, always-list-valued registration
// independent of connector.<type> (Registry.AttentionSources), so this is a
// FAN-OUT op like "ci list"/"auth status"/"config validate" (INV-OUT-1) — it
// always queries every registered attention.sources backend, with no
// targeted-op form. Unlike "ci list"'s plain "runs concatenates" merge
// strategy, the per-source list_attention results are deduped/ranked by
// mergeAttentionItems' own merge/sort algorithm below rather than simply
// concatenated — this is the one Tier-1 fan-out whose per-source results
// are actually merged, not just appended, because attention (unlike CI
// runs) has a genuine cross-source identity ({type, id}) and a comparable
// severity signal to rank by.
//
// A backend not implementing list_attention (the wire-level unknown_op
// sentinel) is reported disabled/"not applicable", mirroring
// auth.go/ci.go's own convention for the same case.
package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// AttentionOutcome is "attention list"'s wire response: FanOutOutcome's
// sources[] row per backend actually queried (INV-OUT-1) — its Count
// carrying that source's own RAW pre-merge list_attention item count, never
// the post-dedup merged count — plus the merged/ranked Items and, only when
// a cap actually cut items, Truncated/TotalBeforeCap: this aggregation
// layer's own strict superset of a single source's per-item shape.
// FanOutOutcome is embedded (promoting Sources and ExitCode()) rather than
// reimplemented, matching every other fan-out in this file.
type AttentionOutcome struct {
	FanOutOutcome
	Items          []MergedAttentionItem `json:"items"`
	Truncated      bool                  `json:"truncated,omitempty"`
	TotalBeforeCap int                   `json:"total_before_cap,omitempty"`
}

// MergedAttentionItem is one deduped/merged item in "attention list"'s
// output: a bare schema.AttentionItem (whose Type/ID/Summary/Severity come
// from the most-severe contributing source — see mergeAttentionItems) plus
// Via, the list of sources that reported it. Via/Truncated/TotalBeforeCap
// exist only at this aggregation layer — never on a single source's own
// per-item schema.AttentionItem.
type MergedAttentionItem struct {
	schema.AttentionItem
	Via []string `json:"via"`
}

// fanOutAttentionList queries list_attention against every backend in
// backends (attention.sources' own config order), building one sources[]
// row per backend queried and returning each succeeding backend's raw,
// pre-merge items keyed by backend name for mergeAttentionItems to
// consume. A backend not implementing list_attention (the wire-level
// unknown_op sentinel) is reported disabled/"not applicable" rather than a
// failure, mirroring authStatusOne's/fanOutCIList's own handling exactly.
//
// reg is threaded through (bead pg2-7wqkr, mirroring fanOutCIList's own
// identical "config travels the same way every other Tier-1 verb's
// dispatch path already attaches it" reasoning): a deadline-based
// backend's own list_attention needs its configured
// attention_threshold/attention_exclude (home/programs/pg-connector's
// attention.perBackend option, rendered onto backends.<name>), which can
// only reach it via this call's own config argument -- unlike search's
// sibling fanOutSearch, which deliberately still passes nil (no bead has
// yet needed per-backend config for search). reg may be nil (every
// existing test predating this bead exercises that path); Registry.
// BackendConfig is nil-receiver-safe and simply answers (nil, nil).
func fanOutAttentionList(ctx context.Context, reg *Registry, backends []string) (map[string][]schema.AttentionItem, FanOutOutcome) {
	perSource := make(map[string][]schema.AttentionItem, len(backends))
	// Sources starts as a non-nil empty slice so a zero-backend
	// (misconfigured host) result still marshals its sources[] field as
	// [] rather than null [bug A15].
	out := FanOutOutcome{Sources: make([]SourceResult, 0, len(backends))}
	for _, b := range backends {
		config, err := reg.BackendConfig(b)
		if err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		resp, err := scriptout.Invoke(ctx, b, "list_attention", nil, config)
		if err != nil {
			if errors.Is(err, scriptout.ErrUnknownOp) {
				out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDisabled, Reason: "not applicable"})
				continue
			}
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		var items []schema.AttentionItem
		if err := scriptout.Decode(resp.Result, &items); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		perSource[b] = items
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(items)})
	}
	return perSource, out
}

// rankOrMedium returns s's canonical severityRank (schema.Severity.Rank),
// treating an invalid/missing severity as schema.SeverityMedium's rank: a
// missing severity is treated as medium for ranking/sorting purposes only,
// and is never itself reported as medium on the wire. It is applied here to
// both the winner-selection comparison in mergeAttentionItems (which
// contributing source's summary/severity "wins" a dedup group) and the
// final list sort, since both are the same underlying severity-ranking
// operation. The wire-level MergedAttentionItem field itself is never
// rewritten to hold "medium" — see mergeAttentionItems' own doc comment.
func rankOrMedium(s schema.Severity) int {
	if !s.IsValid() {
		return schema.SeverityMedium.Rank()
	}
	return s.Rank()
}

// attentionMergeKey is the dedup key mergeAttentionItems groups by: {type,
// id}.
type attentionMergeKey struct {
	Type string
	ID   string
}

// attentionGroup accumulates one dedup group's winning contributor and via
// list while mergeAttentionItems walks every source's raw items in config
// order.
type attentionGroup struct {
	// item carries the winning contributor's own Type/ID/Summary/Severity
	// values verbatim — including a genuinely empty Severity, which MUST
	// stay empty on the wire (see rankOrMedium's doc comment).
	item schema.AttentionItem
	// via lists every source that reported this {type, id}, in
	// attention.sources config order (the accumulation order below
	// already produces this for free, since sourceOrder is walked in
	// order).
	via []string
	// winSourceIdx/winIndex locate the winning contributor: its position
	// in sourceOrder (config order) and its own index within that
	// source's raw item slice — used both for the "config order, earliest
	// wins" tiebreak and to stage rule 4's "each source's own item order"
	// tiebreak (see mergeAttentionItems).
	winSourceIdx int
	winIndex     int
	// winRank caches rankOrMedium(item.Severity) so the comparison loop
	// below never re-derives it.
	winRank int
}

// mergeAttentionItems implements the attention capability's own merge/sort
// algorithm over perSource (each queried source's raw list_attention items,
// keyed by backend name — as returned by fanOutAttentionList) and
// sourceOrder (Registry.AttentionSources()'s own config-order list;
// perSource's keys are always a subset of it, since a disabled/degraded
// source contributes no entry):
//
//  1. Dedup by {type, id}, collapsing multi-source hits into one item with
//     a via: [source, ...] list. A merged item's own summary/severity
//     come from the most-severe contributing source (rankOrMedium); a tie
//     at the same rank is broken by attention.sources config order
//     (earliest-configured source wins).
//  2. Sort the overall list by severityRank descending (rankOrMedium
//     again — missing severity ranks as medium for this comparison only).
//  3. Tiebreak by via.length descending.
//  4. Final tiebreak: the winning contributor's own config order
//     (earliest wins), then that source's own original item order — a
//     stable sort, not a second independent comparator. This falls out of
//     staging the groups by (winSourceIdx, winIndex) before the real sort:
//     sort.SliceStable preserves that staged relative order for anything
//     the real comparator (steps 2-4a) considers equal, which is exactly
//     rule 4's "each source's own item order" clause.
//
// No cap is applied here — that is the caller's job (newAttentionListCmd),
// since "no cap by default, --cap N truncates the already-merged/sorted
// list" is a caller-facing concern, not part of the merge algorithm
// itself.
func mergeAttentionItems(perSource map[string][]schema.AttentionItem, sourceOrder []string) []MergedAttentionItem {
	order := make(map[string]int, len(sourceOrder))
	for i, s := range sourceOrder {
		order[s] = i
	}

	groups := make(map[attentionMergeKey]*attentionGroup)
	var keys []attentionMergeKey

	for _, source := range sourceOrder {
		items, ok := perSource[source]
		if !ok {
			continue
		}
		srcIdx := order[source]
		for idx, item := range items {
			key := attentionMergeKey{Type: item.Type, ID: item.ID}
			rank := rankOrMedium(item.Severity)
			g, exists := groups[key]
			if !exists {
				groups[key] = &attentionGroup{
					item:         item,
					via:          []string{source},
					winSourceIdx: srcIdx,
					winIndex:     idx,
					winRank:      rank,
				}
				keys = append(keys, key)
				continue
			}
			g.via = append(g.via, source)
			if rank > g.winRank || (rank == g.winRank && srcIdx < g.winSourceIdx) {
				g.item = item
				g.winSourceIdx = srcIdx
				g.winIndex = idx
				g.winRank = rank
			}
		}
	}

	// Stage by (final winSourceIdx, winIndex) first — see this function's
	// own doc comment on why this makes rule 4's second half fall out of
	// the real sort below rather than needing its own comparator clause.
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := groups[keys[i]], groups[keys[j]]
		if a.winSourceIdx != b.winSourceIdx {
			return a.winSourceIdx < b.winSourceIdx
		}
		return a.winIndex < b.winIndex
	})

	// The real sort: severityRank descending, via.length descending,
	// winning contributor's config order ascending (earliest wins).
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := groups[keys[i]], groups[keys[j]]
		if a.winRank != b.winRank {
			return a.winRank > b.winRank
		}
		if len(a.via) != len(b.via) {
			return len(a.via) > len(b.via)
		}
		return a.winSourceIdx < b.winSourceIdx
	})

	// Non-nil even when empty so "items" marshals as [] rather than null
	// [bug A15's convention, applied here].
	out := make([]MergedAttentionItem, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		out = append(out, MergedAttentionItem{AttentionItem: g.item, Via: g.via})
	}
	return out
}

const attentionCapFlagName = "cap"

func newAttentionCmd() *cobra.Command {
	attentionCmd := &cobra.Command{
		Use:   "attention",
		Short: "Attention capability commands",
	}
	attentionCmd.AddCommand(newAttentionListCmd())
	return attentionCmd
}

func newAttentionListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Fan list_attention out across every registered attention.sources backend, merged/deduped/ranked",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			backends, err := reg.AttentionSources()
			if err != nil {
				return err
			}
			perSource, fanOut := fanOutAttentionList(cmd.Context(), reg, backends)
			outcome := AttentionOutcome{
				FanOutOutcome: fanOut,
				Items:         mergeAttentionItems(perSource, backends),
			}
			if cmd.Flags().Changed(attentionCapFlagName) {
				// Named capN, not cap, to avoid shadowing the builtin
				// cap() elsewhere in this function's scope.
				capN, capErr := cmd.Flags().GetInt(attentionCapFlagName)
				if capErr != nil {
					return capErr
				}
				if capN < 0 {
					return fmt.Errorf("pg-connector: --%s must be >= 0, got %d", attentionCapFlagName, capN)
				}
				// A truncation is always a manifest marker, never a
				// silent omission — but only when the cap actually cuts
				// items: passing --cap with a value at or above the
				// merged count leaves the list unchanged, so there is
				// nothing to mark. total_before_cap is the merged item
				// COUNT before truncation, never the cap value itself.
				if capN < len(outcome.Items) {
					outcome.TotalBeforeCap = len(outcome.Items)
					outcome.Items = outcome.Items[:capN]
					outcome.Truncated = true
				}
			}
			return writeFanOutResult(cmd, outcome, outcome.ExitCode(), func() string {
				return humanizeAttentionList(outcome)
			})
		},
	}
	cmd.Flags().Int(attentionCapFlagName, 0, "cap the merged/ranked attention list to at most N items (default: no cap)")
	return cmd
}

// humanizeAttentionList formats an "attention list" fan-out outcome (its
// merged/ranked Items plus its per-backend Sources rows) for human
// display, mirroring humanizeCiList's own shape for the analogous fan-out.
func humanizeAttentionList(o AttentionOutcome) string {
	var b strings.Builder
	if len(o.Items) == 0 {
		b.WriteString("attention items: (none)\n")
	} else {
		fmt.Fprintf(&b, "attention items (%d):\n", len(o.Items))
		for _, it := range o.Items {
			sev := string(it.Severity)
			if sev == "" {
				sev = "-"
			}
			fmt.Fprintf(&b, "  [%s] %s/%s: %s (via %s)\n", sev, it.Type, it.ID, it.Summary, strings.Join(it.Via, ","))
		}
	}
	if o.Truncated {
		fmt.Fprintf(&b, "  (truncated: showing %d of %d)\n", len(o.Items), o.TotalBeforeCap)
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}
