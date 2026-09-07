package cmddesc

// remoteMutationOps is the vocabulary of EffectRemote Operations that MUTATE
// the remote (as opposed to a future read-only operation, not yet needed by
// any schema). It is what TransformDryRun consults to know which remote
// effects a dry run marks, and it is open: a future Operation spelling that
// mutates must be added here, or dry-run will fail OPEN and leave it
// unmarked — which is why every Operation this slice's schemas can produce
// ("push", "force-push", "delete-ref") is listed.
var remoteMutationOps = map[string]bool{"push": true, "force-push": true, "delete-ref": true}

// applyTransform rewrites effects under t by effect shape alone. It reports
// false for a Kind it does not know so the caller fails closed instead of
// passing the effects through unchanged.
func applyTransform(t EffectTransform, effects []Effect) ([]Effect, bool) {
	switch t.Kind {
	case TransformNone:
		return effects, true
	case TransformDryRun:
		// Path writes are still DROPPED outright: a dry run of a local write
		// (e.g. `sed -i -n`-shaped tooling, were it ever schema'd that way)
		// genuinely touches no file, and no schema here has a FORBIDDEN class
		// of path write that a dry run must not silently clear — unlike
		// EffectRemote below, there is nothing to preserve.
		//
		// EffectRemote mutations are MARKED, never dropped — operator ruling
		// (Phillip, 2026-09-07, verbatim, recorded on tc-ife3/tc-vn5z): "git
		// push force shiuld be abstain with -n as nothong happens." A dry run
		// of an ordinary "push" still resolves to Approve (RemoteMutation
		// treats DryRun-marked "push" as Permitted, same net effect as the
		// old removal), but a dry run of a FORBIDDEN-class operation
		// (force-push, delete-ref) must not silently vanish into an
		// unconditional Approve either — nothing happens, so Reject is wrong,
		// but auto-approving a forbidden mutation's dry run is wrong too, so
		// RemoteMutation abstains (Unknown) for that case. See its doc
		// comment for the per-Operation verdicts.
		//
		// Marking rather than deleting also makes this ORDER-INDEPENDENT
		// under the interpreter's flag-order application (result() applies
		// each flag's transform in the order it appeared on the command
		// line): retargetRemote (TransformForce/TransformDeleteRef) rewrites
		// only Operation, never DryRun, so `--force -n` (retarget then mark)
		// and `-n --force` (mark then retarget) both end at the same final
		// (Operation="force-push", DryRun=true) pair. Deleting the effect
		// instead — as slice 1 originally did — would have made `-n --force`
		// strip the effect while it was still plain "push" (not yet
		// forbidden-class), leaving nothing behind for the later --force to
		// retarget, silently approving the forbidden dry run in exactly the
		// flag order the brief calls out.
		out := make([]Effect, 0, len(effects))
		for _, e := range effects {
			if e.Kind == EffectPath && e.Access.IsWrite() {
				continue
			}
			if e.Kind == EffectRemote && remoteMutationOps[e.Operation] {
				e.DryRun = true
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
