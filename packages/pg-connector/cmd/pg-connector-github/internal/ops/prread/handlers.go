// Package prread is the pr instance's four read ops served from the daemon's
// store: show (the summary, detail and conversation groups), files (files),
// commits (commits) and review_pending (pending). It is the spec's "Entity read"
// for a single PR.
//
// Each op takes the wire id owner/repo#n (matched case-insensitively on
// owner/repo by the engine's alias table) and the wire argument fresh, asks the
// group engine (groupsvc.Service.Ensure) for the groups the op reads, and
//
//   - answers from the store with no origin call when every group is inside its
//     freshness;
//   - otherwise has the engine fetch the stale groups at interactive priority and
//     wait up to the request's deadline, serving the stale content inside expiry
//     with stale: true, and answering unavailable (naming the cap, the reserve or
//     the origin failure) when no usable row exists;
//   - with fresh: true forces every needed group to origin.
//
// The answers have the result shapes and error codes of the old backend
// (schema.PR, schema.PRFilesResult, schema.PRCommitsResult,
// pr.PendingReviewResult) plus the annotations of internal/answer (served_from,
// stale, age_seconds, groups[]), which are also recorded on the router's
// carrier. Handlers returns the op handlers as values; registering them under
// the router is the daemon assembly's.
package prread

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/answer"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/conversation"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/pending"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/prfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The four op names.
const (
	OpShow          = "show"
	OpFiles         = "files"
	OpCommits       = "commits"
	OpReviewPending = "review_pending"
)

// Ops lists the op names Handlers provides, for the assembly packet's drift
// guard.
var Ops = []string{OpShow, OpFiles, OpCommits, OpReviewPending}

// Engine is the group engine as the handlers use it. *groupsvc.Service
// satisfies it.
type Engine interface {
	Ensure(ctx context.Context, id string, want []string, opt groupsvc.EnsureOptions) (groupsvc.EnsureResult, error)
}

var _ Engine = (*groupsvc.Service)(nil)

// Deps wires the handlers.
type Deps struct {
	Engine Engine
	// Sidecar supplies review_pending's last_append; nil means none.
	Sidecar *pending.SidecarView
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Handlers returns the four read ops, each at schema.PRSchemaVersion.
func Handlers(deps Deps) map[string]scriptout.OpHandler {
	h := &handlers{deps: deps}
	if h.deps.Clock == nil {
		h.deps.Clock = governor.SystemClock{}
	}
	mk := func(f func(context.Context, request) (any, error)) scriptout.OpHandler {
		return scriptout.OpHandler{SchemaVersion: schema.PRSchemaVersion, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			a, err := decodeRequest(raw)
			if err != nil {
				return nil, err
			}
			if h.deps.Engine == nil {
				return nil, scriptout.WrapError(scriptout.ErrUnavailable, "the group engine is not available")
			}
			return f(ctx, a)
		}}
	}
	return map[string]scriptout.OpHandler{
		OpShow:          mk(h.show),
		OpFiles:         mk(h.files),
		OpCommits:       mk(h.commits),
		OpReviewPending: mk(h.reviewPending),
	}
}

type handlers struct{ deps Deps }

func (h *handlers) now() time.Time { return h.deps.Clock.Now() }

// request is the wire argument of all four ops. Unknown extra keys are ignored,
// as in the old handlers.
type request struct {
	ID    string `json:"id"`
	Fresh bool   `json:"fresh"`
}

func decodeRequest(raw json.RawMessage) (request, error) {
	var a request
	if err := scriptout.Decode(raw, &a); err != nil {
		return request{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode args: "+err.Error())
	}
	if _, err := entity.ParseRef(a.ID); err != nil {
		return request{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("id %q is not in \"<owner>/<repo>#<number>\" form", a.ID))
	}
	return a, nil
}

// ensure asks the engine for want and refuses the whole answer when any group is
// unavailable (the engine already serves a stored row inside expiry as Stale, so
// a group is Unavailable only when no usable row exists) or the PR is gone.
func (h *handlers) ensure(ctx context.Context, a request, want []string) (groupsvc.EnsureResult, error) {
	res, err := h.deps.Engine.Ensure(ctx, a.ID, want, groupsvc.EnsureOptions{Fresh: a.Fresh})
	if err != nil {
		return groupsvc.EnsureResult{}, engineError(a.ID, err)
	}
	if res.Summary.Gone {
		return groupsvc.EnsureResult{}, scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("no such pull request: %s", a.ID))
	}
	for _, g := range res.Groups {
		if g.Status == groupsvc.Unavailable {
			return groupsvc.EnsureResult{}, groupError(g.Group, g.Err)
		}
	}
	return res, nil
}

// byGroup finds a group's result.
func byGroup(res groupsvc.EnsureResult, group string) (groupsvc.GroupResult, bool) {
	for _, g := range res.Groups {
		if g.Group == group {
			return g, true
		}
	}
	return groupsvc.GroupResult{}, false
}

// provenance computes the annotations of an answer and records them on the
// router's carrier.
func (h *handlers) provenance(ctx context.Context, res groupsvc.EnsureResult) answer.Provenance {
	p := answer.From(res.Groups, h.now())
	answer.Record(ctx, p, 0)
	return p
}

// corrupt is the error of a stored group that cannot be decoded.
func corrupt(group string, err error) error {
	return scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("the stored %s group is unreadable: %v", group, err))
}

func (h *handlers) show(ctx context.Context, a request) (any, error) {
	res, err := h.ensure(ctx, a, []string{summary.GroupName, groups.Detail, groups.Conversation})
	if err != nil {
		return nil, err
	}
	dg, _ := byGroup(res, groups.Detail)
	d, err := detail.Decode(dg.Row.Content)
	if err != nil {
		return nil, corrupt(groups.Detail, err)
	}
	cg, _ := byGroup(res, groups.Conversation)
	c, err := conversation.Decode(cg.Row.Content)
	if err != nil {
		return nil, corrupt(groups.Conversation, err)
	}
	out := ToSchemaPR(a.ID, res.Summary, &d, &c, h.now())
	answer.ApplyPR(out, h.provenance(ctx, res))
	return out, nil
}

func (h *handlers) files(ctx context.Context, a request) (any, error) {
	res, err := h.ensure(ctx, a, []string{groups.Files})
	if err != nil {
		return nil, err
	}
	g, _ := byGroup(res, groups.Files)
	f, err := prfacts.DecodeFiles(g.Row.Content)
	if err != nil {
		return nil, corrupt(groups.Files, err)
	}
	out := &schema.PRFilesResult{ID: a.ID, Files: f.Files}
	answer.ApplyFiles(out, h.provenance(ctx, res))
	return out, nil
}

func (h *handlers) commits(ctx context.Context, a request) (any, error) {
	res, err := h.ensure(ctx, a, []string{groups.Commits})
	if err != nil {
		return nil, err
	}
	g, _ := byGroup(res, groups.Commits)
	c, err := prfacts.DecodeCommits(g.Row.Content)
	if err != nil {
		return nil, corrupt(groups.Commits, err)
	}
	out := &schema.PRCommitsResult{ID: a.ID, Commits: c.Commits}
	answer.ApplyCommits(out, h.provenance(ctx, res))
	return out, nil
}

// reviewPending is fail-closed like the old op: ANY failure of the pending group
// (no row inside expiry, a failed fetch even when an older row exists, a PR that
// is gone) is an error whose message begins "review_pending: detection_failed:",
// never a "no pending review" answer. The live head it judges the review against
// is the stored summary's headRefOid.
func (h *handlers) reviewPending(ctx context.Context, a request) (any, error) {
	res, err := h.deps.Engine.Ensure(ctx, a.ID, []string{groups.Pending}, groupsvc.EnsureOptions{Fresh: a.Fresh})
	if err != nil {
		return nil, pendingError(err)
	}
	g, ok := byGroup(res, groups.Pending)
	switch {
	case res.Summary.Gone:
		return nil, pendingError(groupsvc.ErrNoSuchPR)
	case !ok || g.Status == groupsvc.Unavailable || (g.Status == groupsvc.Stale && g.Err != nil):
		var cause error
		if ok {
			cause = g.Err
		}
		return nil, pendingError(cause)
	}
	data, err := pending.Decode(g.Row.Content)
	if err != nil {
		return nil, pendingError(err)
	}
	ref, _ := entity.ParseRef(a.ID) // decodeRequest accepted it
	repo, number := ref.Owner+"/"+ref.Repo, ref.Number
	if res.Summary.Repo != "" && res.Summary.Number > 0 {
		repo, number = res.Summary.Repo, res.Summary.Number
	}
	last := h.deps.Sidecar.Last(repo, number)
	out, err := pending.Compute(data, res.Summary.HeadRefOid, repo, number, g.Row.FetchedAt, last)
	if err != nil {
		return nil, err
	}
	answer.ApplyPending(&out, h.provenance(ctx, res))
	return out, nil
}
