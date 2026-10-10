package focus

// The two bead metadata keys whose presence excludes an issue from the
// candidate set. Reading a bead's metadata to EXCLUDE it is not decision
// logic: the keys are written by other components, and this package only
// recognises them. They are declared together here, and the G5 guard permits
// the literal of MetaDedupKey only as this constant's value.
const (
	// MetaSourceID marks a minted focus bead: it names the entity the bead
	// was minted for. Such a bead is never a candidate, or a labelled or
	// linked one would take a second slot for the same work.
	MetaSourceID = "source_id"
	// MetaDedupKey marks a work-item bead some decider minted for a PR (a
	// review or CI-fix bead). The PR itself is the candidate, not the bead.
	MetaDedupKey = "dedup_key"
)
