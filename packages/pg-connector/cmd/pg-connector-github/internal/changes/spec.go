package changes

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

// LogKindPR is the kind of the change rows and pending rows of a pull request.
const LogKindPR = "pr"

// Spec is the GitHub daemon's registration with the shared change engine.
// Emission and Delivery are the legacy modes until the PR lane refactor flips
// them.
func Spec() registry.Spec {
	return registry.Spec{
		EntityKind:   LogKindPR,
		PrimaryGroup: groups.Summary,
		Groups:       groups.All(),
		Changes:      append(classify.Kinds(), "added"),
		Roles: registry.Roles{
			Added: "added", Removed: classify.KindRemoved,
			EnteredQuery: classify.KindEnteredQuery, LeftQuery: classify.KindLeftQuery,
		},
		Views: []registry.View{
			{Name: "pr"},
			{Name: "ci", AnyOf: []string{classify.KindCIChanged}},
		},
		FoldRef:         foldOwnerRepo,
		Emission:        registry.EmitAll,
		Delivery:        registry.DeliverByMembership,
		Membership:      registry.MembershipPolicy{StoreRows: true},
		AdHocQueryWatch: true,
	}
}

// foldOwnerRepo is a wire reference with owner/repo folded to lower case and the
// number kept exactly: the one fold two references of one pull request compare
// equal under. It is reached through Spec().FoldRef and is the leaf's fold.
func foldOwnerRepo(ref string) string { return groups.FoldRef(ref) }
