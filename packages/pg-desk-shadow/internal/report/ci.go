package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
)

// CI-driven categorization changes: a PR moving between "blocked" and
// "reviewable". Pinned meaning: a PR is blocked while its latest CI rollup is
// failure and reviewable otherwise (success, pending, none); a change is a ci
// feed item whose rollup moves the PR from one state to the other relative to the
// PR's previous ci item in time order (ties on the item time broken by Seq); the
// first ci item of a PR sets its state and is not a change.

const (
	stateBlocked    = "blocked"
	stateReviewable = "reviewable"
)

// liveCategorySource names where the live side's categorization is read from.
const liveCategorySource = "router queue rows (payload.metadata.categorization)"

// liveUnobservableReason is the statement text when no live row names a
// categorization (every row the live router writes today).
const liveUnobservableReason = "no live router log row names a PR's blocked-or-reviewable categorization (queue rows carry the change kind only, events rows and the run record carry no CI state), so the daemon side is reported alone"

func rollupState(rollup string) string {
	if rollup == "failure" {
		return stateBlocked
	}
	return stateReviewable
}

type ciPoint struct {
	id    string
	at    time.Time
	seq   int64
	state string
}

type ciMove struct {
	id       string
	at       time.Time
	from, to string
}

// ciMoves returns the state moves of every PR, given its points in any order.
func ciMoves(points []ciPoint) []ciMove {
	byPR := map[string][]ciPoint{}
	for _, p := range points {
		byPR[p.id] = append(byPR[p.id], p)
	}
	ids := make([]string, 0, len(byPR))
	for id := range byPR {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []ciMove
	for _, id := range ids {
		ps := byPR[id]
		sort.SliceStable(ps, func(i, j int) bool {
			if !ps[i].at.Equal(ps[j].at) {
				return ps[i].at.Before(ps[j].at)
			}
			return ps[i].seq < ps[j].seq
		})
		prev := ""
		for _, p := range ps {
			if prev != "" && prev != p.state {
				out = append(out, ciMove{id: id, at: p.at, from: prev, to: p.state})
			}
			prev = p.state
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

func inWindow(m ciMove, from, to time.Time) bool { return !m.at.Before(from) && !m.at.After(to) }

// seenBy reports whether the other side has a move of the same PR to the same
// state within tol.
func seenBy(m ciMove, other []ciMove, tol time.Duration) bool {
	for _, o := range other {
		if o.id == m.id && o.to == m.to && absDur(o.at.Sub(m.at)) <= tol {
			return true
		}
	}
	return false
}

// computeCI builds the CI-driven section from the daemon's ci-feed items and,
// when any queue row names a categorization, from the live side.
func computeCI(items []shadowItem, queue []QueueRow, lab Labeler, from, to time.Time, tol time.Duration) CIDriven {
	var dpts []ciPoint
	for _, it := range items {
		if it.origin == schema.OriginDaemonCIFeed {
			dpts = append(dpts, ciPoint{id: it.id, at: it.at, seq: it.seq, state: rollupState(it.ciRollup)})
		}
	}
	daemonAll := ciMoves(dpts)

	var lpts []ciPoint
	for _, q := range queue {
		if q.Op != "enqueue" || (q.Category != stateBlocked && q.Category != stateReviewable) {
			continue
		}
		_, id, _ := strings.Cut(q.EventID, ":")
		lpts = append(lpts, ciPoint{id: BaseID(id), at: q.At, state: q.Category})
	}
	liveObservable := len(lpts) > 0
	liveAll := ciMoves(lpts)

	out := CIDriven{LiveObservable: liveObservable}
	side := func(all, other []ciMove) CISide {
		s := CISide{Entries: []CIEntry{}}
		for _, m := range all {
			if !inWindow(m, from, to) {
				continue
			}
			seen := seenBy(m, other, tol)
			s.Changes++
			if seen {
				s.AlsoSeenByOther++
			}
			s.Entries = append(s.Entries, CIEntry{PR: lab.Label(m.id), At: schema.Format(m.at), From: m.from, To: m.to, AlsoSeenByOther: seen})
		}
		return s
	}
	out.Daemon = side(daemonAll, liveAll)
	if !liveObservable {
		// Without a live figure nothing is "also seen": report the daemon side alone.
		out.Daemon.AlsoSeenByOther = 0
		for i := range out.Daemon.Entries {
			out.Daemon.Entries[i].AlsoSeenByOther = false
		}
		out.Statement = "live side not observable: " + liveUnobservableReason + fmt.Sprintf(". Daemon side: %d CI-driven categorization changes (a PR moving between blocked and reviewable) in the run window.", out.Daemon.Changes)
		return out
	}
	live := side(liveAll, daemonAll)
	out.Live, out.LiveSource = &live, liveCategorySource
	out.Statement = fmt.Sprintf("CI-driven categorization changes (a PR moving between blocked and reviewable) in the run window: daemon side %d (%d also seen by the live side), live side %d (%d also seen by the daemon side); a move counts as seen when the other side has a move of the same PR to the same state within the tolerance of %s. Live source: %s.", out.Daemon.Changes, out.Daemon.AlsoSeenByOther, live.Changes, live.AlsoSeenByOther, tol, liveCategorySource)
	return out
}
