package view

import "encoding/json"

// ThreadChangeVersion is the only thread change record version this module
// understands. A later version is additive and versioned: a record of another
// version still decodes (so it can be reported) but is not read by any rule.
const ThreadChangeVersion = 1

// ThreadParent is the entity a thread belongs to: the parent reference every
// record carries from the start.
type ThreadParent struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ThreadChange is one source-agnostic, versioned thread change record: a new
// message on a thread whose parent link names an entity. The view's
// `thread_changes` member lists the records the view shows. Nothing here names
// a chat, tracker or forge; Source is an opaque label.
type ThreadChange struct {
	Version   int          `json:"version"`
	ThreadID  string       `json:"thread_id"`
	Parent    ThreadParent `json:"parent"`
	MessageID string       `json:"message_id"`
	Author    string       `json:"author"`
	Timestamp string       `json:"timestamp"`
	Excerpt   string       `json:"excerpt"`
	Source    string       `json:"source"`
}

// KnownVersion reports whether the record's version is one this module reads.
func (c ThreadChange) KnownVersion() bool { return c.Version == ThreadChangeVersion }

// UnmarshalJSON decodes a record without failing the whole view: a record
// whose shape does not match this version (an unknown future version) keeps
// whatever version it printed, and the rest is left zero.
func (c *ThreadChange) UnmarshalJSON(data []byte) error {
	var peek struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(data, &peek)
	type plain ThreadChange
	var p plain
	if peek.Version == ThreadChangeVersion {
		if err := json.Unmarshal(data, &p); err != nil {
			return err
		}
		*c = ThreadChange(p)
		return nil
	}
	_ = json.Unmarshal(data, &p) // best effort: only used to report the record
	*c = ThreadChange(p)
	c.Version = peek.Version
	return nil
}
