// Package list is the pr instance's list op, served from the daemon's store.
//
// # Where the answer comes from
//
// The query NAME must be one the daemon's OWN config defines (the request's
// config.queries is not read): an unknown name answers query_not_recognized. A
// query KEY is the name plus the wire argument "args" (the arguments that change
// what GitHub is asked, see query.Key); list_since and list_before, which arrive
// in the request's config block, only filter and are applied locally to the
// cached ids by their summaries' updatedAt, at no origin cost.
//
//  1. The query's stored id list is used while it is inside its TTL (query_ttl
//     for the configured query, adhoc_query_ttl for an ad-hoc key). Otherwise
//     the query is re-run, as a class 1 task the caller waits on.
//  2. Every id then needs a summary (ids_only without a time range needs none):
//     a fresh stored one is used as is, a missing or stale one is fetched (the
//     summary service batches them at most 74 to a call with spare slots
//     filled). The answer is returned once every id has one.
//  3. The answer stays at summary level (schema.PRListFields).
//
// The wire argument fresh: true forces the origin for the query and for every
// summary.
//
// # When the origin cannot answer
//
// If the governor refuses, the origin fails, the daemon is unauthenticated or the
// caller's deadline passes, the answer is the stored content when it is inside
// expiry (served as stale, with its age), otherwise unavailable naming the cap or
// the reserve (unauthenticated while the token is rejected). A fetch still
// running at the deadline is not cancelled: it completes and is stored.
//
// # Annotations
//
// The handler attaches served_from (cache, or origin once a call was made),
// stale and the age of the oldest stored data served to the router's carrier.
//
// # Result
//
// The result is schema.PRListResult with the same field names and error codes as
// pkg/provider/pr's list. ids are the wire form owner/repo#n; entities are the
// stored summaries (the carried-forward mergeable value, not the observed one);
// truncated is true when the listing was not complete (past GitHub's 1,000-result
// search limit) or a bound could not be judged.
package list

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Served-from values of the annotation.
const (
	ServedFromCache  = "cache"
	ServedFromOrigin = "origin"
)

// Queries is the query runner as the handler uses it. *query.Runner satisfies
// it.
type Queries interface {
	Expr(name string) (schema.QueryExpr, bool)
	Row(ctx context.Context, key query.Key) (store.QueryRow, bool, error)
	Fetch(ctx context.Context, key query.Key) (store.QueryRow, error)
}

var _ Queries = (*query.Runner)(nil)

// Summaries is the summary service as the handler uses it. *summary.Service
// satisfies it.
type Summaries interface {
	Interactive(ctx context.Context, ids []string) []summary.InteractiveResult
	InteractiveFresh(ctx context.Context, ids []string) []summary.InteractiveResult
	Access(ctx context.Context, id string) error
}

var _ Summaries = (*summary.Service)(nil)

// Store is the part of the daemon store the handler reads.
type Store interface {
	GetGroup(ctx context.Context, kind, id, group string) (store.GroupRow, bool, error)
}

var _ Store = (*store.Store)(nil)

// Auth is the typed unauthenticated state. *identity.Tracker satisfies it.
type Auth interface {
	Observe(err error) bool
	Unauthenticated() (bool, error)
}

// Config wires the handler.
type Config struct {
	Queries   Queries
	Summaries Summaries
	Store     Store
	Identity  *entity.Identity
	// Auth is optional: nil means always authenticated.
	Auth Auth
	// Expiry is expiry: stored content older than this is never served.
	Expiry time.Duration
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Register installs the list op under the pr instance.
func Register(r *router.Router, cfg Config) error {
	h, err := Handler(cfg)
	if err != nil {
		return err
	}
	r.Register("pr", scriptout.OpList, h)
	return nil
}

// Handler returns the op handler.
func Handler(cfg Config) (scriptout.OpHandler, error) {
	switch {
	case cfg.Queries == nil, cfg.Summaries == nil, cfg.Store == nil, cfg.Identity == nil:
		return scriptout.OpHandler{}, errors.New("list: handler needs queries, summaries, a store and identity")
	}
	if cfg.Clock == nil {
		cfg.Clock = governor.SystemClock{}
	}
	h := &handler{cfg: cfg}
	return scriptout.OpHandler{SchemaVersion: schema.PRSchemaVersion, Handle: h.handle}, nil
}

type handler struct{ cfg Config }

func (h *handler) now() time.Time { return h.cfg.Clock.Now() }

// wireArgs are the list op's arguments. cursor is accepted and ignored (no
// incremental listing).
type wireArgs struct {
	Query   string            `json:"query"`
	Cursor  json.RawMessage   `json:"cursor"`
	IDsOnly bool              `json:"ids_only"`
	Fresh   bool              `json:"fresh"`
	Args    map[string]string `json:"args"`
}

func (h *handler) handle(ctx context.Context, raw json.RawMessage) (any, error) {
	var a wireArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list args: "+err.Error())
	}
	rng, err := scriptout.ListRangeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	// The daemon's OWN config decides: the request's config.queries is ignored.
	if _, ok := h.cfg.Queries.Expr(a.Query); !ok {
		return nil, scriptout.WrapError(scriptout.ErrQueryNotRecognized,
			fmt.Sprintf("query %q is not defined in this daemon's config", a.Query))
	}
	key := query.Key{Name: a.Query, Args: a.Args}

	srv := &served{}
	row, err := h.queryRow(ctx, key, a.Fresh, srv)
	if err != nil {
		return nil, err
	}

	needSummaries := !a.IDsOnly || !rng.IsZero()
	var (
		sums      map[string]loaded
		imprecise bool
	)
	ids := row.IDs
	if needSummaries {
		sums, err = h.summaries(ctx, row.IDs, a.Fresh, srv)
		if err != nil {
			return nil, err
		}
		ids = ids[:0:0]
		for _, id := range row.IDs {
			l, ok := sums[id]
			if !ok {
				continue // gone from GitHub since the listing
			}
			keep, imp := inRange(l.sum.UpdatedAt, rng)
			imprecise = imprecise || imp
			if keep {
				ids = append(ids, id)
			}
		}
	}
	if key.Adhoc() {
		// An ad-hoc answer's ids join the keep-alive set through the access;
		// the summary fetch above already recorded it for the ids it fetched.
		for _, id := range row.IDs {
			if _, fetched := srv.fetchedIDs[id]; fetched {
				continue
			}
			if err := h.cfg.Summaries.Access(ctx, id); err != nil && !errors.Is(err, summary.ErrUnknownEntity) {
				return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("record access of %s: %v", id, err))
			}
		}
	}

	h.annotate(ctx, srv, row)

	res := &schema.PRListResult{
		PresentIDs: nonNil(ids),
		Cursor:     nil,
		Truncated:  !row.Complete || imprecise,
	}
	if !a.IDsOnly {
		res.Entities = make([]schema.PR, 0, len(ids))
		for _, id := range ids {
			res.Entities = append(res.Entities, toPR(sums[id]))
		}
	}
	return res, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// served accumulates what the answer was made of, for the annotation.
type served struct {
	origin     bool // an origin call was made for this request
	stale      bool // some content was served past its freshness
	oldest     time.Time
	fetchedIDs map[string]struct{}
}

func (s *served) note(fetchedAt time.Time) {
	if fetchedAt.IsZero() {
		return
	}
	if s.oldest.IsZero() || fetchedAt.Before(s.oldest) {
		s.oldest = fetchedAt
	}
}

func (h *handler) annotate(ctx context.Context, s *served, row store.QueryRow) {
	s.note(row.FetchedAt)
	from := ServedFromCache
	if s.origin {
		from = ServedFromOrigin
	}
	var age int64
	if !s.oldest.IsZero() {
		age = max(int64(h.now().Sub(s.oldest)/time.Second), 0)
	}
	router.Annotate(ctx, from, s.stale, age, 0)
}

// queryRow returns the query's row: the stored one inside its TTL, otherwise the
// result of a re-run, falling back to the stored content when it is inside
// expiry.
func (h *handler) queryRow(ctx context.Context, key query.Key, fresh bool, srv *served) (store.QueryRow, error) {
	row, have, err := h.cfg.Queries.Row(ctx, key)
	if err != nil {
		return store.QueryRow{}, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("read query %q: %v", key, err))
	}
	now := h.now()
	if have && !row.FetchedAt.IsZero() && row.FreshUntil.After(now) && !fresh {
		return row, nil
	}
	usable := have && !row.FetchedAt.IsZero() && now.Sub(row.FetchedAt) <= h.cfg.Expiry

	var cause error
	if bad, uerr := h.unauth(); bad {
		cause = query.UnauthenticatedError(uerr)
	} else {
		got, ferr := h.cfg.Queries.Fetch(ctx, key)
		if ferr == nil {
			srv.origin = true
			return got, nil
		}
		cause = ferr
	}
	if usable {
		srv.stale = true
		return row, nil
	}
	return store.QueryRow{}, h.classify(fmt.Sprintf("query %q", key.Name), cause)
}

func (h *handler) unauth() (bool, error) {
	if h.cfg.Auth == nil {
		return false, nil
	}
	return h.cfg.Auth.Unauthenticated()
}

func (h *handler) observe(err error) {
	if h.cfg.Auth != nil {
		h.cfg.Auth.Observe(err)
	}
}

// classify maps why the origin could not answer onto the closed error taxonomy.
func (h *handler) classify(what string, cause error) error {
	var refusal *governor.Refusal
	switch {
	case errors.As(cause, &refusal):
		return scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("%s: %s", what, refusal.Reason))
	case errors.Is(cause, vcs.ErrAuthInvalid):
		return scriptout.WrapError(scriptout.ErrUnauthenticated, fmt.Sprintf("%s: %v", what, cause))
	case errors.Is(cause, query.ErrPending):
		return scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("%s: still being fetched at the deadline; it completes in the background", what))
	default:
		return scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("%s: %v", what, cause))
	}
}

// loaded is one stored summary and when it was fetched.
type loaded struct {
	sum       summary.Summary
	fetchedAt time.Time
	stale     bool
}

// summaries returns the summary of every id (keyed by the id as the query row
// holds it), fetching the missing and stale ones. An id GitHub no longer returns
// is absent from the map.
func (h *handler) summaries(ctx context.Context, ids []string, force bool, srv *served) (map[string]loaded, error) {
	out := make(map[string]loaded, len(ids))
	now := h.now()
	var need []string
	for _, id := range ids {
		g, ok, err := h.group(ctx, id)
		if err != nil {
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("read summary of %s: %v", id, err))
		}
		if ok && !force && g.FreshUntil.After(now) {
			l, err := decode(g, false)
			if err != nil {
				return nil, err
			}
			out[id] = l
			srv.note(g.FetchedAt)
			continue
		}
		need = append(need, id)
	}
	if len(need) == 0 {
		return out, nil
	}

	bad, uerr := h.unauth()
	var results []summary.InteractiveResult
	if !bad {
		if force {
			results = h.cfg.Summaries.InteractiveFresh(ctx, need)
		} else {
			results = h.cfg.Summaries.Interactive(ctx, need)
		}
		if srv.fetchedIDs == nil {
			srv.fetchedIDs = map[string]struct{}{}
		}
		for _, id := range need {
			srv.fetchedIDs[id] = struct{}{}
		}
	}
	for k, id := range need {
		var cause error
		if bad {
			cause = query.UnauthenticatedError(uerr)
		} else {
			r := results[k]
			switch {
			case r.Err == nil && r.Fresh:
				srv.origin = true
				h.observe(nil)
				g, ok, err := h.group(ctx, id)
				if err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("read summary of %s: %v", id, err))
				}
				if !ok {
					continue // GitHub no longer returns it
				}
				l, err := decode(g, false)
				if err != nil {
					return nil, err
				}
				out[id] = l
				srv.note(g.FetchedAt)
				continue
			case r.Err != nil:
				h.observe(r.Err)
				cause = r.Err
			default:
				cause = query.ErrPending
			}
		}
		// The summary could not be refreshed: the stored one is served when it is
		// inside expiry, else the answer is unavailable.
		g, ok, err := h.group(ctx, id)
		if err != nil {
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("read summary of %s: %v", id, err))
		}
		if !ok || now.Sub(g.FetchedAt) > h.cfg.Expiry {
			return nil, h.classify("summary of "+id, cause)
		}
		l, err := decode(g, true)
		if err != nil {
			return nil, err
		}
		out[id] = l
		srv.note(g.FetchedAt)
		srv.stale = true
	}
	return out, nil
}

// group reads the stored summary group of an id (a node id or owner/repo#n).
func (h *handler) group(ctx context.Context, id string) (store.GroupRow, bool, error) {
	node := id
	if strings.Contains(id, "#") {
		ref, err := entity.ParseRef(id)
		if err != nil {
			return store.GroupRow{}, false, nil
		}
		n, _, ok, err := h.cfg.Identity.NodeID(ctx, ref)
		if err != nil || !ok {
			return store.GroupRow{}, false, err
		}
		node = n
	}
	return h.cfg.Store.GetGroup(ctx, entity.KindPR, node, summary.GroupName)
}

func decode(g store.GroupRow, stale bool) (loaded, error) {
	var s summary.Summary
	if err := json.Unmarshal(g.Content, &s); err != nil {
		return loaded{}, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("decode stored summary of %s: %v", g.ID, err))
	}
	return loaded{sum: s, fetchedAt: g.FetchedAt, stale: stale}, nil
}

// inRange applies the time range to a summary's updatedAt. A PR whose timestamp
// is missing or unparseable cannot be judged: it is kept and the answer is
// flagged truncated rather than claimed exact.
func inRange(updatedAt string, r scriptout.TimeRange) (keep, imprecise bool) {
	if r.IsZero() {
		return true, false
	}
	t, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return true, true
	}
	return r.Contains(t), false
}

// toPR maps a stored summary onto the list entity, in the shape the old
// backend's list answered: state in lower case, no branch, base or merged flag,
// checks_rollup folded to success, failure, pending or none. as_of is when the
// summary was fetched and stale says it was served past its freshness.
func toPR(l loaded) schema.PR {
	s := l.sum
	pr := schema.PR{
		ID: s.Ref(), Repo: s.Repo, Number: s.Number, Title: s.Title,
		State: strings.ToLower(s.State), Author: s.Author, URL: s.URL, Draft: s.IsDraft,
		Body: s.Body, AsOf: l.fetchedAt.UTC().Format(time.RFC3339), Stale: l.stale,
		HeadSHA: s.HeadRefOid, Mergeable: s.Mergeable, ChecksRollup: github.ChecksRollupFor(s.ChecksState),
		NodeID: s.NodeID, UpdatedAt: s.UpdatedAt, ReviewDecision: s.ReviewDecision,
		CommentCount: s.CommentCount, ReviewCount: s.ReviewCount,
		ReviewThreadCount: s.ReviewThreadCount, LabelCount: s.LabelCount,
	}
	if len(s.Labels) > 0 {
		pr.Labels = s.Labels
	}
	return pr
}
