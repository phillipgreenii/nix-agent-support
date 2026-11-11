package settle

// Pure parts of Wake's work-set composition (wake.go). Wake keeps the reads,
// the liveSeen update and the order of those steps; these functions only decide
// which sessions belong to the work set and which wake reasons apply.

// liveDiffSessions returns the sessions whose live flag changed: live now but
// not before, or live before but not now.
func liveDiffSessions(live, liveSeen map[string]bool) map[string]bool {
	out := map[string]bool{}
	for session := range live {
		if live[session] && !liveSeen[session] {
			out[session] = true
		}
	}
	for session := range liveSeen {
		if !live[session] {
			out[session] = true
		}
	}
	return out
}

// notLiveSessions returns the members of set that are not live. A live session
// is left to the live path of evaluate.
func notLiveSessions(set, live map[string]bool) map[string]bool {
	out := map[string]bool{}
	for session := range set {
		if !live[session] {
			out[session] = true
		}
	}
	return out
}

// unsettledSessions returns the cached sessions that have no settled row. A
// low-space flip makes these due, because the quiet-age gate is lifted.
func unsettledSessions[V any](cache map[string]V, settled map[string]bool) map[string]bool {
	out := map[string]bool{}
	for session := range cache {
		if !settled[session] {
			out[session] = true
		}
	}
	return out
}

// wakeReasons says which wake reasons apply to a pass: dirty (a dirty session
// or a live-set signal), due (a heap item came due) and poll (a live diff the
// signal did not announce, a low-space flip, or nothing else to explain it).
func wakeReasons(dirtyCount, dueCount int, sigLive, liveDiff, flip bool) map[string]bool {
	dirtyReason := dirtyCount > 0 || sigLive
	return map[string]bool{
		"dirty": dirtyReason,
		"due":   dueCount > 0,
		"poll":  (liveDiff && !sigLive) || flip || (!dirtyReason && dueCount == 0),
	}
}
