package session

import "strings"

// Status-line rate_limits sibling suffixes (ADR 0021 §1/§2). The wrapper writes a
// <id>.status.jsonl record file (and MAY keep a <id>.status.last hash sidecar) next
// to the transcript; neither is a transcript and neither is a session record.
//
// sessionModeSuffix (bead pg2-gzrn2) is a THIRD sibling: the session-mode CLI's
// <id>.session-mode.json per-session mode record, written next to the same
// transcript. Its extension is .json (not .jsonl), so without this exclusion
// listSessionFiles would strip only the .json suffix and derive the phantom
// session ID "<id>.session-mode" — the same bug class ADR 0021 §2 already fixed
// for the two suffixes above.
//
// compactionSuffix (bead pg2-thkh0.5, ADR 0096) is a FOURTH sibling:
// `session-mode hook post-compact` writes <id>.compaction.json (ts, trigger) next
// to the same transcript. It is .json, not .jsonl, so no pa-monitor enumerator can
// select it today; the exclusion is a defensive consistency guard mirroring
// sessionModeSuffix, so a future scanner of the projects directory cannot derive
// the phantom session ID "<id>.compaction".
const (
	statusRecordSuffix  = ".status.jsonl"
	statusSidecarSuffix = ".status.last"
	sessionModeSuffix   = ".session-mode.json"
	compactionSuffix    = ".compaction.json"
)

// IsTranscriptFile reports whether name is a Claude Code transcript file, as
// opposed to a status-line rate_limits sibling file (ADR 0021 §2). It is the
// single shared predicate applied everywhere pa-monitor enumerates transcript
// .jsonl files (ResolveTranscript here and the sibling-file LimitsSource reader),
// so a <id>.status.jsonl is NEVER selected as a transcript.
//
// A transcript ends in .jsonl but NOT .status.jsonl. The .status.last hash sidecar
// is likewise excluded — it is never a .jsonl, but rejecting it here documents the
// full exclusion set.
func IsTranscriptFile(name string) bool {
	if !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	return !IsStatusSiblingFile(name)
}

// IsStatusSiblingFile reports whether name is a session-adjacent sibling file
// that is NOT itself a transcript or a session record: the <id>.status.jsonl
// rate_limits record, the <id>.status.last hash sidecar, or the
// <id>.session-mode.json mode record (bead pg2-gzrn2), or the
// <id>.compaction.json compaction marker (bead pg2-thkh0.5). Used by
// gc.listSessionFiles to skip these so they never derive a phantom session ID
// (ADR 0021 §2), while genuine <id>.json session records still pass.
func IsStatusSiblingFile(name string) bool {
	return strings.HasSuffix(name, statusRecordSuffix) ||
		strings.HasSuffix(name, statusSidecarSuffix) ||
		strings.HasSuffix(name, sessionModeSuffix) ||
		strings.HasSuffix(name, compactionSuffix)
}
