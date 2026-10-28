package changes

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

// LogKindIssue is the kind of the change rows and pending rows of an issue.
const LogKindIssue = groups.EntityKind

// Spec is the Jira daemon's registration with the shared change engine.
//
// Emission and Delivery are the watched modes (the only modes, and so the zero
// values): a Jira row exists only for a watched issue. The membership words are
// roles only (StoreRows is false, a permanent per-connector policy), so
// entered_query and left_query are not in Changes.
func Spec() registry.Spec {
	return registry.Spec{
		EntityKind:   LogKindIssue,
		PrimaryGroup: groups.Summary,
		Groups:       groups.All(),
		Changes:      changeKinds(),
		Roles: registry.Roles{
			Added: classify.KindAdded, Removed: classify.KindRemoved,
			EnteredQuery: classify.KindEnteredQuery, LeftQuery: classify.KindLeftQuery,
		},
		Views:           []registry.View{{Name: "issue"}},
		FoldRef:         groups.FoldRef,
		Emission:        registry.EmitWatchedOnly,
		Delivery:        registry.DeliverByWatched,
		Membership:      registry.MembershipPolicy{StoreRows: false},
		AdHocQueryWatch: false,
	}
}

// changeKinds is classify.Kinds without the two membership words, followed by the
// first-observation kind.
func changeKinds() []string {
	var out []string
	for _, k := range classify.Kinds() {
		if k == classify.KindEnteredQuery || k == classify.KindLeftQuery {
			continue
		}
		out = append(out, k)
	}
	return append(out, classify.KindAdded)
}
