// Package registry is the vocabulary a connector registers with the shared
// change-feed code: the entity kind, the groups, the change kinds, the four
// roles the feed protocol itself needs, the views, and the policies that pick
// the emission and delivery behaviour. Spec is plain data plus one function
// and holds no behaviour of the shared code; the shared packages receive every
// kind as data and never compare to a concrete kind of their own.
package registry

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// Emission says which entities the change engine appends rows for.
type Emission int

const (
	// EmitAll appends a row for every tracked entity. It is the behaviour the
	// first adopter has before the lane refactor; a row carries the entity's
	// watched flag as it stood.
	EmitAll Emission = iota
	// EmitWatchedOnly appends a row only for an entity whose watched flag is set
	// in the same transaction (or is set by the same write).
	EmitWatchedOnly
)

// Delivery says how the feed selects the rows a consumer receives.
type Delivery int

const (
	// DeliverByMembership is the behaviour the first adopter has before the lane
	// refactor: a query name is required, a member's rows are delivered, and a
	// non-member's row only when a membership kind names the query.
	DeliverByMembership Delivery = iota
	// DeliverByWatched delivers the rows whose watched snapshot is set, whether
	// or not the entity is still a member of any query.
	DeliverByWatched
)

// Roles name the four change kinds the feed protocol itself needs. Added and
// Removed MUST be in Spec.Changes; EnteredQuery and LeftQuery MUST be in
// Spec.Changes only when Membership.StoreRows is true (a connector that stores
// no membership rows does not list them in its change catalog).
type Roles struct {
	Added        string // first observation, late watch (appended by the engine)
	Removed      string // the entity is gone
	EnteredQuery string // membership: arrival
	LeftQuery    string // membership: departure
}

// View is a named filter over one log. A consumer's cursor is bound to one view.
type View struct {
	Name string
	// AnyOf: when non-empty, a row of a member is delivered only if its kinds
	// include one of these. The Removed role and a membership kind that names
	// the consumer's query always pass.
	AnyOf []string
}

// MembershipPolicy says whether arrivals and departures are stored as log rows.
type MembershipPolicy struct{ StoreRows bool }

// The causes recorded in entity.watched_via.
const (
	ViaQuery  = "query:" // followed by the configured query name
	ViaDirect = "direct" // one read or lookup of one entity carried the watch option
	ViaAdHoc  = "adhoc:" // followed by the query name of an ad-hoc run carrying the watch option
)

// Spec is everything one connector tells the shared code about its vocabulary.
// It is plain data plus one function; it holds no behaviour of the shared code.
type Spec struct {
	// EntityKind is the entity.kind, change_log.kind and pending_change.kind of
	// the one entity type this daemon's feed serves.
	EntityKind string
	// PrimaryGroup is the group that describes the entity itself (its first
	// fetch is the entity's baseline). Groups lists every group name in
	// canonical order, PrimaryGroup included.
	PrimaryGroup string
	Groups       []string
	// Changes is every change kind the connector emits, in canonical order (the
	// order Merge writes a kind list in, and the label set of the kind metric).
	Changes []string
	Roles   Roles
	// Views: at least one; Views[0] is the default view.
	Views []View
	// FoldRef folds a wire reference to the form in which two references of one
	// entity compare equal. nil means the identity.
	FoldRef func(ref string) string

	Emission        Emission
	Delivery        Delivery
	Membership      MembershipPolicy
	AdHocQueryWatch bool
}

// namePattern is lower_snake_case: lower-case letters and digits, words
// joined by single underscores, starting with a letter.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// Validate reports the first thing wrong with s: an empty EntityKind, a Roles
// entry (see Roles) or View.AnyOf entry that is not in Changes, a duplicate or
// empty name, a name that is not lower_snake_case, a PrimaryGroup missing from
// Groups, no View.
func (s Spec) Validate() error {
	if s.EntityKind == "" {
		return errors.New("registry: EntityKind is empty")
	}
	if err := s.checkRoles(); err != nil {
		return err
	}
	for _, v := range s.Views {
		for _, k := range v.AnyOf {
			if !slices.Contains(s.Changes, k) {
				return fmt.Errorf("registry: view %q lists %q, which is not in Changes", v.Name, k)
			}
		}
	}
	lists := s.nameLists()
	for _, l := range lists {
		if err := checkDistinct(l.what, l.names); err != nil {
			return err
		}
	}
	for _, l := range lists {
		if err := checkSnake(l.what, l.names); err != nil {
			return err
		}
	}
	if !slices.Contains(s.Groups, s.PrimaryGroup) {
		return fmt.Errorf("registry: PrimaryGroup %q is not in Groups", s.PrimaryGroup)
	}
	if len(s.Views) == 0 {
		return errors.New("registry: no View")
	}
	return nil
}

// checkRoles reports the first role that must be in Changes and is not.
func (s Spec) checkRoles() error {
	required := []struct{ role, kind string }{
		{"Added", s.Roles.Added},
		{"Removed", s.Roles.Removed},
	}
	if s.Membership.StoreRows {
		required = append(
			required,
			struct{ role, kind string }{"EnteredQuery", s.Roles.EnteredQuery},
			struct{ role, kind string }{"LeftQuery", s.Roles.LeftQuery},
		)
	}
	for _, r := range required {
		if !slices.Contains(s.Changes, r.kind) {
			return fmt.Errorf("registry: role %s %q is not in Changes", r.role, r.kind)
		}
	}
	return nil
}

type nameList struct {
	what  string
	names []string
}

// nameLists is every list of names Validate checks, the entity kind first.
func (s Spec) nameLists() []nameList {
	views := make([]string, len(s.Views))
	for i, v := range s.Views {
		views[i] = v.Name
	}
	return []nameList{
		{"entity kind", []string{s.EntityKind}},
		{"group", s.Groups},
		{"change", s.Changes},
		{"view", views},
	}
}

// checkDistinct reports the first empty or repeated name of names; what says
// which list it is.
func checkDistinct(what string, names []string) error {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		switch {
		case n == "":
			return fmt.Errorf("registry: empty %s name", what)
		case seen[n]:
			return fmt.Errorf("registry: duplicate %s name %q", what, n)
		}
		seen[n] = true
	}
	return nil
}

// checkSnake reports the first name of names that is not lower_snake_case.
func checkSnake(what string, names []string) error {
	for _, n := range names {
		if !namePattern.MatchString(n) {
			return fmt.Errorf("registry: %s name %q is not lower_snake_case", what, n)
		}
	}
	return nil
}
