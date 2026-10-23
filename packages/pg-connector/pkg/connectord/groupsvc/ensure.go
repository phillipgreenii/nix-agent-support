package groupsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Status says how a group of an Ensure answer may be used.
type Status int

const (
	// Fresh: the row is inside fresh_until and its stamps match the current
	// primary content, or this very call fetched it.
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
	// Fresh forces the fetch of every needed group, the primary group included,
	// even a group inside fresh_until.
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
	// Primary is the description of the primary content the groups were judged
	// against; zero when none could be had (every group is then Unavailable).
	Primary engine.Primary
	// Content is the stored primary content Primary describes; empty when none
	// could be had. The connector decodes it into its own type.
	Content []byte
	// Groups has one entry per wanted group, in the order asked, the primary
	// group included.
	Groups []GroupResult
}

// errNotRefreshed is the error of a fetch that finished without a usable row.
var errNotRefreshed error = sentinelNotRefreshed

// attempt is one interactive fetch an Ensure queued and waited for.
type attempt struct {
	group   string
	flight  *scheduler.Flight
	done    bool
	err     error
	pending bool
	// commitsBefore and genBefore are what the group looked like when the first
	// attempt was queued, to tell whether this call fetched it.
	commitsBefore uint64
	genBefore     int64
}

func (a *attempt) succeeded() bool { return a != nil && a.done && a.err == nil }

// Ensure is the "entity read" for any set of groups of one entity. It records
// the access; makes sure the primary content is current (it comes first, because
// every other group's fetch needs its stored reference, stamps and state); serves
// the wanted groups that are inside fresh_until with matching stamps from the
// store; queues the others at interactive priority, fetches them in parallel
// (one fetch per entity and group however many callers ask) and waits up to ctx's
// deadline. A fetch still running at the deadline is NOT cancelled: it completes
// in the background and is stored, and the answer is the stale content inside
// expiry or unavailable. Fresh forces the fetch of every needed group.
//
// The primary group is not a Fetcher: Ensure submits the scheduler task of
// Spec.PrimaryGroup and waits for it, and the connector's own executor of that
// kind stores the content.
//
// id is a store id or a wire reference. An entity the origin says does not exist
// (an unknown reference, or an entity already tombstoned) is Config.NoSuchEntity
// and a malformed id Config.BadRef. Failures to fetch are NOT errors of Ensure:
// they are the Err of the group's result.
func (s *Service) Ensure(ctx context.Context, id string, want []string, opt EnsureOptions) (EnsureResult, error) {
	want, err := s.normalizeWant(want)
	if err != nil {
		return EnsureResult{}, err
	}
	node, _, err := s.Resolve(ctx, id)
	if err != nil {
		return EnsureResult{}, err
	}
	kind, primary := s.cfg.Spec.EntityKind, s.cfg.Spec.PrimaryGroup
	// ctx is the caller's deadline and bounds only the WAIT for fetches. The
	// bookkeeping reads and writes are local and must still work once it has
	// passed (a late answer is the stale row, not an error).
	sc := context.WithoutCancel(ctx)
	// Record the access: it raises the refresh priority and keeps the entity in
	// the keep-alive set. A tombstoned entity stays gone.
	if err := s.cfg.Store.TouchEntity(sc, kind, node, s.now()); err != nil {
		return EnsureResult{}, err
	}
	ent, _, err := s.cfg.Store.GetEntity(sc, kind, node)
	if err != nil {
		return EnsureResult{}, err
	}
	if !ent.TombstonedAt.IsZero() {
		return EnsureResult{}, fmt.Errorf("%w: %s", s.cfg.NoSuchEntity, id)
	}

	// Step 1: the primary group.
	prim, primRow, havePrim, err := s.loadPrimary(sc, node)
	if err != nil {
		return EnsureResult{}, err
	}
	var primAtt *attempt
	primGenBefore := int64(-1)
	if havePrim {
		primGenBefore = primRow.Generation
	}
	// The clock is read once for the primary group, here: the stored row either is
	// inside fresh_until now, or this call fetches it below (see primOK).
	primFreshAtRead := havePrim && !opt.Fresh && primRow.FreshUntil.After(s.now())
	if !primFreshAtRead {
		atts := s.fetchRound(ctx, node, ent.LastAccess, []string{primary}, func(string) bool {
			_, row, ok, _ := s.loadPrimary(sc, node)
			return ok && row.FreshUntil.After(s.now())
		})
		primAtt = atts[primary]
		primAtt.genBefore = primGenBefore
		if prim, primRow, havePrim, err = s.loadPrimary(sc, node); err != nil {
			return EnsureResult{}, err
		}
	} else {
		_ = s.cfg.KeepAlive.Access(sc, node) // books the primary group's own cycle
	}
	// A primary fetch (or any other) may have found the entity gone.
	if gone, err := s.tombstoned(sc, node); err != nil {
		return EnsureResult{}, err
	} else if gone {
		return EnsureResult{}, fmt.Errorf("%w: %s", s.cfg.NoSuchEntity, id)
	}

	now := s.now()
	// The primary content is current when it was inside fresh_until at the read
	// above, or when this very call fetched it (its generation moved): a row this
	// call stored is Fresh however long the answer took to arrive, so the clock is
	// not read again. Judging fresh_until a second time let a host slow enough to
	// outlast the primary TTL between the store and this line age the content out
	// under the caller, and every dependent group was then refused as unrefreshed.
	primOK := havePrim && (primAtt == nil || (primAtt.succeeded() && primRow.Generation != primAtt.genBefore))
	if primAtt != nil && primAtt.succeeded() && !primOK {
		primAtt.err = errNotRefreshed
	}
	primServable := s.servable(primRow, havePrim, now)
	if !primOK && !primServable {
		// No primary content to judge anything by: every wanted group is unavailable.
		err := primaryFailure(ctx, primAtt)
		out := EnsureResult{Groups: make([]GroupResult, len(want))}
		for i, g := range want {
			out.Groups[i] = GroupResult{Group: g, Status: Unavailable, Pending: primAtt != nil && primAtt.pending, Err: err}
		}
		return out, nil
	}

	// Step 2: the other groups.
	stamps := groupkit.Stamps{Primary: prim.Stamp, Secondary: prim.Stamp2}
	results := make(map[string]GroupResult, len(want))
	var toFetch []string
	for _, g := range want {
		if g == primary {
			continue
		}
		row, ok, err := s.cfg.Store.GetGroup(sc, kind, node, g)
		if err != nil {
			return EnsureResult{}, err
		}
		switch {
		case !primOK:
			// The primary content could not be refreshed: nothing is judged fresh
			// against content that may be out of date; serve what is inside expiry.
			results[g] = s.settle(ctx, g, row, ok, primaryAsAttempt(primAtt), false, now)
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
			row, ok, err := s.cfg.Store.GetGroup(sc, kind, node, g)
			return err == nil && ok && s.cfg.Rules.Fresh(row, stamps, s.now()).IsFresh()
		})
		if gone, err := s.tombstoned(sc, node); err != nil {
			return EnsureResult{}, err
		} else if gone {
			return EnsureResult{}, fmt.Errorf("%w: %s", s.cfg.NoSuchEntity, id)
		}
		now = s.now()
		for _, g := range toFetch {
			a := atts[g]
			a.commitsBefore = before[g]
			row, ok, err := s.cfg.Store.GetGroup(sc, kind, node, g)
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
	out := EnsureResult{Primary: prim, Content: primRow.Content, Groups: make([]GroupResult, 0, len(want))}
	members := map[string]bool(nil)
	for _, g := range want {
		var r GroupResult
		if g == primary {
			r = s.settle(ctx, g, primRow, havePrim, primAtt, primOK, now)
			r.FromOrigin = primAtt.succeeded() && havePrim && primRow.Generation != primAtt.genBefore
		} else {
			r = results[g]
		}
		out.Groups = append(out.Groups, r)
		if r.Status == Fresh && !r.FromOrigin && g != primary {
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

// primaryAsAttempt carries a failed or pending primary refresh into the results
// of the groups that could not be judged against it.
func primaryAsAttempt(a *attempt) *attempt {
	if a == nil {
		return &attempt{done: true, err: errNotRefreshed}
	}
	return &attempt{done: a.done, err: a.err, pending: a.pending}
}

func primaryFailure(ctx context.Context, a *attempt) error {
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

// servable reports whether a stored row may be served stale: it has content and
// was fetched within expiry.
func (s *Service) servable(row store.GroupRow, ok bool, now time.Time) bool {
	return ok && len(row.Content) > 0 && !row.FetchedAt.IsZero() && now.Sub(row.FetchedAt) <= s.cfg.Expiry
}

// tombstoned reports whether the entity has been marked gone.
func (s *Service) tombstoned(ctx context.Context, id string) (bool, error) {
	ent, ok, err := s.cfg.Store.GetEntity(ctx, s.cfg.Spec.EntityKind, id)
	if err != nil {
		return false, err
	}
	return ok && !ent.TombstonedAt.IsZero(), nil
}

// normalizeWant drops duplicates, keeps the order, and rejects a name outside
// the closed set of groups.
func (s *Service) normalizeWant(want []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, g := range want {
		if !s.isGroup(g) {
			return nil, fmt.Errorf("%w %q", ErrUnknownGroup, g)
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

// Resolve returns the store id and the wire reference of an entity named by a
// store id or by a wire reference. A reference the alias table does not know is
// resolved with Config.Resolver (admitted as interactive); an entity the origin
// says does not exist is Config.NoSuchEntity and a malformed id Config.BadRef. A
// store id whose alias is unknown comes back with an empty reference.
func (s *Service) Resolve(ctx context.Context, id string) (nodeID, ref string, err error) {
	wire, isRef, perr := s.cfg.ParseID(id)
	if !isRef {
		if perr != nil {
			return "", "", fmt.Errorf("%w: %q", s.cfg.BadRef, id)
		}
		ref, _, err := s.cfg.Aliases.Ref(ctx, id)
		if err != nil {
			return "", "", err
		}
		return id, ref, nil
	}
	if perr != nil {
		return "", "", fmt.Errorf("%w: %v", s.cfg.BadRef, perr)
	}
	found, err := s.cfg.Aliases.Resolve(ctx, []string{wire})
	if err != nil {
		return "", "", err
	}
	if a, ok := found[wire]; ok && a.ID != "" {
		return a.ID, a.CanonicalRef, nil
	}
	if s.cfg.Resolver == nil {
		return "", "", fmt.Errorf("groupsvc: %s is not known and no resolver is configured", id)
	}
	res, _, err := s.cfg.Resolver.Resolve(ctx,
		CallMeta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive}, []string{wire})
	if err != nil {
		return "", "", err
	}
	if len(res) == 0 || !res[0].Found {
		return "", "", fmt.Errorf("%w: %s", s.cfg.NoSuchEntity, id)
	}
	return res[0].NodeID, res[0].Ref, nil
}
