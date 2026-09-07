package cmddesc

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
