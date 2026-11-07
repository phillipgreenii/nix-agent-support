package pool

import "time"

// SchemaVersion is the lease record format version.
const SchemaVersion = 1

// Record is the stored description of one slot and its current lease. It is
// never the source of liveness: that is the slot's exclusive flock (see
// Store.Inspect). The lease key is OwnerSessionID; BeadHint is an affinity
// hint only and is never used to look a lease up.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	Slot          string `json:"slot"`
	State         State  `json:"state"`

	LeaseID        string    `json:"lease_id,omitempty"`
	OwnerSessionID string    `json:"owner_session_id,omitempty"`
	OwnerPID       int       `json:"owner_pid,omitempty"`
	OwnerStart     time.Time `json:"owner_start,omitzero"`
	BeadHint       string    `json:"bead_hint,omitempty"`
	Repo           string    `json:"repo,omitempty"`
	Commit         string    `json:"commit,omitempty"`
	Branch         string    `json:"branch,omitempty"`
	Acquired       time.Time `json:"acquired,omitzero"`
	Heartbeat      time.Time `json:"heartbeat,omitzero"`
	Expiry         time.Time `json:"expiry,omitzero"`

	// LastBeadHint survives release so the pool can prefer the slot last used
	// for a work item. It is a preference only.
	LastBeadHint string `json:"last_bead_hint,omitempty"`
	// Reason records why a slot was quarantined or retired.
	Reason string `json:"reason,omitempty"`
}

// clearLease drops every lease field, keeping the slot identity and the
// affinity memory.
func (r *Record) clearLease() {
	r.LeaseID, r.OwnerSessionID, r.OwnerPID = "", "", 0
	r.OwnerStart = time.Time{}
	r.BeadHint, r.Repo, r.Commit, r.Branch = "", "", "", ""
	r.Acquired, r.Heartbeat, r.Expiry = time.Time{}, time.Time{}, time.Time{}
}
