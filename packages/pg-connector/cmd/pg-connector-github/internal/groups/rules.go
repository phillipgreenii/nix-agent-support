package groups

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Stamps are the head and base commits a stamped group's content describes
// (headRefOid and baseRefOid).
type Stamps struct {
	Head, Base string
}

// StampsOf reads the head and base stamps from a summary.
func StampsOf(s summary.Summary) Stamps {
	return Stamps{Head: s.HeadRefOid, Base: s.BaseRefOid}
}

// Fingerprint is the sha256 of the bytes in hex, for groups whose fingerprint
// is the whole content. A group packet may supply its own.
func Fingerprint(content []byte) string { return groupkit.Fingerprint(content) }

// Level is how fresh a stored group is.
type Level = groupkit.Level

const (
	// LevelFresh: serve the stored content.
	LevelFresh = groupkit.LevelFresh
	// LevelStale: past its time window, or fetched for another head or base, or
	// empty; refresh it (serving it meanwhile is the caller's rule).
	LevelStale = groupkit.LevelStale
	// LevelHardStale: past the maximum age; the content MUST be re-pulled before
	// it is served as current.
	LevelHardStale = groupkit.LevelHardStale
)

// Freshness is the verdict of Rules.Fresh.
type Freshness = groupkit.Freshness

// Rules encodes the spec's "Field groups" freshness rules and "Defaults"
// table over a groupkit.Table built from the daemon's settings. It holds no
// clock: every method takes the time it is to judge by.
type Rules struct {
	tbl groupkit.Table
}

// NewRules builds the rules from the daemon's settings. The runs group's TTL is
// ci_active_ttl, the shortest of the content-dependent TTLs: the runs group
// applies ci_active_ttl and ci_failed_ttl itself.
func NewRules(s config.Settings) *Rules {
	return &Rules{tbl: groupkit.Table{
		TerminalTTL: s.TerminalTTL.Std(),
		Groups: []groupkit.GroupSpec{
			{Name: Summary, TTL: s.SummaryTTL.Std(), Background: true},
			{Name: Detail, TTL: s.DetailTTL.Std(), MaxAge: s.DetailMaxAge.Std(), Extendable: true, Background: true},
			{Name: Conversation, TTL: s.ConversationTTL.Std(), MaxAge: s.ConversationMaxAge.Std(), Extendable: true, Background: true},
			{Name: Files, Stamped: true, Background: true},
			{Name: Commits, Stamped: true, Background: true},
			{Name: Pending, TTL: s.PendingTTL.Std()},
			{Name: Runs, TTL: s.CIActiveTTL.Std(), MaxAge: s.CIMaxAge.Std(), Stamped: true, Background: true},
		},
	}}
}

// Table is the shared freshness table the rules are built on.
func (r *Rules) Table() groupkit.Table { return r.tbl }

// TTL is the group's time to live after a fetch; zero means no time limit (the
// stamped files and commits groups). For runs it is ci_active_ttl. An unknown
// group has none.
func (r *Rules) TTL(group string) time.Duration { return r.tbl.TTL(group) }

// MaxAge is the group's hard maximum age after a fetch; zero means none.
func (r *Rules) MaxAge(group string) time.Duration { return r.tbl.MaxAge(group) }

// Schedule returns the fresh_until and max_age_at of a group fetched at
// fetchedAt. For a PR in a terminal state every group is fresh until
// fetchedAt + terminal_ttl and has no maximum age. Otherwise fresh_until is
// fetchedAt + TTL(group) (zero when the group has no TTL: files and commits,
// which are fresh while their stamps match) and max_age_at is
// fetchedAt + MaxAge(group) (zero when none).
func (r *Rules) Schedule(group string, terminal bool, fetchedAt time.Time) (freshUntil, maxAgeAt time.Time) {
	return r.tbl.Schedule(group, terminal, fetchedAt)
}

// Extend is the rule revalidation uses for a detail or conversation row whose
// content GitHub confirmed unchanged: fresh_until becomes
// min(max_age_at, now + ttl). ok is false when the row is already at its
// maximum age, when its MaxAgeAt is zero, and for any group that is not
// extendable (only detail and conversation are); the caller then re-pulls the
// group. Extend is never called for a terminal PR, whose fresh_until is already
// terminal_ttl.
func (r *Rules) Extend(row store.GroupRow, group string, now time.Time) (freshUntil time.Time, ok bool) {
	return r.tbl.Extend(row, group, now)
}

// Background reports whether the group is refreshed by the background cycle.
// The pending group is never refreshed in the background; it is read through
// only when asked for. A group the table does not list is refreshed.
func (r *Rules) Background(group string) bool {
	spec, listed := r.tbl.Spec(group)
	return !listed || spec.Background
}

// Stamped reports whether the group's rows record the head and base they were
// fetched for and are fresh only while those match the current summary.
func (r *Rules) Stamped(group string) bool { return r.tbl.Stamped(group) }

// Fresh judges a stored row against the current summary's stamps at now. An
// empty row (no content, as an invalidation of a never-fetched group leaves) is
// never served and is stale. A row at or past max_age_at is hard-stale. A
// stamped group fetched for another head or base is stale even inside its time
// window. Otherwise the row is fresh while now is before fresh_until; a zero
// fresh_until means "no time limit" for a stamped group and "unknown, stale"
// for any other.
func (r *Rules) Fresh(row store.GroupRow, cur Stamps, now time.Time) Freshness {
	return r.tbl.Fresh(row, groupkit.Stamps{Primary: cur.Head, Secondary: cur.Base}, now)
}
