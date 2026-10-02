// fingerprint.go: the pg_router_escalation_fingerprint values this probe
// computes for each finding kind — exact-match only, no fuzzy matching at
// probe time (that judgment stays with the triager) [design: "Bead
// identity and schema", "Fingerprint metadata key" paragraph]:
//   - needs_input findings: needs-input:<ccpool session external_id>
//     [design: same paragraph, second bullet].
//   - Pool scoping (bead pg2-bkzrc): a session in a NAMED role pool is
//     fingerprinted needs-input:<pool label>:<external_id>, because
//     external ids are only unique within one pool. The ambient pool keeps
//     the legacy needs-input:<external_id> form so beads filed before the
//     probe scanned every pool still deduplicate.
//   - Never-prompted sessions: never-prompted:<pool label>:<external_id>.
//   - Zombie-count drift: zombie-count:<severity band> [design: same
//     paragraph, third bullet].
package main

// needsInputFingerprint renders "needs-input:<external_id>" [design:
// "Fingerprint metadata key" paragraph, second bullet], with the pool
// scope inserted for a named pool (see the package comment above).
func needsInputFingerprint(scope, externalID string) string {
	if scope == "" {
		return "needs-input:" + externalID
	}
	return "needs-input:" + scope + ":" + externalID
}

// neverPromptedFingerprint renders "never-prompted:<pool label>:<external_id>".
func neverPromptedFingerprint(poolLabel, externalID string) string {
	return "never-prompted:" + poolLabel + ":" + externalID
}

// zombieDriftFingerprint renders "zombie-count:<severity band>" [design:
// same paragraph, third bullet]. Because the band is baked directly into
// the fingerprint, a move to a NEW band is inherently a fresh fingerprint
// (dedup.go's matchExisting will not find an existing bead for it, so
// decideAction creates a new one) — this is how this probe realizes "skip
// unless the value moved into a new severity band" [Binding decisions:
// "'Nothing new' rule" bullet, zombie-count half] without needing a
// separate band-comparison step: staying IN the same band reuses the same
// fingerprint and is therefore naturally deduplicated.
func zombieDriftFingerprint(band severityZombieBand) string {
	return "zombie-count:" + string(band)
}
