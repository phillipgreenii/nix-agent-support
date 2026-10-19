// fresh.go: the umbrella half of --fresh on the read verbs.
//
// --fresh asks for an answer no older than the caller's own out-of-band write.
// The umbrella has no read-through on files, commits, review pending, list or
// ci list, so for a backend that keeps no cache of its own the flag has no
// effect there (pr show and issue show keep bypassing the umbrella
// read-through, cache_policy.go's dispatchShow). A backend that declares the
// cache_opt_out capability holds its own cache and freshness, so the flag is
// forwarded to it as the request argument fresh: true and the backend decides
// how current its answer is.
//
// The rule is per BACKEND, never per name and never per call: the argument is
// added only when the flag is set AND the receiving backend's own capabilities
// declare cache_opt_out (backendOptedOut). The key is omitted, never sent as
// false, so a request to any other backend is byte-identical to one made
// without the flag, and an unset flag makes no capabilities call at all.
package main

import (
	"context"
	"maps"

	"github.com/spf13/cobra"
)

// freshArgKey is the request argument a cache_opt_out backend reads.
const freshArgKey = "fresh"

// withFresh returns the request arguments to send to backend: args itself when
// fresh is unset or the backend does not declare cache_opt_out (a failed
// capabilities call reads as "not declared", so an unreachable probe never
// adds the argument), otherwise a copy of args with fresh: true. args is never
// modified.
func withFresh(ctx context.Context, reg *Registry, backend string, args map[string]any, fresh bool) map[string]any {
	if !fresh || !backendDeclaresCacheOptOut(ctx, reg, backend) {
		return args
	}
	out := make(map[string]any, len(args)+1)
	maps.Copy(out, args)
	out[freshArgKey] = true
	return out
}

// freshBackends returns the set of backends in backends that declare
// cache_opt_out, probing them in parallel (capabilitiesAll), when fresh is set;
// it returns nil without any capabilities call when fresh is unset. A backend
// whose probe failed is not in the set.
func freshBackends(ctx context.Context, reg *Registry, backends []string, fresh bool) map[string]bool {
	if !fresh || len(backends) == 0 {
		return nil
	}
	set := make(map[string]bool, len(backends))
	for i, caps := range capabilitiesAll(ctx, reg, backends) {
		if caps != nil && backendOptedOut(caps.Vocabulary) {
			set[backends[i]] = true
		}
	}
	return set
}

// freshArgsFor builds the per-backend argument function of a fan-out: base
// returns the arguments a backend would get without the flag, and a backend in
// optedOut (freshBackends' result) additionally gets fresh: true. A nil
// optedOut yields base's arguments unchanged for every backend.
func freshArgsFor(optedOut map[string]bool, base func(backend string) map[string]any) func(string) any {
	return func(b string) any {
		args := base(b)
		if !optedOut[b] {
			return args
		}
		out := make(map[string]any, len(args)+1)
		maps.Copy(out, args)
		out[freshArgKey] = true
		return out
	}
}

// addFreshReadFlag registers --fresh on a read verb that has no read-through of
// its own (pr files, pr commits, pr review pending, pr list, ci list) and
// returns the flag's value. Its effect is the forwarding in this file; it
// changes no exit code, outcome shape or output.
func addFreshReadFlag(cmd *cobra.Command) *bool {
	return cmd.Flags().Bool("fresh", false, "ask a backend that keeps its own cache for an answer fetched from the origin now "+
		"(forwarded as fresh: true; no effect on a backend without its own cache). "+
		"For reading back the caller's own out-of-band write; polling paths SHOULD NOT use it")
}
