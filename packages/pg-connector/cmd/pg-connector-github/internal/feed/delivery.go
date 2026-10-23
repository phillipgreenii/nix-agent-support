package feed

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The change words a delivered row carries for a key.
const (
	ChangeAdded   = shared.ChangeAdded
	ChangeRemoved = shared.ChangeRemoved
	ChangeChanged = shared.ChangeChanged
)

// registration is the GitHub registration the delivery rule runs under.
var registration = changes.Spec()

// viewOf returns the registered view named kind; a name that is not registered
// is a view without a kind filter, the PR feed's.
func viewOf(kind string) registry.View {
	for _, v := range registration.Views {
		if v.Name == kind {
			return v
		}
	}
	return registry.View{}
}

// DeliveryFor reports whether a log row reaches a PR-feed key of query, and which
// of "added", "removed" or "changed" it is for that key. member says whether the
// row's entity is a member of the query now. It is the shared Deliver under the
// GitHub registration.
func DeliveryFor(row store.ChangeRow, query string, member bool) (deliver bool, change string) {
	return DeliveryForKey(KindPR, row, query, member)
}

// DeliveryForKey is DeliveryFor for a key of kind: for the "ci" key only rows
// whose kinds include ci_changed (of a member), or a membership or removed kind
// naming the query, are delivered.
func DeliveryForKey(kind string, row store.ChangeRow, query string, member bool) (deliver bool, change string) {
	return shared.Deliver(registration, viewOf(kind), row, query, member)
}
