package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// resolveBackends returns the ordered list of backend binary names
// registered under connector.<entityType> (reg.List's own returned order,
// already the config file's own written order — this is what
// DispatchTargeted below treats as "registration order"), or an error if
// none are registered. Shared by Dispatch and DispatchTargeted so the
// no-backend-registered failure text — and behavior — can never drift
// between the id-less and id-keyed dispatch paths: neither this packet's
// multi-instance resolution policy nor its id-keyed-only scoping changes
// this len(backends) == 0 case for any op.
func resolveBackends(reg *Registry, entityType string) ([]string, error) {
	backends, err := reg.List(entityType)
	if err != nil {
		return nil, err
	}
	if len(backends) == 0 {
		return nil, fmt.Errorf("dispatch: no backend registered for connector.%s", entityType)
	}
	return backends, nil
}

// Dispatch calls op on the one backend registered for entityType, generic
// over capability: it resolves the registered backend binary name via reg,
// then invokes pkg/scriptout's caller-side Invoke against it. This is the
// seam a sibling packet's own CLI verb group calls into rather than
// re-implementing its own exec/dispatch path, operationalizing the
// registry and wire protocol together as the umbrella's own call path to a
// registered backend.
//
// Dispatch stays exactly this — hard-fail at N > 1 registered backends —
// and is now used ONLY by the one id-less write routed through it (issue
// create). The multi-instance try-each resolution policy is implemented
// by DispatchTargeted below instead, scoped to id-keyed targeted ops
// only, by this phase's own operator ruling: the narrower question of
// what create (or any other id-less write) SHOULD eventually do at N > 1
// is explicitly out of scope here, handed to a separate bead to resolve.
func Dispatch(ctx context.Context, reg *Registry, entityType, op string, args any) (*scriptout.Response, error) {
	backends, err := resolveBackends(reg, entityType)
	if err != nil {
		return nil, err
	}
	if len(backends) > 1 {
		return nil, fmt.Errorf("dispatch: %d backends registered for connector.%s; targeted-op resolution needs exactly one (multi-backend selection is a future concern)", len(backends), entityType)
	}
	return scriptout.Invoke(ctx, backends[0], op, args)
}

// DispatchTargeted implements this docket's multi-instance targeted-op
// resolution policy for id-keyed ops only: issue show/comment/transition,
// ci get_logs/rerun_failed, and pr show/categorize/feedback_set. For a
// targeted op against a list-valued connector.<type> with N > 1
// registered backends, it tries each registered backend in registration
// order (resolveBackends' own returned order), stopping at the first
// answer that is not not_found. Any other error short-circuits
// immediately and is returned as-is, never swallowed to fall through to
// the next backend. If every registered backend answers not_found, the
// aggregate targeted-op result is that not_found answer.
//
// With exactly one registered backend, this resolves identically to
// Dispatch's own single-backend path — the loop runs once and returns
// that backend's answer, not_found included — so a targeted op's
// single-backend behavior is unaffected by this function's introduction.
// Dispatch's existing len(backends) == 0 ("no backend registered") case is
// also unaffected, via the shared resolveBackends helper above.
func DispatchTargeted(ctx context.Context, reg *Registry, entityType, op string, args any) (*scriptout.Response, error) {
	backends, err := resolveBackends(reg, entityType)
	if err != nil {
		return nil, err
	}
	var resp *scriptout.Response
	var callErr error
	for _, b := range backends {
		resp, callErr = scriptout.Invoke(ctx, b, op, args)
		if callErr == nil || !errors.Is(callErr, scriptout.ErrNotFound) {
			// Success, or any error other than not_found: short-circuit
			// immediately and return as-is — never swallowed to fall
			// through to the next backend.
			return resp, callErr
		}
		// not_found: try the next registered backend.
	}
	// Every registered backend answered not_found: that is the aggregate
	// targeted-op result.
	return resp, callErr
}
