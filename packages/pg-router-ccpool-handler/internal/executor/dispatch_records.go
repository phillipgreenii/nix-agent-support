package executor

import "sort"

// The event-log kinds of the dispatch records (INV-CCH-35, INV-CCA-10,
// INV-CMA-10). dispatch_start is written per ACCEPTED dispatch (the executor's
// own, below), dispatch_decline per pre-accept decline (the cmd layer's, which
// owns the busyDecline value).
const (
	// DispatchStartKind is the eventlog kind of the per-accepted-dispatch record.
	DispatchStartKind = "dispatch_start"
	// DispatchDeclineKind is the eventlog kind of the per-decline record.
	DispatchDeclineKind = "dispatch_decline"
)

// The two launch routes a ccpool-backed dispatch_start records. ccpool new does
// not report ccpool's internal route to its caller, so the record names the
// route the HANDLER took.
const (
	// RouteLaunched: this dispatch launched a fresh session through ccpool new
	// and it returned ready.
	RouteLaunched = "launched"
	// RouteAdopted: this dispatch adopted an already-live session of the same
	// event's turn instead of launching one (INV-CCH-30).
	RouteAdopted = "adopted"
)

// sessionEnvField returns env as the record's session_env value: never nil, so
// the field is present (and an empty object) for a role with no sessionEnv.
// sessionEnv carries no secret by INV-CCH-33, so values are written as-is.
func sessionEnvField(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

// emitCommandStart writes the dispatch_start record of an accepted command-role
// dispatch: the rendered argv, whether the strict switch is on and the base-env
// variable NAMES (nil when the switch is off). It never writes an env value.
func (r *commandRun) emitCommandStart(d DispatchContext, argv, baseNames []string) {
	if r.deps.Log == nil {
		return
	}
	names := append([]string{}, baseNames...)
	sort.Strings(names)
	_ = r.deps.Log.Emit("info", DispatchStartKind, "dispatch accepted: command payload ran",
		map[string]any{
			"role": d.Role.Name, "item_id": d.Item.ID, "item_type": d.Item.Type,
			"argv": argv, "strict_base_env": d.Role.StrictBaseEnv, "base_env_names": names,
		})
}

// emitCCPoolStart writes the dispatch_start record of an accepted ccpool-role
// dispatch: the route the handler took (RouteLaunched or RouteAdopted), the
// session's external id and the role's sessionEnv entries, values included.
// The claude env itself is the prefix tool's: an operator follows
// session_env.PG_WRAP_SPEC to pg-wrap show.
func (r *ccpoolRun) emitCCPoolStart(d DispatchContext, route, session string) {
	if r.deps.Log == nil {
		return
	}
	_ = r.deps.Log.Emit("info", DispatchStartKind, "dispatch accepted: ccpool session ready",
		map[string]any{
			"role": d.Role.Name, "item_id": d.Item.ID, "item_type": d.Item.Type,
			"route": route, "session": session, "session_env": sessionEnvField(d.Role.SessionEnv),
		})
}
