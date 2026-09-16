package interpret

// Ownership is the "can I act?" axis for a tracked PR — a closed 3-value set,
// ported from packages/pg-pr/internal/ownership.Ownership.
type Ownership string

const (
	OwnershipMine    Ownership = "mine"
	OwnershipCoOwned Ownership = "co-owned"
	OwnershipTeam    Ownership = "team"
)

// ActsAsMine reports whether consumers should treat this PR like the
// operator's own (true for Mine and CoOwned; false for Team). Ported from
// ownership.Ownership.ActsAsMine.
func (o Ownership) ActsAsMine() bool { return o == OwnershipMine || o == OwnershipCoOwned }

// classifyOwnership applies pg-pr's exact precedence: authored-by-self wins
// (Mine); else a self-authored commit on the PR's branch (CoOwned); else
// Team. Empty self => Team. Ported from ownership.Classify.
func classifyOwnership(self, prAuthor string, commitAuthors []string) Ownership {
	if self == "" {
		return OwnershipTeam
	}
	if prAuthor == self {
		return OwnershipMine
	}
	for _, a := range commitAuthors {
		if a != "" && a == self {
			return OwnershipCoOwned
		}
	}
	return OwnershipTeam
}
