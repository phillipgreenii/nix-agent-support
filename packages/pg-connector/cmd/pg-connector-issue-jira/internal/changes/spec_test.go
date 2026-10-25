package changes

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

func TestSpecIsValidAndRegistersWatchedOnlyEmission(t *testing.T) {
	s := Spec()
	if err := s.Validate(); err != nil {
		t.Fatalf("Spec().Validate() = %v", err)
	}
	if s.Emission != registry.EmitWatchedOnly {
		t.Errorf("Emission = %v, want EmitWatchedOnly", s.Emission)
	}
	if s.Delivery != registry.DeliverByWatched {
		t.Errorf("Delivery = %v, want DeliverByWatched", s.Delivery)
	}
	if s.Membership.StoreRows || s.AdHocQueryWatch {
		t.Errorf("membership = %+v, ad hoc watch = %v, want neither", s.Membership, s.AdHocQueryWatch)
	}
}

func TestSpecNamesTheIssueEntityItsGroupsViewsAndRoles(t *testing.T) {
	s := Spec()
	if s.EntityKind != "issue" || LogKindIssue != "issue" {
		t.Errorf("entity kind = %q, LogKindIssue = %q", s.EntityKind, LogKindIssue)
	}
	if s.PrimaryGroup != "summary" || !slices.Equal(s.Groups, []string{"summary", "detail"}) {
		t.Errorf("primary group = %q, groups = %v", s.PrimaryGroup, s.Groups)
	}
	if len(s.Views) != 1 || s.Views[0].Name != "issue" || len(s.Views[0].AnyOf) != 0 {
		t.Errorf("views = %+v", s.Views)
	}
	want := registry.Roles{Added: "added", Removed: "removed", EnteredQuery: "entered_query", LeftQuery: "left_query"}
	if s.Roles != want {
		t.Errorf("roles = %+v, want %+v", s.Roles, want)
	}
}

func TestSpecChangesAreTheKindsWithoutMembershipWordsThenAdded(t *testing.T) {
	var want []string
	for _, k := range classify.Kinds() {
		if k != "entered_query" && k != "left_query" {
			want = append(want, k)
		}
	}
	want = append(want, "added")
	got := Spec().Changes
	if !slices.Equal(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	for _, k := range []string{"entered_query", "left_query"} {
		if slices.Contains(got, k) {
			t.Errorf("changes lists the membership word %q", k)
		}
	}
	if got[len(got)-1] != "added" {
		t.Errorf("last change = %q, want added", got[len(got)-1])
	}
}

func TestSpecFoldRefUpperCases(t *testing.T) {
	fold := Spec().FoldRef
	if fold("proj-12") != fold("PROJ-12") || fold("proj-12") != "PROJ-12" {
		t.Errorf("fold(proj-12) = %q, fold(PROJ-12) = %q", fold("proj-12"), fold("PROJ-12"))
	}
	if fold("PROJ-12") == fold("PROJ-13") {
		t.Error("the number was folded away")
	}
	if fold("PROJ-12") != groups.FoldRef("PROJ-12") {
		t.Error("the fold is not groups.FoldRef")
	}
}

func TestSpecReturnsAFreshValueEachCall(t *testing.T) {
	a := Spec()
	a.Changes[0] = "mutated"
	if Spec().Changes[0] == "mutated" {
		t.Error("Spec shares its Changes slice between calls")
	}
}
