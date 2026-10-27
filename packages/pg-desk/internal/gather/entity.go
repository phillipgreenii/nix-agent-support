package gather

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// EntityGatherer is the generic per-entity-type gather seam: given an
// entity id and the change that triggered it, produce one opaque payload
// plus the metadata the pipeline persists beside it.
type EntityGatherer interface {
	GatherEntity(ctx context.Context, entityID string, change ChangeKind) (GatherResult, error)
}

// GatherResult is one EntityGatherer's output. Degraded is a non-fatal note;
// RemovedState is independent of it and is "" unless the type defines a
// removed concept (today only "not_found").
type GatherResult struct {
	Payload      json.RawMessage
	AsOf         string
	Degraded     string
	RemovedState string
	// Watched is entity_meta.watched read from the answer (issue type); nil
	// means NOT STATED.
	Watched *bool
}

// BackendEntityGatherer is the optional EntityGatherer extension that pins the
// read to one backend. GatherEntityOn runs the hydration read against backend
// (an empty backend is GatherEntity); GatherEntityWatch is the same read that
// also asks the backend to watch the entity.
type BackendEntityGatherer interface {
	GatherEntityOn(ctx context.Context, entityID, backend string, change ChangeKind) (GatherResult, error)
	GatherEntityWatch(ctx context.Context, entityID, backend string) (GatherResult, error)
}

// IssueFacts is the issue type's gather payload.
type IssueFacts struct {
	IssueShow json.RawMessage `json:"issue_show,omitempty"`
	// IssueDeps is the `issue deps <id> --full` result; only present when
	// the Gatherer's read-issue-deps switch is on.
	IssueDeps json.RawMessage `json:"issue_deps,omitempty"`
}

// ThreadFacts is the thread type's gather payload: the raw `thread show`
// result, carried as returned (the connector exposes no messages field yet).
type ThreadFacts struct {
	ThreadShow json.RawMessage `json:"thread_show,omitempty"`
}

// readIssueDeps holds the per-Gatherer read-issue-deps switch. It lives here
// rather than on the Gatherer struct because gather.go is unchanged by
// design (the PR pipeline is not refactored by the generic seam).
var readIssueDeps sync.Map // *Gatherer -> bool

// SetReadIssueDeps turns the optional `issue deps` read on or off for the
// issue adapter EntityGatherers hands out. Default is off.
func (g *Gatherer) SetReadIssueDeps(on bool) { readIssueDeps.Store(g, on) }

func (g *Gatherer) readsIssueDeps() bool {
	v, ok := readIssueDeps.Load(g)
	return ok && v.(bool)
}

// EntityGatherers returns the registry: exactly "pr", "issue" and "thread".
func (g *Gatherer) EntityGatherers() map[string]EntityGatherer {
	return map[string]EntityGatherer{
		"pr":     &prGatherAdapter{g: g},
		"issue":  &issueGatherAdapter{g: g, readDeps: g.readsIssueDeps()},
		"thread": &threadGatherAdapter{g: g},
	}
}

type prGatherAdapter struct{ g *Gatherer }

func (a *prGatherAdapter) GatherEntity(ctx context.Context, entityID string, change ChangeKind) (GatherResult, error) {
	f, err := a.g.Gather(ctx, "pr", entityID, change)
	if err != nil {
		return GatherResult{}, err
	}
	if f.RemovedState == "" && len(f.PRShow) > 0 {
		a.g.gatherReviewState(ctx, entityID, &f)
	}
	payload, err := json.Marshal(f)
	if err != nil {
		return GatherResult{}, fmt.Errorf("gather: marshal pr facts: %w", err)
	}
	return GatherResult{Payload: payload, AsOf: f.AsOf, Degraded: f.Degraded, RemovedState: f.RemovedState}, nil
}

type issueGatherAdapter struct {
	g        *Gatherer
	readDeps bool
}

func (a *issueGatherAdapter) GatherEntity(ctx context.Context, entityID string, change ChangeKind) (GatherResult, error) {
	return a.GatherEntityOn(ctx, entityID, "", change)
}

func (a *issueGatherAdapter) GatherEntityOn(ctx context.Context, entityID, backend string, _ ChangeKind) (GatherResult, error) {
	return a.gather(ctx, entityID, backend, false)
}

func (a *issueGatherAdapter) GatherEntityWatch(ctx context.Context, entityID, backend string) (GatherResult, error) {
	return a.gather(ctx, entityID, backend, true)
}

func (a *issueGatherAdapter) gather(ctx context.Context, entityID, backend string, watch bool) (GatherResult, error) {
	if entityID == "" {
		return GatherResult{}, fmt.Errorf("gather: entity id is required")
	}
	env := a.g.issueBeadsDirEnv()
	// --fresh: hydration follows a detected change, so it must not be served
	// from a connector cache entry still within its read-through TTL.
	args := []string{"issue", "show", entityID, "--fresh"}
	if backend != "" {
		args = append(args, "--backend", backend)
	}
	if watch {
		args = append(args, "--watch")
	}
	show, notFound, err := a.g.targetedCall(ctx, args, env)
	if notFound {
		return GatherResult{RemovedState: "not_found"}, nil
	}
	if err != nil {
		return GatherResult{}, fmt.Errorf("gather: fetch issue %s: %w", entityID, err)
	}
	facts := IssueFacts{IssueShow: show}
	var meta struct {
		AsOf string `json:"as_of"`
	}
	_ = json.Unmarshal(show, &meta) // best-effort
	out := GatherResult{AsOf: meta.AsOf}
	if w, stated := EntityMetaWatched(show); stated {
		out.Watched = &w
	}
	// A fresh read can still fall back to a stale cache entry when the backend
	// is unavailable: such an answer is degraded, never content.
	out.Degraded = degradedReason(show)
	if a.readDeps && out.Degraded == "" {
		deps, depsNotFound, derr := a.g.targetedCall(ctx, []string{"issue", "deps", entityID, "--full"}, env)
		if derr != nil {
			return GatherResult{}, fmt.Errorf("gather: read deps of issue %s: %w", entityID, derr)
		}
		if depsNotFound {
			return GatherResult{}, fmt.Errorf("gather: read deps of issue %s: pg-connector reports not_found", entityID)
		}
		facts.IssueDeps = deps
	}
	payload, err := json.Marshal(facts)
	if err != nil {
		return GatherResult{}, fmt.Errorf("gather: marshal issue facts: %w", err)
	}
	out.Payload = payload
	return out, nil
}

type threadGatherAdapter struct{ g *Gatherer }

func (a *threadGatherAdapter) GatherEntity(ctx context.Context, entityID string, _ ChangeKind) (GatherResult, error) {
	if entityID == "" {
		return GatherResult{}, fmt.Errorf("gather: entity id is required")
	}
	show, notFound, err := a.g.targetedCall(ctx, []string{"thread", "show", entityID}, nil)
	if notFound {
		return GatherResult{RemovedState: "not_found"}, nil
	}
	if err != nil {
		return GatherResult{}, fmt.Errorf("gather: fetch thread %s: %w", entityID, err)
	}
	payload, err := json.Marshal(ThreadFacts{ThreadShow: show})
	if err != nil {
		return GatherResult{}, fmt.Errorf("gather: marshal thread facts: %w", err)
	}
	var meta struct {
		AsOf string `json:"as_of"`
	}
	_ = json.Unmarshal(show, &meta) // best-effort
	return GatherResult{Payload: payload, AsOf: meta.AsOf}, nil
}
