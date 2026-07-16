// Package ownership classifies a tracked PR on the single "can I act on this
// PR?" axis. Ownership is a CLOSED 3-value set — it is deliberately NOT where
// engagement/tier signals (why a PR is in my set, how urgent) live; pg2-4dz88
// layers those as a separate classifier over the same Engagement facts.
package ownership

// Ownership is the "can I act?" axis for a tracked PR.
type Ownership string

const (
	Mine    Ownership = "mine"
	CoOwned Ownership = "co-owned"
	Team    Ownership = "team"
)

// Engagement is the set of PR facts Classify reads. It is the growth point:
// pg2-4dz88 adds signals (MyReviewSubmitted, AssignedToMe, ICommented, …) here
// without changing call sites. CommitAuthors is the per-commit author logins
// observed this tick; nil/empty (enrichment absent) degrades to authorship-only.
type Engagement struct {
	Self          string
	PRAuthor      string
	CommitAuthors []string
}

// Classify applies precedence: authored-by-self => Mine (always wins); else a
// self-authored commit => CoOwned; else Team. Empty Self => Team.
func Classify(e Engagement) Ownership {
	if e.Self == "" {
		return Team
	}
	if e.PRAuthor == e.Self {
		return Mine
	}
	for _, a := range e.CommitAuthors {
		if a != "" && a == e.Self {
			return CoOwned
		}
	}
	return Team
}

// ActsAsMine reports whether store consumers should treat this PR like my own
// (dashboard Mine panel, reply-posting, mine-style review, no team attention).
// True for Mine and CoOwned; false for Team.
func (o Ownership) ActsAsMine() bool { return o == Mine || o == CoOwned }

// String returns the store/payload string value.
func (o Ownership) String() string { return string(o) }
