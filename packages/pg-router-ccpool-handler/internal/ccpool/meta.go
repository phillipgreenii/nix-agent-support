package ccpool

import (
	"strconv"
	"time"
)

// pg-router's session-metadata key namespace. Keys are PREFIXED (pgrouter.*) because
// they live in a KV store shared with ccpool and any other consumer; the prefix
// prevents collision with a key ccpool or another writer might use. (Design:
// docs/superpowers/specs/2026-06-24-session-metadata-at-dispatch-design.md.)
const (
	MetaKeyBead = "pgrouter.bead" // the bead id the session is working
	MetaKeyRole = "pgrouter.role" // the pg-router role name
	MetaKeyPool = "pgrouter.pool" // owner tag; always PoolName

	// MetaKeyLeaseUntil is the supervision lease (bead pg2-g2u9m, INV-CCH-18):
	// an RFC3339 UTC instant until which a live handler vouches that it is
	// supervising the session. The dispatching handler stamps it at launch
	// (covering the launch wait plus one TTL) and refreshes it every poll; a
	// lease in the past means nobody is supervising the session any more. A
	// session with NO lease (launched by an older build) is never an orphan.
	// NEVER passed as a ccpool --label: a per-poll value would churn telemetry.
	MetaKeyLeaseUntil = "pgrouter.lease_until"
	// MetaKeyLaunchedAt is the RFC3339 UTC instant the dispatch launched the
	// session. It lets a reconcile (or an absorbing dispatch) measure the time
	// budget from the real launch rather than from "now". Never a --label.
	MetaKeyLaunchedAt = "pgrouter.launched_at"
	// MetaKeyOrphanReclaimed marks a session the orphan reconcile closed
	// (RFC3339 UTC of the reclaim). A reclaimed row is NOT a duplicate worth
	// absorbing: its work was abandoned, so a redelivered dispatch must launch
	// afresh. Never a --label.
	MetaKeyOrphanReclaimed = "pgrouter.orphan_reclaimed"
	// MetaKeyEventID is the id of the pg-router event whose dispatch launched
	// the session (bead pg2-uprw5, ADR 0082). A redelivery of the SAME accepted
	// event carries the same id; a later, legitimate re-dispatch for the same
	// bead and role (a reopened review) is a NEW event with a different id. It is
	// what lets a dispatch tell a crash-window redelivery from a re-dispatch when
	// deciding whether to absorb a handler-closed settled row. Absent when the
	// dispatch carried no event id. Never a --label.
	MetaKeyEventID = "pgrouter.event_id"
	// MetaKeyPurgePending marks a session whose teardown is two-phase and not yet
	// finished (bead pg2-kqegi, INV-CCH-20): the handler has closed the row
	// (non-purge) but not yet removed its per-bead worktree, and will purge the
	// row only once the worktree is removed or confirmed gone. While the marker
	// is present the row is neither a duplicate to absorb nor an orphan to
	// reclaim; only the purge_pending retry handles it. Never a --label.
	MetaKeyPurgePending = "pgrouter.purge_pending"
	// MetaKeyPurgeAttempts counts the purge_pending retries made for a row (a
	// decimal integer), for the retry's own log line. Never a --label.
	MetaKeyPurgeAttempts = "pgrouter.purge_attempts"
)

// PoolName is the owner value stamped on pgrouter.pool, identifying pg-router's sessions
// among all sessions sharing a ccpool pool DB.
const PoolName = "pg-router"

// DispatchMeta builds the session metadata pg-router stamps on a session at dispatch.
//
// now is the dispatch's clock reading and leaseTTL the supervision-lease TTL
// (config.Config.LeaseTTL). The initial lease covers the whole `ccpool new`
// wait (EnsureTimeout) plus one TTL, so a session still launching is never
// mistaken for an orphan; the handler shortens it to now+TTL on its first
// refresh once Ensure succeeds. A zero now or a non-positive leaseTTL omits the
// lease keys (the session is then never treated as an orphan). A non-empty
// eventID is stamped as MetaKeyEventID; an empty one omits the key.
func DispatchMeta(beadID, role string, now time.Time, leaseTTL time.Duration, eventID string) map[string]string {
	m := map[string]string{
		MetaKeyBead: beadID,
		MetaKeyRole: role,
		MetaKeyPool: PoolName,
	}
	if eventID != "" {
		m[MetaKeyEventID] = eventID
	}
	if !now.IsZero() && leaseTTL > 0 {
		m[MetaKeyLaunchedAt] = FormatMetaTime(now)
		m[MetaKeyLeaseUntil] = FormatMetaTime(now.Add(EnsureTimeout + leaseTTL))
	}
	return m
}

// FormatMetaTime renders t the way the pgrouter.* time keys carry it: RFC3339, UTC.
func FormatMetaTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ParseMetaTime parses a pgrouter.* time value; ok is false for an absent or
// malformed value.
func ParseMetaTime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// LeaseUntil returns the session's supervision-lease expiry; ok is false when
// the session carries no (parseable) lease.
func (s Session) LeaseUntil() (time.Time, bool) { return ParseMetaTime(s.Meta[MetaKeyLeaseUntil]) }

// LaunchedAt returns the instant the dispatch launched the session; ok is
// false when the session carries no (parseable) launch time.
func (s Session) LaunchedAt() (time.Time, bool) { return ParseMetaTime(s.Meta[MetaKeyLaunchedAt]) }

// PurgePending reports whether the session carries the two-phase-teardown
// marker (MetaKeyPurgePending): its row is kept until its worktree is gone.
func (s Session) PurgePending() bool { return s.Meta[MetaKeyPurgePending] != "" }

// PurgeAttempts returns how many purge_pending retries have been made for the
// session (0 when absent or malformed).
func (s Session) PurgeAttempts() int {
	n, err := strconv.Atoi(s.Meta[MetaKeyPurgeAttempts])
	if err != nil || n < 0 {
		return 0
	}
	return n
}
