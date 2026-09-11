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

// pinBackend validates that pinned (a caller-supplied --backend value; ""
// means no pin) actually names one of the registered backends, returning
// it unchanged, or a CLI-level error naming what IS registered
// (bead pg2-2j5ac.28.1, design's "id-less op rule").
func pinBackend(entityType string, backends []string, pinned string) (string, error) {
	for _, b := range backends {
		if b == pinned {
			return pinned, nil
		}
	}
	return "", fmt.Errorf("dispatch: --backend %q is not registered for connector.%s (registered: %v)", pinned, entityType, backends)
}

// invokeOne is the shared "resolve this backend's own config, then Invoke"
// step every dispatch path in this file goes through, so a backend's
// registered backends.<binary> config block (registry.go's BackendConfig)
// is attached to every request the SAME way regardless of which of
// Dispatch/DispatchTargeted resolved it (bead pg2-2j5ac.28.1, design:
// "the umbrella copies a registered backend's ... config block VERBATIM
// into every request to that backend").
func invokeOne(ctx context.Context, reg *Registry, binary, op string, args any) (*scriptout.Response, error) {
	config, err := reg.BackendConfig(binary)
	if err != nil {
		return nil, err
	}
	return scriptout.Invoke(ctx, binary, op, args, config)
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
// for the ordinary (unpinned) case, and is used by the id-less writes
// routed through it (issue create). The multi-instance try-each
// resolution policy is implemented by DispatchTargeted below instead,
// scoped to id-keyed targeted ops only, by this phase's own operator
// ruling: the narrower question of what create (or any other id-less
// write) SHOULD eventually do at N > 1 with no --backend given is
// explicitly out of scope here, handed to a separate bead to resolve.
//
// pinned ("" for no pin) implements design's "id-less op rule": an
// id-less op on a type with more than one registered backend MUST require
// --backend when the op cannot fan out meaningfully (create is exactly
// this case) — a non-empty pinned here skips the N > 1 hard-fail entirely
// and dispatches straight to that one validated backend, whether N is 1
// or many.
func Dispatch(ctx context.Context, reg *Registry, entityType, op string, args any, pinned string) (*scriptout.Response, error) {
	backends, err := resolveBackends(reg, entityType)
	if err != nil {
		return nil, err
	}
	if pinned != "" {
		b, pinErr := pinBackend(entityType, backends, pinned)
		if pinErr != nil {
			return nil, pinErr
		}
		return invokeOne(ctx, reg, b, op, args)
	}
	if len(backends) > 1 {
		return nil, fmt.Errorf("dispatch: %d backends registered for connector.%s; targeted-op resolution needs exactly one, or an explicit --backend to pin one (multi-backend selection with no pin is a future concern)", len(backends), entityType)
	}
	return invokeOne(ctx, reg, backends[0], op, args)
}

// DispatchTargeted implements this docket's multi-instance targeted-op
// resolution policy for id-keyed ops only: issue show/comment/transition,
// ci get_logs/rerun_failed, and pr show/files/commits. For a
// targeted op against a list-valued connector.<type> with N > 1
// registered backends and no --backend pin, it tries each registered
// backend in registration order (resolveBackends' own returned order),
// stopping at the first answer that is not not_found. Any other error
// short-circuits immediately and is returned as-is, never swallowed to
// fall through to the next backend. If every registered backend answers
// not_found, the aggregate targeted-op result is that not_found answer.
//
// With exactly one registered backend, this resolves identically to
// Dispatch's own single-backend path — the loop runs once and returns
// that backend's answer, not_found included — so a targeted op's
// single-backend behavior is unaffected by this function's introduction.
// Dispatch's existing len(backends) == 0 ("no backend registered") case is
// also unaffected, via the shared resolveBackends helper above.
//
// pinned ("" for no pin) skips the try-each loop entirely and dispatches
// straight to that one validated backend — design's "--backend on
// EVERY Tier-1 verb" bullet, threaded onto every id-keyed targeted op the
// same way Dispatch's own pinned path handles the id-less case above.
func DispatchTargeted(ctx context.Context, reg *Registry, entityType, op string, args any, pinned string) (*scriptout.Response, error) {
	backends, err := resolveBackends(reg, entityType)
	if err != nil {
		return nil, err
	}
	if pinned != "" {
		b, pinErr := pinBackend(entityType, backends, pinned)
		if pinErr != nil {
			return nil, pinErr
		}
		return invokeOne(ctx, reg, b, op, args)
	}
	var resp *scriptout.Response
	var callErr error
	for _, b := range backends {
		resp, callErr = invokeOne(ctx, reg, b, op, args)
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
