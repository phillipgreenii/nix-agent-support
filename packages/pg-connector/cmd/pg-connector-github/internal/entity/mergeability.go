package entity

// The three values GitHub reports for a PR's mergeable state.
const (
	MergeableUnknown     = "UNKNOWN"
	MergeableMergeable   = "MERGEABLE"
	MergeableConflicting = "CONFLICTING"
)

// Known reports whether v is a value GitHub has finished computing (anything
// but UNKNOWN or the empty string, so a future known value is also known).
func Known(v string) bool { return v != "" && v != MergeableUnknown }

// Carry applies the mergeability rule.
//
// GitHub recomputes mergeability lazily and normally passes through UNKNOWN, so
// MERGEABLE then UNKNOWN then CONFLICTING is a real change hidden behind two
// UNKNOWN transitions. prev is the last KNOWN value carried in the entity row
// ("" when none), observed is what the wire just returned, and headChanged and
// baseChanged say whether the head or base commit the observation describes
// differs from the one prev was learned for.
//
//   - A head or base change clears prev first: the old head's mergeability says
//     nothing about the new one, so it is neither returned nor compared with.
//   - An UNKNOWN (or empty) observation is never a change; carried is prev, or
//     UNKNOWN when no known value has been seen for the current head and base.
//   - A known observation is the new carried value; changed is true only when
//     prev is known and differs from it.
//
// carried is the value the daemon puts on the wire. The caller stores it in the
// entity row's last_known_mergeable only when Known(carried).
func Carry(prev, observed string, headChanged, baseChanged bool) (carried string, changed bool) {
	if headChanged || baseChanged {
		prev = ""
	}
	if !Known(observed) {
		if Known(prev) {
			return prev, false
		}
		return MergeableUnknown, false
	}
	return observed, Known(prev) && prev != observed
}
