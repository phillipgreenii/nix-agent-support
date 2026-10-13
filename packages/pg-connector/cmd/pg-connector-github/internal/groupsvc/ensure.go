package groupsvc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Status says how a group of an Ensure answer may be used.
type Status int

const (
	// Fresh: the row is inside fresh_until and its stamps match the current
	// summary, or this very call fetched it.
	Fresh Status = iota + 1
	// Stale: the row is served past its freshness but inside expiry.
	Stale
	// Unavailable: there is no row inside expiry; Err says why.
	Unavailable
)

// String names the status.
func (s Status) String() string {
	switch s {
	case Fresh:
		return "fresh"
	case Stale:
		return "stale"
	case Unavailable:
		return "unavailable"
	}
	return "unknown"
}

// EnsureOptions are the options of Ensure.
type EnsureOptions struct {
	// Fresh forces the fetch of every needed group, summary included, even a
	// group inside fresh_until (the wire argument fresh: true).
	Fresh bool
}

// GroupResult is the answer for one wanted group.
type GroupResult struct {
	Group string
	// Row is the stored row served (zero for Unavailable without a row).
	Row    store.GroupRow
	Status Status
	// Pending is true when the fetch was still running at the caller's deadline:
	// it is not cancelled and completes in the background.
	Pending bool
	// FromOrigin is true when this very call fetched the group from origin, false
	// when it was answered from the store.
	FromOrigin bool
	// Err is why the group is not Fresh: for Unavailable the governor's cap or
	// reserve, the origin failure, or the deadline; for Stale the failure that
	// kept the refresh from finishing (nil when only the deadline did).
	Err error
}

// EnsureResult is the answer of Ensure.
type EnsureResult struct {
	// Summary is the current summary the groups were judged against; zero when no
	// summary could be had (every group is then Unavailable).
	Summary summary.Summary
	// Groups has one entry per wanted group, in the order asked, summary
	// included.
	Groups []GroupResult
}

// errNotRefreshed is the error of a fetch that finished without a usable row.
var errNotRefreshed = errors.New("groupsvc: the refresh finished without storing a fresh row")

// attempt is one interactive fetch an Ensure queued and waited for.
type attempt struct {
	group   string
	flight  *scheduler.Flight
	done    bool
	err     error
	pending bool
	// before is what the group looked like when the first attempt was queued, to
	// tell whether this call fetched it.
	commitsBefore uint64
	genBefore     int64
}

func (a *attempt) succeeded() bool { return a != nil && a.done && a.err == nil }

// Ensure is the spec's "Entity read" for any set of groups of one PR. It records
// the access; makes sure the summary is current (it comes first, because every
// other group's fetch needs its stored reference, head, base and state); serves
// the wanted groups that are inside fresh_until with matching stamps from the
// store; queues the others at interactive priority, fetches them in parallel
// (one fetch per entity and group however many callers ask) and waits up to ctx's
// deadline. A fetch still running at the deadline is NOT cancelled: it completes
// in the background and is stored, and the answer is the stale content inside
// expiry or unavailable. Fresh forces the fetch of every needed group.
//
// id is a node id or owner/repo#n. A PR GitHub says does not exist (an unknown
// reference, or an entity already tombstoned) is ErrNoSuchPR and a malformed id
// ErrBadRef. Failures to fetch are NOT errors of Ensure: they are the Err of the
// group's result.
func (s *Service) Ensure(ctx context.Context, id string, want []string, opt EnsureOptions) (EnsureResult, error) {
	want, err := normalizeWant(want)
	if err != nil {
		return EnsureResult{}, err
	}
	node, _, err := s.Resolve(ctx, id)
	if err != nil {
		return EnsureResult{}, err
	}
	// ctx is the caller's deadline and bounds only the WAIT for fetches. The
	// bookkeeping reads and writes are local and must still work once it has
	// passed (a late answer is the stale row, not an error).
	sc := context.WithoutCancel(ctx)
	// Record the access: it raises the refresh priority and keeps the entity in
	// the keep-alive set. A tombstoned entity stays gone.
	if err := s.cfg.Store.TouchEntity(sc, entity.KindPR, node, s.now()); err != nil {
		return EnsureResult{}, err
	}
	ent, _, err := s.cfg.Store.GetEntity(sc, entity.KindPR, node)
	if err != nil {
		return EnsureResult{}, err
	}
	if !ent.TombstonedAt.IsZero() {
		return EnsureResult{}, fmt.Errorf("%w: %s", ErrNoSuchPR, id)
	}

	// Step 1: the summary.
	sum, sumRow, haveSum, err := s.loadSummary(sc, node)
	if err != nil {
		return EnsureResult{}, err
	}
	var sumAtt *attempt
	sumGenBefore := int64(-1)
	if haveSum {
		sumGenBefore = sumRow.Generation
	}
	if opt.Fresh || !haveSum || !sumRow.FreshUntil.After(s.now()) {
		atts := s.fetchRound(ctx, node, ent.LastAccess, []string{summary.GroupName}, func(string) bool {
			_, row, ok, _ := s.loadSummary(sc, node)
			return ok && row.FreshUntil.After(s.now())
		})
		sumAtt = atts[summary.GroupName]
		sumAtt.genBefore = sumGenBefore
		if sum, sumRow, haveSum, err = s.loadSummary(sc, node); err != nil {
			return EnsureResult{}, err
		}
	} else {
		_ = s.cfg.Summary.Access(sc, node) // books the summary's own cycle
	}
	// A summary fetch (or any other) may have found the PR gone.
	if gone, err := s.tombstoned(sc, node); err != nil {
		return EnsureResult{}, err
	} else if gone {
		return EnsureResult{}, fmt.Errorf("%w: %s", ErrNoSuchPR, id)
	}

	now := s.now()
	sumOK := haveSum && sumRow.FreshUntil.After(now) && (sumAtt == nil || sumAtt.succeeded())
	if sumAtt != nil && sumAtt.succeeded() && !sumOK {
		sumAtt.err = errNotRefreshed
	}
	sumServable := s.servable(sumRow, haveSum, now)
	if !sumOK && !sumServable {
		// No summary to judge anything by: every wanted group is unavailable.
		err := summaryFailure(ctx, sumAtt)
		out := EnsureResult{Groups: make([]GroupResult, len(want))}
		for i, g := range want {
			out.Groups[i] = GroupResult{Group: g, Status: Unavailable, Pending: sumAtt != nil && sumAtt.pending, Err: err}
		}
		return out, nil
	}

	// Step 2: the other groups.
	stamps := groups.StampsOf(sum)
	results := make(map[string]GroupResult, len(want))
	needed := map[string]store.GroupRow{}
	var toFetch []string
	for _, g := range want {
		if g == summary.GroupName {
			continue
		}
		row, ok, err := s.cfg.Store.GetGroup(sc, entity.KindPR, node, g)
		if err != nil {
			return EnsureResult{}, err
		}
		needed[g] = row
		switch {
		case !sumOK:
			// The summary could not be refreshed: nothing is judged fresh against
			// a summary that may be out of date; serve what is inside expiry.
			results[g] = s.settle(ctx, g, row, ok, summaryAsAttempt(sumAtt), false, now)
		case ok && !opt.Fresh && s.cfg.Rules.Fresh(row, stamps, now).IsFresh():
			results[g] = GroupResult{Group: g, Row: row, Status: Fresh}
		default:
			if _, registered := s.fetcher(g); !registered {
				results[g] = s.settle(ctx, g, row, ok, &attempt{done: true, err: fmt.Errorf("%w: %s", ErrNoFetcher, g)}, false, now)
				continue
			}
			toFetch = append(toFetch, g)
		}
	}
	if len(toFetch) > 0 {
		before := map[string]uint64{}
		for _, g := range toFetch {
			before[g] = s.commitCount(node, g)
		}
		atts := s.fetchRound(ctx, node, ent.LastAccess, toFetch, func(g string) bool {
			row, ok, err := s.cfg.Store.GetGroup(sc, entity.KindPR, node, g)
			return err == nil && ok && s.cfg.Rules.Fresh(row, stamps, s.now()).IsFresh()
		})
		if gone, err := s.tombstoned(sc, node); err != nil {
			return EnsureResult{}, err
		} else if gone {
			return EnsureResult{}, fmt.Errorf("%w: %s", ErrNoSuchPR, id)
		}
		now = s.now()
		for _, g := range toFetch {
			a := atts[g]
			a.commitsBefore = before[g]
			row, ok, err := s.cfg.Store.GetGroup(sc, entity.KindPR, node, g)
			if err != nil {
				return EnsureResult{}, err
			}
			fromOrigin := a.succeeded() && s.commitCount(node, g) != a.commitsBefore
			judged := ok && s.cfg.Rules.Fresh(row, stamps, now).IsFresh()
			r := s.settle(ctx, g, row, ok, a, judged, now)
			r.FromOrigin = fromOrigin
			results[g] = r
		}
	}

	// Assemble in the order asked.
	out := EnsureResult{Summary: sum, Groups: make([]GroupResult, 0, len(want))}
	members := map[string]bool(nil)
	for _, g := range want {
		var r GroupResult
		if g == summary.GroupName {
			r = s.settle(ctx, g, sumRow, haveSum, sumAtt, sumOK, now)
			r.FromOrigin = sumAtt.succeeded() && haveSum && sumRow.Generation != sumAtt.genBefore
		} else {
			r = results[g]
		}
		out.Groups = append(out.Groups, r)
		if r.Status == Fresh && !r.FromOrigin && g != summary.GroupName {
			if members == nil {
				if members, err = s.members(sc); err != nil {
					members = map[string]bool{}
				}
			}
			_ = s.bookRead(sc, node, g, r.Row, members)
		}
	}
	if o := s.obs(); o != nil {
		for _, r := range out.Groups {
			if r.Status == Unavailable {
				continue
			}
			var staleness time.Duration
			if r.Status == Stale && !r.Row.FreshUntil.IsZero() && now.After(r.Row.FreshUntil) {
				staleness = now.Sub(r.Row.FreshUntil)
			}
			o.Served(r.Group, staleness)
		}
	}
	return out, nil
}

// summaryAsAttempt carries a failed or pending summary refresh into the results
// of the groups that could not be judged against it.
func summaryAsAttempt(a *attempt) *attempt {
	if a == nil {
		return &attempt{done: true, err: errNotRefreshed}
	}
	return &attempt{done: a.done, err: a.err, pending: a.pending}
}

func summaryFailure(ctx context.Context, a *attempt) error {
	switch {
	case a == nil:
		return errNotRefreshed
	case a.pending:
		return ctx.Err()
	case a.err != nil:
		return a.err
	}
	return errNotRefreshed
}

// settle turns the row an Ensure ended up with into the group's result. fresh is
// the verdict of the freshness rules for the row now stored; it counts only when
// the fetch (if one was queued) finished cleanly.
func (s *Service) settle(ctx context.Context, group string, row store.GroupRow, ok bool, a *attempt, fresh bool, now time.Time) GroupResult {
	r := GroupResult{Group: group}
	if ok {
		r.Row = row
	}
	if a != nil {
		r.Pending = a.pending
	}
	switch {
	case ok && fresh && (a == nil || a.succeeded()):
		r.Status = Fresh
		return r
	case s.servable(row, ok, now):
		r.Status = Stale
		if a != nil {
			r.Err = a.err
		}
		return r
	}
	r.Status = Unavailable
	switch {
	case a != nil && a.err != nil:
		r.Err = a.err
	case a != nil && a.pending:
		r.Err = ctx.Err()
	default:
		r.Err = errNotRefreshed
	}
	return r
}

// tombstoned reports whether the entity has been marked gone.
func (s *Service) tombstoned(ctx context.Context, node string) (bool, error) {
	ent, ok, err := s.cfg.Store.GetEntity(ctx, entity.KindPR, node)
	if err != nil {
		return false, err
	}
	return ok && !ent.TombstonedAt.IsZero(), nil
}

// normalizeWant drops duplicates, keeps the order, and rejects a name outside
// the closed set of groups.
func normalizeWant(want []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, g := range want {
		if !isGroup(g) {
			return nil, fmt.Errorf("groupsvc: unknown group %q", g)
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out, nil
}

// fetchRound queues every named group at interactive priority and waits for
// them, in parallel, up to ctx's deadline. A group whose fetch finished cleanly
// but whose row is still not fresh (a write superseded it, or another path's
// refresh was dropped as already fresh) is queued once more. A fetch that is
// still running when ctx ends is left running and reported pending.
func (s *Service) fetchRound(ctx context.Context, node string, access time.Time, names []string, fresh func(group string) bool) map[string]*attempt {
	atts := make(map[string]*attempt, len(names))
	for pass := 0; pass < 2; pass++ {
		var round []*attempt
		for _, g := range names {
			a := atts[g]
			if a != nil && (a.pending || a.err != nil || fresh(g)) {
				continue
			}
			if a != nil && ctx.Err() != nil {
				continue
			}
			na := &attempt{group: g}
			f, err := s.cfg.Scheduler.SubmitInteractive(scheduler.Task{Kind: g, Entity: node, Group: g, Access: access})
			if err != nil {
				na.done, na.err = true, err
			} else {
				na.flight = f
			}
			atts[g] = na
			round = append(round, na)
		}
		if len(round) == 0 {
			break
		}
		for _, a := range round {
			if a.flight == nil {
				continue
			}
			select {
			case <-a.flight.Done():
			default:
				select {
				case <-a.flight.Done():
				case <-ctx.Done():
					a.pending = true
					continue
				}
			}
			a.done, a.err = true, a.flight.Result().Err
		}
	}
	return atts
}
