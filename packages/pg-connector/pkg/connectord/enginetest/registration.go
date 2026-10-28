// Package enginetest is the neutral rig of the shared connector-daemon tests: a
// fake connector made of invented names, with a scripted lister and classifier,
// and the assertions of the tests that run the shared engine, query runner and
// feed together.
//
// The rig knows nothing of a real origin. Its entity kinds are alpha and beta,
// its change kinds retitled, toggled and tweaked, its roles added, gone, joined
// and left; another connector daemon reuses it in its conformance tests the same
// way the shared packages' tests do. Every name goes through vocab.W, so a test set built
// on the rig also runs over a second registration of different invented words
// (see vocab).
//
// A Registration is the registry.Spec plus the names that built it. A Rig wires
// the shared engine, query runner and feed over one real store, a manual clock
// and the scripted fakes of fakes.go. The options of NewRegistration override the
// Spec fields a test varies: the emission and delivery policy, whether membership
// rows are stored, and ad-hoc query watching.
package enginetest

import (
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/enginetest/vocab"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Registration is the vocabulary of the fake connector: the registry.Spec and the
// invented names it is made of, so a test refers to a name by field and never by
// literal.
type Registration struct {
	Spec registry.Spec

	// Entity is the entity kind (alpha by default, beta with WithBeta).
	Entity string
	// Extra is the one non-primary group; Primary the primary group (the store's).
	Extra, Primary string
	// Retitled, Toggled and Tweaked are the change kinds the scripted classifier
	// produces: a changed title, a flipped flag and a changed extra group.
	Retitled, Toggled, Tweaked string
	// Added, Removed, Entered and Left are the four roles.
	Added, Removed, Entered, Left string
	// ViewAll delivers every row; ViewToggled only rows carrying the Toggled kind.
	ViewAll, ViewToggled string
}

// Option overrides one thing of a registration.
type Option func(*Registration)

// WithEmission sets Spec.Emission.
func WithEmission(e registry.Emission) Option {
	return func(r *Registration) { r.Spec.Emission = e }
}

// WithDelivery sets Spec.Delivery.
func WithDelivery(d registry.Delivery) Option {
	return func(r *Registration) { r.Spec.Delivery = d }
}

// WithStoreRows sets Spec.Membership.StoreRows. With false the arrival and
// departure kinds are left out of Spec.Changes, as the registry requires of a
// connector that stores no membership rows.
func WithStoreRows(storeRows bool) Option {
	return func(r *Registration) {
		r.Spec.Membership.StoreRows = storeRows
		r.Spec.Changes = changeKinds(r, storeRows)
	}
}

// WithAdHocQueryWatch sets Spec.AdHocQueryWatch.
func WithAdHocQueryWatch(on bool) Option {
	return func(r *Registration) { r.Spec.AdHocQueryWatch = on }
}

// WithBeta registers the entity kind beta instead of alpha.
func WithBeta() Option {
	return func(r *Registration) {
		r.Entity = vocab.W("beta")
		r.Spec.EntityKind = r.Entity
	}
}

// WithSpec applies fn to the Spec last, for a test that varies something no
// other option does.
func WithSpec(fn func(*registry.Spec)) Option {
	return func(r *Registration) { fn(&r.Spec) }
}

// NewRegistration returns the fake connector's registration with every option
// applied in order. The default delivers by watched, emits for watched entities
// only, stores membership rows and does not watch ad-hoc queries.
func NewRegistration(opts ...Option) Registration {
	r := Registration{
		Entity:      vocab.W("alpha"),
		Primary:     store.SummaryGroupName,
		Extra:       vocab.W("extra"),
		Retitled:    vocab.W("retitled"),
		Toggled:     vocab.W("toggled"),
		Tweaked:     vocab.W("tweaked"),
		Added:       vocab.W("added"),
		Removed:     vocab.W("gone"),
		Entered:     vocab.W("joined"),
		Left:        vocab.W("left"),
		ViewAll:     vocab.W("all"),
		ViewToggled: vocab.W("flagged"),
	}
	r.Spec = registry.Spec{
		EntityKind:   r.Entity,
		PrimaryGroup: r.Primary,
		Groups:       []string{r.Primary, r.Extra},
		Changes:      changeKinds(&r, true),
		Roles:        registry.Roles{Added: r.Added, Removed: r.Removed, EnteredQuery: r.Entered, LeftQuery: r.Left},
		Views:        []registry.View{{Name: r.ViewAll}, {Name: r.ViewToggled, AnyOf: []string{r.Toggled}}},
		FoldRef:      strings.ToLower,
		Emission:     registry.EmitWatchedOnly,
		Delivery:     registry.DeliverByWatched,
		Membership:   registry.MembershipPolicy{StoreRows: true},
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

// changeKinds is Spec.Changes in canonical order, with the membership kinds only
// when storeRows is set.
func changeKinds(r *Registration, storeRows bool) []string {
	kinds := []string{r.Added, r.Removed}
	if storeRows {
		kinds = append(kinds, r.Entered, r.Left)
	}
	return append(kinds, r.Retitled, r.Toggled, r.Tweaked)
}
