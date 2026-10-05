package workitem

import (
	"reflect"
	"testing"
)

// specTable is a literal copy of the spec 9.8 work-item contract table
// (docs/superpowers/specs/2026-09-29-entity-change-flow-design.md). The
// contract data in contract.go is compared against it, so drift in either
// direction fails.
var specTable = map[Kind]KindContract{
	KindAnchor: {
		IssueType:     "merge-request",
		TitleTemplate: "<repo>#<n>: <pr title>",
		Labels:        []string{"co-owned", "pbase:<n>"},
		MetadataKeys:  []string{"repo", "pr_number", "state", "branch", "base", "author", "url", "draft", "dedup_key"},
		Parent:        "",
	},
	KindProcessFeedback: {
		IssueType:     "task",
		TitleTemplate: "process-feedback: <repo>#<n>",
		Labels:        []string{"mine", "fbsum:<digest>"},
		MetadataKeys:  []string{"repo", "pr_number", "branch", "covered_comments", "dedup_key"},
		Parent:        "anchor",
	},
	KindReviewPR: {
		IssueType:     "task",
		TitleTemplate: "review-pr: <repo>#<n>",
		Labels:        nil,
		MetadataKeys:  []string{"repo", "pr_number", "branch", "head_sha", "ownership", "dedup_key"},
		Parent:        "anchor",
	},
	KindFixCI: {
		IssueType:     "task",
		TitleTemplate: "fix-ci: <repo>#<n>",
		Labels:        []string{"mine", "worker-ready"},
		MetadataKeys:  []string{"repo", "pr_number", "branch", "head_sha", "failing_checks", "failing_builds", "dedup_key"},
		Parent:        "anchor",
	},
	KindResolveConflict: {
		IssueType:     "task",
		TitleTemplate: "resolve-conflict: <repo>#<n>",
		Labels:        []string{"mine", "worker-ready"},
		MetadataKeys:  []string{"repo", "pr_number", "branch", "head_sha", "base", "base_sha", "dedup_key"},
		Parent:        "anchor",
	},
}

func TestKindsAreTheFiveExactStrings(t *testing.T) {
	want := []string{"anchor", "process-feedback", "review-pr", "fix-ci", "resolve-conflict"}
	var got []string
	for _, k := range Kinds() {
		got = append(got, string(k))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
}

func TestContractMatchesSpecTable(t *testing.T) {
	if len(Kinds()) != len(specTable) {
		t.Fatalf("kinds %d != spec rows %d", len(Kinds()), len(specTable))
	}
	for _, k := range Kinds() {
		want, ok := specTable[k]
		if !ok {
			t.Fatalf("kind %q not in spec table", k)
		}
		got := ContractFor(k)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ContractFor(%q) = %#v, want %#v", k, got, want)
		}
	}
}

func TestContractForReturnsCopies(t *testing.T) {
	c := ContractFor(KindFixCI)
	c.Labels[0] = "mutated"
	c.MetadataKeys[0] = "mutated"
	if got := ContractFor(KindFixCI); got.Labels[0] != "mine" || got.MetadataKeys[0] != "repo" {
		t.Fatalf("contract table mutated through returned value: %#v", got)
	}
}

func TestContractForUnknownKindIsZero(t *testing.T) {
	if got := ContractFor(Kind("nope")); !reflect.DeepEqual(got, KindContract{}) {
		t.Fatalf("got %#v", got)
	}
}

func TestEveryKindContractCarriesDedupKey(t *testing.T) {
	for _, k := range Kinds() {
		keys := ContractFor(k).MetadataKeys
		if keys[len(keys)-1] != "dedup_key" {
			t.Errorf("%s: dedup_key is not in the key list: %v", k, keys)
		}
	}
}
