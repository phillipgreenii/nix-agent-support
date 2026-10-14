package diag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Queued is one pull request of a refresh answer.
type Queued struct {
	// ID is the pull request's canonical owner/repo#n (its node id when the alias
	// table holds no reference for it).
	ID string `json:"id"`
	// Groups are the groups queued, in the order of the spec's field-group table.
	Groups []string `json:"groups"`
}

// RefreshResult is the answer of refresh.
type RefreshResult struct {
	Queued []Queued `json:"queued"`
}

// DefaultPRGroups are the groups pr refresh queues when none is named: every
// group but pending, which is queued only when named.
func DefaultPRGroups() []string {
	return []string{groups.Summary, groups.Detail, groups.Conversation, groups.Files, groups.Commits, groups.Runs}
}

// DefaultCIGroups are the groups ci refresh queues: the summary (the runs fetch
// reads the stored summary's head, so the summary is refreshed with it) and runs.
func DefaultCIGroups() []string { return []string{groups.Summary, groups.Runs} }

// RefreshHandlers returns the refresh handler of each instance, keyed by instance
// and then op: pr/refresh with args {id, query, groups} (exactly one of id or
// query) and ci/refresh with args {pr_id}. Each is stamped with its instance's
// schema version.
func RefreshHandlers(deps Deps) map[string]map[string]scriptout.OpHandler {
	h := &refresher{deps: deps}
	return map[string]map[string]scriptout.OpHandler{
		"pr": {OpRefresh: {SchemaVersion: schema.PRSchemaVersion, Handle: h.handlePR}},
		"ci": {OpRefresh: {SchemaVersion: schema.CISchemaVersion, Handle: h.handleCI}},
	}
}

type refresher struct{ deps Deps }

type prArgs struct {
	ID     string   `json:"id"`
	Query  string   `json:"query"`
	Groups []string `json:"groups"`
}

type ciArgs struct {
	PRID string `json:"pr_id"`
}

func (r *refresher) handlePR(ctx context.Context, raw json.RawMessage) (any, error) {
	var a prArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode refresh args: "+err.Error())
	}
	a.ID, a.Query = strings.TrimSpace(a.ID), strings.TrimSpace(a.Query)
	if (a.ID == "") == (a.Query == "") {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, `refresh needs exactly one of the arguments "id" (owner/repo#n) and "query"`)
	}
	want, err := groupSet(a.Groups, DefaultPRGroups())
	if err != nil {
		return nil, err
	}
	if a.Query != "" && !r.deps.Config.HasQuery(a.Query) {
		return nil, scriptout.WrapError(scriptout.ErrQueryNotRecognized, fmt.Sprintf("%q is not a configured query", a.Query))
	}
	router.Annotate(ctx, "store", false, 0, 0)
	if a.Query != "" {
		return r.refreshQuery(ctx, a.Query, want)
	}
	q, err := r.refreshOne(ctx, a.ID, want)
	if err != nil {
		return nil, err
	}
	return RefreshResult{Queued: []Queued{q}}, nil
}

func (r *refresher) handleCI(ctx context.Context, raw json.RawMessage) (any, error) {
	var a ciArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode refresh args: "+err.Error())
	}
	a.PRID = strings.TrimSpace(a.PRID)
	if a.PRID == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, `refresh needs the argument "pr_id" (owner/repo#n)`)
	}
	router.Annotate(ctx, "store", false, 0, 0)
	q, err := r.refreshOne(ctx, a.PRID, DefaultCIGroups())
	if err != nil {
		return nil, err
	}
	return RefreshResult{Queued: []Queued{q}}, nil
}

// groupSet validates the named groups and returns them in the order of
// groups.All, de-duplicated; none named means def. A name outside the closed set
// is invalid_argument naming the valid set.
func groupSet(named, def []string) ([]string, error) {
	if len(named) == 0 {
		return def, nil
	}
	have := map[string]bool{}
	for _, n := range named {
		n = strings.TrimSpace(n)
		valid := false
		for _, g := range groups.All() {
			valid = valid || g == n
		}
		if !valid {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
				fmt.Sprintf("unknown group %q; the valid groups are %s", n, strings.Join(groups.All(), ", ")))
		}
		have[n] = true
	}
	var out []string
	for _, g := range groups.All() {
		if have[g] {
			out = append(out, g)
		}
	}
	return out, nil
}

// refreshOne resolves one pull request reference (at most one origin call, for
// an unseen reference), records an access, and invalidates and queues the
// groups. A pull request with no stored summary only has its summary queued.
func (r *refresher) refreshOne(ctx context.Context, id string, want []string) (Queued, error) {
	node, ref, err := r.deps.Engine.Resolve(ctx, id)
	if err != nil {
		return Queued{}, wireErr("refresh "+id, err)
	}
	if err := r.deps.Summaries.Access(ctx, node); err != nil {
		return Queued{}, wireErr("refresh "+id, err)
	}
	return r.queue(ctx, node, ref, want)
}

// queue invalidates and queues the groups of an entity the store has a row for.
func (r *refresher) queue(ctx context.Context, node string, ref entity.Ref, want []string) (Queued, error) {
	out := Queued{ID: node, Groups: []string{}}
	if ref != (entity.Ref{}) {
		out.ID = ref.String()
	}
	have, err := r.hasSummary(ctx, node)
	if err != nil {
		return Queued{}, wireErr("refresh "+out.ID, err)
	}
	for _, g := range want {
		if !have && g != groups.Summary {
			continue // skipped by the executor until a summary exists
		}
		if err := r.deps.Engine.InvalidateAndQueueAs(ctx, node, g, groupsvc.OriginRefresh); err != nil {
			return Queued{}, wireErr("refresh "+out.ID, err)
		}
		out.Groups = append(out.Groups, g)
	}
	return out, nil
}

func (r *refresher) hasSummary(ctx context.Context, node string) (bool, error) {
	row, ok, err := r.deps.Store.GetGroup(ctx, entity.KindPR, node, groups.Summary)
	if err != nil {
		return false, err
	}
	return ok && len(row.Content) > 0, nil
}

// refreshQuery queues the re-run of the configured query at class 2 and the
// groups of every id currently stored for it. Members the re-run finds are
// picked up by the runner's own Track.
func (r *refresher) refreshQuery(ctx context.Context, name string, want []string) (any, error) {
	key := query.Key{Name: name}
	if err := r.deps.Queries.QueueRun(key, governor.ClassInvalidated); err != nil {
		return nil, wireErr("refresh query "+name, err)
	}
	res := RefreshResult{Queued: []Queued{}}
	row, ok, err := r.deps.Queries.Row(ctx, key)
	if err != nil {
		return nil, wireErr("refresh query "+name, err)
	}
	if !ok {
		return res, nil
	}
	for _, id := range row.IDs {
		node, ref, known, err := r.member(ctx, id)
		if err != nil {
			return nil, wireErr("refresh query "+name, err)
		}
		if !known {
			continue
		}
		q, err := r.queue(ctx, node, ref, want)
		if err != nil {
			return nil, err
		}
		res.Queued = append(res.Queued, q)
	}
	return res, nil
}

// member finds the node id and reference of a stored query member (a reference
// or a node id) without an origin call; known is false for a member the alias
// table or the entity table does not hold.
func (r *refresher) member(ctx context.Context, id string) (string, entity.Ref, bool, error) {
	var node string
	var ref entity.Ref
	if strings.Contains(id, "#") {
		parsed, perr := entity.ParseRef(id)
		if perr != nil {
			return "", entity.Ref{}, false, nil
		}
		n, canon, ok, err := r.deps.Aliases.NodeID(ctx, parsed)
		if err != nil || !ok {
			return "", entity.Ref{}, false, err
		}
		node, ref = n, canon
	} else {
		var err error
		if ref, _, err = r.deps.Aliases.Ref(ctx, id); err != nil {
			return "", entity.Ref{}, false, err
		}
		node = id
	}
	_, found, err := r.deps.Store.GetEntity(ctx, entity.KindPR, node)
	if err != nil || !found {
		return "", entity.Ref{}, false, err
	}
	return node, ref, true, nil
}
