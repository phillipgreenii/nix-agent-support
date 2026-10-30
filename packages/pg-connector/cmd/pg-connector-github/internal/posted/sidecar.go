package posted

import "errors"

// StateVersion is the version of the on-disk sidecar shape.
const StateVersion = 1

// ErrCorrupt is wrapped by the sidecar store's Load when a record exists but
// cannot be decoded. The record is left untouched: the operator deletes it to
// allow a repost, and treating it as empty would silently re-post dismissed
// comments.
var ErrCorrupt = errors.New("posted: sidecar is corrupt")

// LastAppend records the most recent append to a PR's pending review.
type LastAppend struct {
	At    string `json:"at"`
	Added int    `json:"added"`
	Head  string `json:"head"`
}

// State is the per-PR sidecar: every fingerprint confirmed posted, the heads
// whose body section was written, and the last append. It is plain JSON so the
// operator can delete it to allow a repost.
type State struct {
	Version      int         `json:"version"`
	Fingerprints []string    `json:"fingerprints"`
	BodyHeads    []string    `json:"body_heads"`
	LastAppend   *LastAppend `json:"last_append,omitempty"`
}

// Has reports whether fp is recorded as confirmed posted.
func (s State) Has(fp string) bool {
	for _, f := range s.Fingerprints {
		if f == fp {
			return true
		}
	}
	return false
}

// AddFingerprints records fingerprints, skipping ones already present.
func (s *State) AddFingerprints(fps ...string) {
	for _, fp := range fps {
		if !s.Has(fp) {
			s.Fingerprints = append(s.Fingerprints, fp)
		}
	}
}

// AddBodyHead records that the body section for head was written.
func (s *State) AddBodyHead(head string) {
	for _, h := range s.BodyHeads {
		if h == head {
			return
		}
	}
	s.BodyHeads = append(s.BodyHeads, head)
}
