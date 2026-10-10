package focus

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Key names one stored entity. Type is the store's entity type: "pr" or
// "issue".
type Key struct{ Type, ID string }

// Kind is the focus kind of a candidate: a PR, a Jira issue (an issue entity
// whose id is not a bead id) or a bead (an issue entity whose id matches the
// configured bead_id_pattern).
type Kind string

// The closed Kind set. Candidates are ordered by kind in this order (pr,
// jira, bead), then by key.
const (
	KindPR   Kind = "pr"
	KindJira Kind = "jira"
	KindBead Kind = "bead"
)

// order is the position of k in the candidate order.
func (k Kind) order() int {
	switch k {
	case KindPR:
		return 0
	case KindJira:
		return 1
	case KindBead:
		return 2
	}
	return 3
}

// Via says why a linked-only candidate is a candidate: the first seed joined
// to it (in kind, then key order), the relation of the link that joins them
// (the first relation by name when several do), and how many further seeds
// are joined to it.
type Via struct {
	SeedKey  Key
	Relation string
	More     int
}

// Candidate is one member of the candidate set. Seed is true for a seed;
// Via is set exactly for a linked-only candidate. An entity that is both a
// seed and linked appears once, as a seed, with no Via.
type Candidate struct {
	Key  Key
	Kind Kind
	Seed bool
	Via  *Via
}

// Notice codes.
const (
	// NoticeEmptyIdentityList: focus.operator_identities is empty, so no Jira
	// issue seeds.
	NoticeEmptyIdentityList = "empty-identity-list"
	// NoticeNoBeadCandidates: a bead pattern is set but no bead is a seed
	// (none carries PlanableLabel in an open, in_progress or blocked state).
	NoticeNoBeadCandidates = "no-bead-candidates"
	// NoticeNoBeadPattern: bead_id_pattern is unset, so no issue is a bead
	// and no bead seeds.
	NoticeNoBeadPattern = "no-bead-pattern"
)

// Notice is a condition the operator should see that is not a failure.
type Notice struct{ Code, Text string }

// SourceCounts are the per-source counts the show header prints. PR, Jira
// and Bead count the candidates of each kind, seeds and linked-only alike;
// Linked counts the linked-only candidates (those that arrived through a
// link). OpenBeads counts the eligible beads in an open, in_progress or
// blocked state, and UnlabelledOpenBeads those of them without PlanableLabel,
// so the header can say how many beads a label would add.
type SourceCounts struct{ PR, Jira, Bead, Linked, OpenBeads, UnlabelledOpenBeads int }

// CandidateSet is the answer of Candidates. Candidates are in the
// deterministic order: by kind (pr, jira, bead), then by entity type, then by
// id. A set can be empty; that is a valid result, not an error.
type CandidateSet struct {
	Candidates []Candidate
	Counts     SourceCounts
	Notices    []Notice
}

// MergeReason is the Reason of the external references link a --merge records
// from the kept entity to the absorbed one. Inputs.Absorbed is read from
// those links.
const MergeReason = "focus:merge"

// Inputs is everything the candidate computation reads, assembled by Load. It
// is a plain value: Candidates and ExplainCandidate read nothing else, so a
// test builds one directly.
type Inputs struct {
	Repo   string
	Now    time.Time
	Config *config.Config
	// Entities are the stored entity rows of Repo.
	Entities map[Key]store.Entity
	// Interps are the stored interpretation rows (the PR ownership).
	Interps map[Key]store.Interpretation
	// Annotations are the key/value annotations by entity.
	Annotations map[Key]map[string]string
	// Links is every stored link; both directions are derivable.
	Links []store.XrefLink
	// PlanKeys are the keys currently in the plan of any period (read-only
	// input: the candidate set decides who may ENTER a plan, never who stays).
	PlanKeys map[Key]bool
	// Absorbed maps an absorbed key to the key that absorbed it, read from the
	// external references links whose Reason is MergeReason.
	Absorbed map[Key]Key
}

// Explanation is ExplainCandidate's answer for one key. Reasons is set when
// the key is not a candidate and holds codes from the closed Reason list.
type Explanation struct {
	IsCandidate bool
	Seed        bool
	Reasons     []string
	Via         *Via
}

// The closed list of explanation reasons.
const (
	ReasonNotInStore      = "not-in-store"
	ReasonInactive        = "inactive"
	ReasonHidden          = "hidden"
	ReasonMintedBead      = "minted-bead"
	ReasonWorkItemBead    = "work-item-bead"
	ReasonDeferred        = "deferred"
	ReasonTerminal        = "terminal"
	ReasonAbsorbed        = "absorbed"
	ReasonNotSeedOrLinked = "not-seed-or-linked"
)

// PlanableLabel is the bead label that makes a bead a seed.
const PlanableLabel = "pg-focus-planable"

// The link relations that yield candidates. They are local constants because
// the constants of internal/links are unexported.
const (
	relationJira       = "jira"
	relationWork       = "work"
	relationParent     = "parent"
	relationMentions   = "mentions"
	relationDependsOn  = "depends_on"
	relationReferences = "references"
)

// externalOriginPrefix begins the origin of an externally managed link.
const externalOriginPrefix = "external:"

// Entity types and values the rules compare.
const (
	entityTypePR    = "pr"
	entityTypeIssue = "issue"
	ownershipMine   = "mine"
	ownershipCoOwn  = "co-owned"
	prStateOpen     = "open"
	statusOpen      = "open"
	statusInProg    = "in_progress"
	statusBlocked   = "blocked"
	statusDeferred  = "deferred"
)
