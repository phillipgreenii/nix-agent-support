package cmddesc

// remoteMutationOps is the vocabulary of EffectRemote Operations that MUTATE
// the remote (as opposed to a future read-only operation, not yet needed by
// any schema). It is what TransformDryRun consults to know which remote
// effects a dry run removes, and it is open: a future Operation spelling that
// mutates must be added here, or dry-run will fail OPEN and leave it in —
// which is why every Operation this slice's schemas can produce ("push",
// "force-push", "delete-ref") is listed.
var remoteMutationOps = map[string]bool{"push": true, "force-push": true, "delete-ref": true}

// applyTransform rewrites effects under t by effect shape alone. It reports
// false for a Kind it does not know so the caller fails closed instead of
// passing the effects through unchanged.
func applyTransform(t EffectTransform, effects []Effect) ([]Effect, bool) {
	switch t.Kind {
	case TransformNone:
		return effects, true
	case TransformDryRun:
		out := make([]Effect, 0, len(effects))
		for _, e := range effects {
			if e.Kind == EffectPath && e.Access.IsWrite() {
				continue
			}
			if e.Kind == EffectRemote && remoteMutationOps[e.Operation] {
				continue
			}
			out = append(out, e)
		}
		return out, true
	case TransformInPlace:
		out := make([]Effect, len(effects))
		for i, e := range effects {
			if e.Kind == EffectPath && e.Access == AccessRead && e.FromPositional {
				e.Access = AccessModify
			}
			out[i] = e
		}
		return out, true
	case TransformNoClobber:
		return retarget(effects, AccessTruncate, AccessCreate), true
	case TransformAppend:
		return retarget(effects, AccessTruncate, AccessModify), true
	case TransformForce:
		return retargetRemote(effects, "push", "force-push"), true
	case TransformDeleteRef:
		return retargetRemote(effects, "push", "delete-ref"), true
	default:
		return effects, false
	}
}

// retarget copies effects, rewriting every path effect of access class from
// to class to.
func retarget(effects []Effect, from, to PathAccess) []Effect {
	out := make([]Effect, len(effects))
	for i, e := range effects {
		if e.Kind == EffectPath && e.Access == from {
			e.Access = to
		}
		out[i] = e
	}
	return out
}

// retargetRemote copies effects, rewriting every EffectRemote of Operation
// from to Operation to.
func retargetRemote(effects []Effect, from, to string) []Effect {
	out := make([]Effect, len(effects))
	for i, e := range effects {
		if e.Kind == EffectRemote && e.Operation == from {
			e.Operation = to
		}
		out[i] = e
	}
	return out
}
