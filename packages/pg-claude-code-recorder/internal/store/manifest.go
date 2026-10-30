package store

// Manifest lists every file of a settled Conversation. It is stored in the
// settled table, in the Conversation's archive directory (manifest.json) and
// inside every archive segment.
type Manifest struct {
	Session   string         `json:"session"`
	SettledAt int64          `json:"settled_at_ns"`
	Method    string         `json:"method"` // clone or bytes
	SessionID []string       `json:"session_ids"`
	Files     []ManifestFile `json:"files"`
}

// ManifestFile is one file of the Conversation.
type ManifestFile struct {
	// Path is the original path relative to the Claude directory.
	Path string `json:"path"`
	File int64  `json:"file"`
	Mode string `json:"mode"`
	// Gen is the current generation (the latest version of a snapshot file) and
	// Size the committed copy offset (the latest version's size).
	Gen         int           `json:"gen"`
	Size        int64         `json:"size"`
	Hash        string        `json:"hash"` // xxhash64 of the current generation's bytes, hex
	Generations []ManifestGen `json:"generations"`
	Parsed      bool          `json:"parsed"`
	ParsedBy    string        `json:"parsed_by,omitempty"`
	SessionIDs  []string      `json:"session_ids,omitempty"`
}

// ManifestGen is one generation (or snapshot version) of a file.
type ManifestGen struct {
	Gen  int   `json:"gen"`
	Size int64 `json:"size"`
}
