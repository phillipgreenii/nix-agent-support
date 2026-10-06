package classify

// Field-coverage test for the pr classifier (ADR S35, design 10.3).
//
// The cheap PR list carries a fixed set of fields, schema.PRListFields; the
// entity-change flow decides "did this PR change" from a fingerprint of that
// list entry alone. A field the classifier reads that the list does NOT carry
// is invisible to that cheap check: a change to it only surfaces when a slower
// refresh tier re-hydrates the PR. This test makes that gap explicit and
// reviewable. Every field the pr classifier reads from a pr_show snapshot MUST
// be either
//
//   - in schema.PRListFields (part of the list fingerprint), or
//   - on the prBlindSpots declaration below, naming the refresh tier that
//     eventually sees it.
//
// A new classifier read that is in neither set fails prFieldCoverageProblems,
// and so does a blind-spot entry with no (or an unknown) tier, an entry that
// is no schema.PR field, and an entry that is already in the list fingerprint
// (a stale blind spot that would hide the fingerprint taking the field over).
//
// How "fields a consumer reads" is enumerated (the design leaves this to the
// implementer): reflection over prShowFields, the struct pr.go decodes
// pr_show into. It is the classifier's complete read-set by construction, so
// adding a field to it without declaring its coverage goes red with no
// separate list to forget. Scope is the pr classifier only. Deciders are a
// separate binary built in another phase and are not covered here. The
// non-pr_show inputs (the CI fan-out under Facts.ci, Facts.head_sha as the
// head fallback, Facts.degraded) are Facts-level, not schema.PR fields, and
// are likewise out of this test's scope.
//
// pg-desk production code deliberately does not import pkg/schema; this
// test-only import of the connector schema package is intended (cmd/pg-desk's
// links_test.go is the precedent).

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Refresh tier names, literal strings owned by this declaration. The tiers
// themselves are implemented elsewhere; nothing here depends on that code.
const (
	// tierReconcile is the local reconcile tier (default reconcile_age 30m).
	tierReconcile = "reconcile"
	// tierRemote is the remote re-hydration tier (default sweep.max_age 6h).
	tierRemote = "remote"
)

// prBlindSpot declares one PR field the cheap list cannot see and the refresh
// tier that eventually does.
type prBlindSpot struct {
	Field string // schema.PR JSON field name
	Tier  string // tierReconcile or tierRemote
	Why   string // what the list cannot see
}

// prBlindSpots is the committed record of what the cheap PR list cannot see.
// Adding a classifier read that the list does not carry means adding it here,
// with a tier, in the same change.
var prBlindSpots = []prBlindSpot{
	{"base", tierReconcile, "base branch retarget is not in the list selection"},
	{"merged", tierReconcile, "the merged flag is not selected; state alone is in the list"},
	{"merge_state_status", tierReconcile, "mergeStateStatus is not in the list selection (the classifier deliberately ignores it, see pr.go)"},
	{"review_requests", tierReconcile, "reviewRequests detail is not in the list selection (not read by the classifier today)"},
	{"comments", tierRemote, "comment bodies, edits and per-comment resolved flags are only in the full show; the list sees counts"},
	{"reviews", tierRemote, "review ids/states and review-thread detail are only in the full show; the list sees counts"},
}

// prReadFields returns the JSON field names of the pr_show fields the
// classifier decodes (and therefore reads), derived from prShowFields.
func prReadFields() []string {
	t := reflect.TypeOf(prShowFields{})
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// schemaPRFields returns the JSON field names of schema.PR.
func schemaPRFields() map[string]bool {
	t := reflect.TypeOf(schema.PR{})
	out := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// prFieldCoverageProblems checks a read-set against the list fingerprint set
// and a blind-spot declaration, returning one message per violation (empty
// when the declaration is sound). Pure, so the red cases can be exercised.
func prFieldCoverageProblems(read, listFields []string, blind []prBlindSpot, schemaFields map[string]bool) []string {
	inList := make(map[string]bool, len(listFields))
	for _, f := range listFields {
		inList[f] = true
	}
	blindByField := make(map[string]prBlindSpot, len(blind))
	var problems []string
	for _, b := range blind {
		if _, dup := blindByField[b.Field]; dup {
			problems = append(problems, "blind spot "+b.Field+": declared more than once")
		}
		blindByField[b.Field] = b
		switch b.Tier {
		case tierReconcile, tierRemote:
		case "":
			problems = append(problems, "blind spot "+b.Field+": names no refresh tier")
		default:
			problems = append(problems, "blind spot "+b.Field+": unknown refresh tier "+b.Tier)
		}
		if !schemaFields[b.Field] {
			problems = append(problems, "blind spot "+b.Field+": not a schema.PR field")
		}
		if inList[b.Field] {
			problems = append(problems, "blind spot "+b.Field+": is already in schema.PRListFields, drop the blind-spot entry")
		}
	}
	for _, f := range read {
		if !schemaFields[f] {
			problems = append(problems, "read field "+f+": not a schema.PR field")
		}
		if !inList[f] {
			if _, ok := blindByField[f]; !ok {
				problems = append(problems, "read field "+f+": in neither schema.PRListFields nor the blind-spot list; add it to prBlindSpots with a refresh tier")
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// TestPRFieldCoverage_Classifier is the REQUIRED gate: every pr_show field the
// pr classifier reads is in the list fingerprint or a tiered blind spot.
func TestPRFieldCoverage_Classifier(t *testing.T) {
	read := prReadFields()
	if len(read) == 0 {
		t.Fatal("prShowFields yielded no JSON fields; the reflection is broken")
	}
	for _, p := range prFieldCoverageProblems(read, schema.PRListFields, prBlindSpots, schemaPRFields()) {
		t.Error(p)
	}
}

// TestPRFieldCoverage_DetectsGaps proves the check goes red on each failure
// mode, so the gate cannot pass vacuously.
func TestPRFieldCoverage_DetectsGaps(t *testing.T) {
	schemaFields := schemaPRFields()
	list := []string{"state", "draft"}
	good := []prBlindSpot{{"base", tierReconcile, "x"}}

	cases := []struct {
		name  string
		read  []string
		blind []prBlindSpot
		want  string
	}{
		{"read field in neither set", []string{"state", "base", "head_sha_missing_from_both"}, good, "head_sha_missing_from_both"},
		{"read field removed from blind list", []string{"state", "base"}, nil, "read field base: in neither"},
		{"blind spot with no tier", []string{"base"}, []prBlindSpot{{"base", "", "x"}}, "names no refresh tier"},
		{"blind spot with unknown tier", []string{"base"}, []prBlindSpot{{"base", "daily", "x"}}, "unknown refresh tier"},
		{"blind spot not a schema field", []string{"base"}, []prBlindSpot{{"base", tierRemote, "x"}, {"bogus_field", tierRemote, "x"}}, "bogus_field: not a schema.PR field"},
		{"blind spot already in the list", []string{"state"}, []prBlindSpot{{"state", tierRemote, "x"}}, "already in schema.PRListFields"},
		{"duplicate blind spot", []string{"base"}, []prBlindSpot{{"base", tierRemote, "x"}, {"base", tierRemote, "x"}}, "declared more than once"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := prFieldCoverageProblems(c.read, list, c.blind, schemaFields)
			for _, g := range got {
				if strings.Contains(g, c.want) {
					return
				}
			}
			t.Fatalf("want a problem containing %q, got %v", c.want, got)
		})
	}

	if got := prFieldCoverageProblems([]string{"state", "base"}, list, good, schemaFields); len(got) != 0 {
		t.Fatalf("a sound declaration reported problems: %v", got)
	}
}

// TestPRFieldCoverage_NamedBlindSpots pins the blind spots the design names
// (ADR S35, 4 Q8): the list cannot see mergeStateStatus or reviewRequests, and
// each must carry a tier.
func TestPRFieldCoverage_NamedBlindSpots(t *testing.T) {
	byField := map[string]string{}
	for _, b := range prBlindSpots {
		byField[b.Field] = b.Tier
	}
	for _, f := range []string{"merge_state_status", "review_requests", "comments", "reviews"} {
		if byField[f] == "" {
			t.Errorf("%s must be a blind spot with a named refresh tier", f)
		}
	}
}
