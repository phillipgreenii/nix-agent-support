package client

import (
	"context"
	"maps"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Fallback supplies a local answer for an op when the daemon is down. cause is
// the *socket.DaemonDownError. The result is wrapped in the op's schema
// version; a returned error is classified like any handler error. A Fallback
// runs only for an unreachable daemon, never for a version mismatch or for a
// daemon's own error answer.
type Fallback func(ctx context.Context, req scriptout.Request, cause error) (any, error)

// Op describes one op the instance's client LISTS (reports in capabilities).
// Listing is not a gate: the client forwards every op, listed or not, and the
// daemon answers unknown_op for one nobody registered.
type Op struct {
	// SchemaVersion stamps an answer the client composes itself (an
	// unavailable for this op, a fallback result).
	SchemaVersion int
	// Fallback, when non-nil, is the op's answer while the daemon is down.
	Fallback Fallback
}

// Instance is one client mode: its name on the wire, the compiled-in op table
// capabilities answers from, and the schema versions it reports.
type Instance struct {
	Name           string
	Ops            map[string]Op
	SchemaVersions map[string]int
}

// WithFallback returns a copy of i whose op has fb as its local fallback (the
// op is listed when it was not). The fallback needs the loaded config's paths,
// which the compiled-in table does not have, so the binary registers it here
// after loading the config.
func (i Instance) WithFallback(op string, fb Fallback) Instance {
	ops := make(map[string]Op, len(i.Ops)+1)
	maps.Copy(ops, i.Ops)
	o := ops[op]
	o.Fallback = fb
	ops[op] = o
	i.Ops = ops
	return i
}

// OpNames returns the instance's listed ops, sorted, capabilities included.
func (i Instance) OpNames() []string {
	names := make([]string, 0, len(i.Ops)+1)
	seen := map[string]bool{}
	for op := range i.Ops {
		names = append(names, op)
		seen[op] = true
	}
	if !seen[scriptout.OpCapabilities] {
		names = append(names, scriptout.OpCapabilities)
	}
	sort.Strings(names)
	return names
}

// Vocabulary keys the client declares, compiled in. cache_opt_out keeps the
// umbrella's own cache and ledger diff off for this backend (its
// cacheEnabled fails OPEN when capabilities fails, hence the client answers it
// without the daemon); owns_changes says the backend owns its change feed.
const (
	VocabCacheOptOut = "cache_opt_out"
	VocabOwnsChanges = "owns_changes"
)

// Capabilities is the compiled-in capabilities answer for i: its op table, its
// schema versions, the two vocabulary flags and the build's version.
func (i Instance) Capabilities(version string) scriptout.CapabilitiesResponse {
	sv := make(map[string]int, len(i.SchemaVersions))
	maps.Copy(sv, i.SchemaVersions)
	return scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  sv,
		Ops:             i.OpNames(),
		Vocabulary: map[string]any{
			VocabCacheOptOut: true,
			VocabOwnsChanges: true,
		},
		Version: version,
	}
}

// PR is the pr instance's client table for this phase: the ops the daemon's
// handlers register so far. Later phases add the rest.
func PR() Instance {
	v := schema.PRSchemaVersion
	return Instance{
		Name: "pr",
		Ops: map[string]Op{
			scriptout.OpCapabilities: {SchemaVersion: v},
			scriptout.OpAuthStatus:   {SchemaVersion: v},
			scriptout.OpList:         {SchemaVersion: v},
			"status":                 {SchemaVersion: v},
			"explain":                {SchemaVersion: v},
		},
		SchemaVersions: map[string]int{"pr": schema.PRSchemaVersion},
	}
}

// CI is the ci instance's client table for this phase.
func CI() Instance {
	v := schema.CISchemaVersion
	return Instance{
		Name: "ci",
		Ops: map[string]Op{
			scriptout.OpCapabilities: {SchemaVersion: v},
			scriptout.OpAuthStatus:   {SchemaVersion: v},
			"status":                 {SchemaVersion: v},
			"explain":                {SchemaVersion: v},
		},
		SchemaVersions: map[string]int{"ci": schema.CISchemaVersion},
	}
}
