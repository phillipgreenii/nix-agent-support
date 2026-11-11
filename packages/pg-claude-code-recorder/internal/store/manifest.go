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
	// Registry lists the registry file versions that name this session; their
	// bytes are archived with it (the registry file itself belongs to no single
	// Conversation and stays where it is).
	Registry []ManifestRegistry `json:"registry_versions,omitempty"`
}

// ManifestRegistry is one stored version of a registry file.
type ManifestRegistry struct {
	File    int64 `json:"file"`
	Version int   `json:"version"`
	Size    int64 `json:"size"`
}

// ManifestFile is one file of the Conversation.
type ManifestFile struct {
	// Path is the original path relative to the Claude directory.
	Path string `json:"path"`
	File int64  `json:"file"`
	Mode string `json:"mode"`
	// Gen is the current generation (the latest version of a snapshot file) and
	// Size the committed copy offset (the latest version's size).
	Gen  int   `json:"gen"`
	Size int64 `json:"size"`
	// LastSize is the size the file row last recorded for a snapshot file. It
	// can differ from the stored version's size (the row follows what the
	// source path showed, the version holds what was stored); settle compares
	// it, never the stored size, to detect a change. Manifests written before
	// this field existed carry that size in Size (nil here).
	LastSize    *int64        `json:"last_size,omitempty"`
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

// StoredSize is the number of bytes the archive holds for the file's current
// generation, the length Hash covers. A snapshot manifest written before
// LastSize existed carried the file row's last size in Size, which can exceed
// the stored version; its generation record always has the stored size.
func (mf ManifestFile) StoredSize() int64 {
	if mf.Mode == "snapshot" {
		for _, g := range mf.Generations {
			if g.Gen == mf.Gen {
				return g.Size
			}
		}
	}
	return mf.Size
}

// ChangeSize is the size settle compares with the file row's LastSize for a
// snapshot file.
func (mf ManifestFile) ChangeSize() int64 {
	if mf.LastSize != nil {
		return *mf.LastSize
	}
	return mf.Size
}
