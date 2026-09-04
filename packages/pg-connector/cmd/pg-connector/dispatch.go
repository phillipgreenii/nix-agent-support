package main

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Dispatch calls op on the one backend registered for entityType, generic
// over capability: it resolves the registered backend binary name via reg,
// then invokes pkg/scriptout's caller-side Invoke against it. This is the
// seam a sibling packet's own CLI verb group calls into rather than
// re-implementing its own exec/dispatch path, operationalizing the
// registry and wire protocol together as the umbrella's own call path to a
// registered backend.
//
// Targeted-op backend resolution: this docket's own scope never registers
// more than one backend under any list-valued connector.<type> entry, so
// Dispatch only needs to resolve unambiguously when exactly one backend is
// registered. Selecting among multiple simultaneously-registered same-type
// backends for a targeted op is left as a freedom-boundary/future concern.
func Dispatch(ctx context.Context, reg *Registry, entityType, op string, args any) (*scriptout.Response, error) {
	backends, err := reg.List(entityType)
	if err != nil {
		return nil, err
	}
	if len(backends) == 0 {
		return nil, fmt.Errorf("dispatch: no backend registered for connector.%s", entityType)
	}
	if len(backends) > 1 {
		return nil, fmt.Errorf("dispatch: %d backends registered for connector.%s; targeted-op resolution needs exactly one (multi-backend selection is a future concern)", len(backends), entityType)
	}
	return scriptout.Invoke(ctx, backends[0], op, args)
}
