package registry

import (
	"strings"
	"testing"
)

// valid returns a registration of invented names that passes Validate; each
// case of the table breaks one thing about it.
func valid() Spec {
	return Spec{
		EntityKind:   "example_item",
		PrimaryGroup: "core",
		Groups:       []string{"core", "extra"},
		Changes:      []string{"alpha", "beta", "gone", "first", "enter_set", "leave_set"},
		Roles:        Roles{Added: "first", Removed: "gone", EnteredQuery: "enter_set", LeftQuery: "leave_set"},
		Views:        []View{{Name: "everything"}, {Name: "narrow", AnyOf: []string{"alpha"}}},
		Membership:   MembershipPolicy{StoreRows: true},
	}
}

func TestValidateAcceptsAWellFormedSpec(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate = %v", err)
	}
}

func TestValidateReportsTheFirstProblem(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Spec)
		want   string
	}{
		{"empty entity kind", func(s *Spec) { s.EntityKind = "" }, "EntityKind is empty"},
		{"added role outside Changes", func(s *Spec) { s.Roles.Added = "elsewhere" }, "role Added"},
		{"removed role outside Changes", func(s *Spec) { s.Roles.Removed = "" }, "role Removed"},
		{"arrival role outside Changes", func(s *Spec) { s.Roles.EnteredQuery = "elsewhere" }, "role EnteredQuery"},
		{"departure role outside Changes", func(s *Spec) { s.Roles.LeftQuery = "elsewhere" }, "role LeftQuery"},
		{"view entry outside Changes", func(s *Spec) { s.Views[1].AnyOf = []string{"elsewhere"} }, "not in Changes"},
		{"duplicate group", func(s *Spec) { s.Groups = []string{"core", "core"} }, "duplicate group"},
		{"duplicate change", func(s *Spec) { s.Changes = append(s.Changes, "alpha") }, "duplicate change"},
		{"duplicate view", func(s *Spec) { s.Views = append(s.Views, View{Name: "narrow"}) }, "duplicate view"},
		{"empty group name", func(s *Spec) { s.Groups = []string{"core", ""} }, "empty group"},
		{"empty change name", func(s *Spec) { s.Changes = append(s.Changes, "") }, "empty change"},
		{"empty view name", func(s *Spec) { s.Views = append(s.Views, View{}) }, "empty view"},
		{"entity kind not snake case", func(s *Spec) { s.EntityKind = "ExampleItem" }, "lower_snake_case"},
		{"group not snake case", func(s *Spec) { s.Groups = []string{"core", "Extra-Group"} }, "lower_snake_case"},
		{"change not snake case", func(s *Spec) { s.Changes = append(s.Changes, "double__underscore") }, "lower_snake_case"},
		{"view not snake case", func(s *Spec) { s.Views[0].Name = "1st" }, "lower_snake_case"},
		{"missing primary group", func(s *Spec) { s.PrimaryGroup = "absent" }, "PrimaryGroup"},
		{"no view", func(s *Spec) { s.Views = nil }, "no View"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.mutate(&s)
			err := s.Validate()
			if err == nil {
				t.Fatal("Validate accepted a broken spec")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateRequiresMembershipRolesOnlyWhenRowsAreStored(t *testing.T) {
	s := valid()
	s.Membership.StoreRows = false
	s.Changes = []string{"alpha", "beta", "gone", "first"}
	s.Roles.EnteredQuery, s.Roles.LeftQuery = "", ""
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate with no stored membership rows = %v", err)
	}
	s.Membership.StoreRows = true
	if err := s.Validate(); err == nil {
		t.Fatal("Validate accepted stored membership rows with no membership kinds")
	}
}

func TestValidateReportsProblemsInTheDocumentedOrder(t *testing.T) {
	// An empty entity kind outranks a role problem, which outranks a duplicate
	// name, which outranks a missing primary group, which outranks no view.
	s := valid()
	s.EntityKind = ""
	s.Roles.Added = "elsewhere"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "EntityKind") {
		t.Fatalf("Validate = %v, want the entity kind first", err)
	}
	s = valid()
	s.Roles.Added = "elsewhere"
	s.Groups = []string{"core", "core"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "role Added") {
		t.Fatalf("Validate = %v, want the role first", err)
	}
	s = valid()
	s.Groups = []string{"core", "core"}
	s.PrimaryGroup = "absent"
	s.Views = nil
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate group") {
		t.Fatalf("Validate = %v, want the duplicate first", err)
	}
	s = valid()
	s.PrimaryGroup = "absent"
	s.Views = nil
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "PrimaryGroup") {
		t.Fatalf("Validate = %v, want the primary group first", err)
	}
}
