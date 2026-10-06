package attention

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/dependency"

// NameBlockedByOpenDependency is the name the context suppressor records as
// the reason a candidate was dropped (shown by `pg-desk attention explain`).
const NameBlockedByOpenDependency = "blocked-by-open-dependency"

func init() {
	RegisterSuppressor(blockedByOpenDependency{})
}

// blockedByOpenDependency is the first context suppressor
// (docs/behavior/pg-desk/attention.md, "Suppress"): a candidate raised
// because the entity ITSELF is broken (Candidate.SelfBroken: CI failing, a
// merge conflict) is dropped while any PR the entity depends on is still
// open. A broken PR stacked on an open PR is not actionable yet, and the
// breakage usually clears when the base merges. Once the last dependency has
// merged (or closed) the suppressor no longer claims the candidate, so the
// candidate fires on the very next read: the dependency's own stored row is
// what changed, and the dependencies are re-derived at every read.
//
// Dependencies come from the dependency package's registry (the read-time
// stack and the operator-recorded external depends_on link); a shared Jira
// issue is a group, never a dependency. A dependency with no stored row is
// not open, so an unknown PR never holds a candidate back.
//
// Candidates that wait on someone else (a review request, approved and ready
// to land) are never claimed, and neither is any entity type but a PR.
type blockedByOpenDependency struct{}

func (blockedByOpenDependency) Name() string { return NameBlockedByOpenDependency }

func (blockedByOpenDependency) Suppress(c Candidate, env *SuppressEnv) (bool, error) {
	if !c.SelfBroken || c.Type != dependency.TypePR || env == nil || env.Dependencies == nil {
		return false, nil
	}
	open, err := env.Dependencies.OpenDependenciesOf(c.Type, c.ID)
	if err != nil {
		return false, err
	}
	return len(open) > 0, nil
}
