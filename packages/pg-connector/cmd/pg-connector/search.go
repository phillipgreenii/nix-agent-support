// search.go: the "pg-connector search <query>" Tier-1-only CLI verb.
//
// search.sources is a top-level, always-list-valued registration
// independent of connector.<type> (Registry.SearchSources), so this is a
// FAN-OUT op (INV-OUT-1) like "attention list"/"ci list"/"auth status" — it
// always queries every registered search.sources backend, with no
// type-filter parameter and no way to scope a query to a subset of sources
// (this packet's own Binding decisions).
//
// Unlike "attention list"'s merge/dedup/rank algorithm, search performs NO
// cross-source collapsing: each source's own results are reported as their
// own group, in that source's own returned order, and groups themselves are
// ordered by search.sources registration order — never interleaved (this
// packet's own Binding decisions).
//
// A backend not implementing search (the wire-level unknown_op sentinel) is
// reported disabled/"not applicable", mirroring auth.go's/attention.go's own
// convention for the same case.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// SearchOutcome is "search"'s wire response: FanOutOutcome's sources[] row
// per backend actually queried (INV-OUT-1) — its Count carrying that
// source's own RAW search result-list length, since (unlike auth_status/
// capabilities) search has a natural per-call item count — plus Groups (the
// per-source, never-merged result groups) and Warnings (one entry per
// unrecognized --fields value). FanOutOutcome is embedded (promoting
// Sources and ExitCode()) rather than reimplemented, matching attention.go's
// AttentionOutcome.
type SearchOutcome struct {
	FanOutOutcome
	Groups   []SearchResultGroup `json:"groups"`
	Warnings []string            `json:"warnings,omitempty"`
}

// SearchResultGroup is one source's own, unmerged slice of search results:
// Results preserves exactly the order that source's own search response
// returned, never re-sorted or interleaved with another source's results.
type SearchResultGroup struct {
	Source  string                `json:"source"`
	Results []schema.SearchResult `json:"results"`
}

// searchOpArgs is the wire args shape for the "search" op —
// {"query": "...", "fields": [...]} — matching
// pkg/provider/search.NewDispatchTable's own decode target exactly.
type searchOpArgs struct {
	Query  string   `json:"query"`
	Fields []string `json:"fields"`
}

// fanOutSearch queries the search op against every backend in backends
// (search.sources' own config order), building one sources[] row per
// backend queried and returning each succeeding backend's raw,
// un-grouped-yet schema.SearchResult slice keyed by backend name for
// groupSearchResults to consume. A backend not implementing search (the
// wire-level unknown_op sentinel) is reported disabled/"not applicable"
// rather than a failure, mirroring fanOutAttentionList's/authStatusOne's
// own handling exactly.
func fanOutSearch(ctx context.Context, backends []string, query string, fields []string) (map[string][]schema.SearchResult, FanOutOutcome) {
	perSource := make(map[string][]schema.SearchResult, len(backends))
	// Sources starts as a non-nil empty slice so a zero-backend
	// (misconfigured host) result still marshals its sources[] field as
	// [] rather than null [bug A15].
	out := FanOutOutcome{Sources: make([]SourceResult, 0, len(backends))}
	for _, b := range backends {
		resp, err := scriptout.Invoke(ctx, b, "search", searchOpArgs{Query: query, Fields: fields})
		if err != nil {
			if errors.Is(err, scriptout.ErrUnknownOp) {
				out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDisabled, Reason: "not applicable"})
				continue
			}
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		var results []schema.SearchResult
		if err := scriptout.Decode(resp.Result, &results); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		perSource[b] = results
		// Count is that source's own raw result-list length — search has a
		// natural per-call item count, unlike auth_status/capabilities,
		// which is why SourceResult.Count MAY be 0 for those but MUST NOT
		// be a placeholder here (this packet's own Acceptance criteria).
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(results)})
	}
	return perSource, out
}

// groupSearchResults builds SearchOutcome's Groups from perSource (each
// queried source's raw search results, keyed by backend name — as returned
// by fanOutSearch) and sourceOrder (search.sources' own registration-order
// list; perSource's keys are always a subset of it, since a
// disabled/degraded source contributes no entry). Unlike
// mergeAttentionItems (attention.go), there is NO cross-source dedup/merge
// here — this packet's own Binding decisions are explicit that two
// different backends' results are never collapsed. Groups are ordered by
// sourceOrder (search.sources registration order); a source with no entry
// in perSource (disabled or degraded — its own health already lives in
// FanOutOutcome.Sources) contributes no group at all, rather than an empty
// placeholder one.
func groupSearchResults(perSource map[string][]schema.SearchResult, sourceOrder []string) []SearchResultGroup {
	// Non-nil even when empty so "groups" marshals as [] rather than null
	// [bug A15's convention, applied here].
	groups := make([]SearchResultGroup, 0, len(sourceOrder))
	for _, source := range sourceOrder {
		results, ok := perSource[source]
		if !ok {
			continue
		}
		groups = append(groups, SearchResultGroup{Source: source, Results: results})
	}
	return groups
}

// searchAttributesVocabularyKey is the capabilities.Vocabulary key a
// backend declares its own additional searchable attribute names under (a
// JSON array of strings). The design pins reuse of
// pkg/scriptout.CapabilitiesResponse.Vocabulary as the MECHANISM for a
// backend to declare its own search attributes the same way a backend's
// per-backend capability vocabulary is already declared, but leaves the
// specific key name to this packet's own implementer to choose and
// document — this is that choice (this packet's own Binding decisions), not
// re-derivable from anything else in this module.
const searchAttributesVocabularyKey = "search_attributes"

// coreSearchFieldNames is the fixed set of JSON keys schema.SearchResult
// itself always carries — the first member of the field-validation union
// validateSearchFields checks a requested --fields value against.
var coreSearchFieldNames = map[string]bool{
	"type":   true,
	"id":     true,
	"title":  true,
	"url":    true,
	"source": true,
}

// knownSearchFields builds the union of searchable attribute names this
// call recognizes as well-formed: the core set (coreSearchFieldNames), every
// entity type's own fixed schema-declared attribute (today the EMPTY set —
// no pkg/schema type declares one yet; that is each entity type's own
// future extension, explicitly out of scope for this packet — see this
// packet's own Binding decisions and Out of scope sections), and every
// actually-queried backend's own capabilities-declared vocabulary, read
// under searchAttributesVocabularyKey. A backend whose capabilities call
// itself fails, or that declares nothing under that key, simply contributes
// nothing extra to the union — capabilities lookup failures are never
// reported as their own warning or error here; they only affect which
// fields validateSearchFields recognizes.
func knownSearchFields(ctx context.Context, backends []string) map[string]bool {
	known := make(map[string]bool, len(coreSearchFieldNames))
	for f := range coreSearchFieldNames {
		known[f] = true
	}
	for _, b := range backends {
		resp, err := scriptout.InvokeCapabilities(ctx, b)
		if err != nil || resp == nil {
			continue
		}
		addVocabularyFieldNames(known, resp.Vocabulary[searchAttributesVocabularyKey])
	}
	return known
}

// addVocabularyFieldNames adds every string entry found in raw (the
// searchAttributesVocabularyKey value read out of a backend's own
// capabilities.Vocabulary map — expected to decode as either []string or
// the []any-of-string shape encoding/json produces for a map[string]any
// value) into known. Any other shape is silently ignored rather than
// treated as an error: a backend's own vocabulary declaration is advisory,
// never part of the wire protocol's own Response envelope contract.
func addVocabularyFieldNames(known map[string]bool, raw any) {
	switch v := raw.(type) {
	case []string:
		for _, s := range v {
			known[s] = true
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				known[s] = true
			}
		}
	}
}

// validateSearchFields checks fields (the caller's requested --fields
// values) against knownSearchFields' union, returning one warning entry per
// value that matches nothing in that union — never an error, and never a
// per-backend stderr line (this packet's own Contract/Acceptance criteria).
// A field present in the union produces no warning even when a specific
// queried backend's own result didn't populate it: this validates that the
// REQUEST is well-formed against the known universe of attribute names, not
// whether every backend actually populated it (this packet's own Binding
// decisions).
//
// An empty fields list skips validation entirely, producing no warnings and
// never calling knownSearchFields (this packet's own Freedom boundary: the
// design does not distinguish "skip validation" from "treat as core set
// only" for an empty request, and no backend exists yet to observe the
// difference — this packet's implementer's choice).
func validateSearchFields(ctx context.Context, backends []string, fields []string) []string {
	if len(fields) == 0 {
		return nil
	}
	known := knownSearchFields(ctx, backends)
	var warnings []string
	for _, f := range fields {
		if !known[f] {
			warnings = append(warnings, fmt.Sprintf(
				"field %q is not part of the core search result shape, any type's declared attributes, or any queried backend's declared vocabulary", f,
			))
		}
	}
	return warnings
}

const searchFieldsFlagName = "fields"

func newSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Fan search out across every registered search.sources backend, grouped by source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			backends, err := reg.SearchSources()
			if err != nil {
				return err
			}
			fields, err := cmd.Flags().GetStringSlice(searchFieldsFlagName)
			if err != nil {
				return err
			}
			perSource, fanOut := fanOutSearch(cmd.Context(), backends, args[0], fields)
			outcome := SearchOutcome{
				FanOutOutcome: fanOut,
				Groups:        groupSearchResults(perSource, backends),
				Warnings:      validateSearchFields(cmd.Context(), backends, fields),
			}
			return writeFanOutResult(cmd, outcome, outcome.ExitCode(), func() string {
				return humanizeSearch(outcome)
			})
		},
	}
	cmd.Flags().StringSlice(searchFieldsFlagName, nil, "request specific result attributes by name; an unrecognized one produces a warning, never an error")
	return cmd
}

// humanizeSearch formats a "search" fan-out outcome (its per-source Groups,
// any field-validation Warnings, plus its per-backend Sources rows) for
// human display, mirroring humanizeAttentionList's/humanizeCiList's own
// shape for the analogous fan-out.
func humanizeSearch(o SearchOutcome) string {
	var b strings.Builder
	if len(o.Groups) == 0 {
		b.WriteString("search results: (none)\n")
	} else {
		for _, g := range o.Groups {
			fmt.Fprintf(&b, "%s (%d):\n", g.Source, len(g.Results))
			for _, r := range g.Results {
				fmt.Fprintf(&b, "  [%s] %s: %s (%s)\n", r.Type, r.ID, r.Title, r.URL)
			}
		}
	}
	if len(o.Warnings) > 0 {
		b.WriteString("warnings:\n")
		for _, w := range o.Warnings {
			fmt.Fprintf(&b, "  - %s\n", w)
		}
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}
