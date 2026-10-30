package store

// Record types written to observations.journal and applied to index.db. Both
// the live path and replay go through the same idempotent apply function.
const (
	TEpoch     = "epoch"     // a state epoch began (initial, or a rebuild)
	TFile      = "file"      // a file id was assigned
	TIdent     = "ident"     // a snapshot file's identity changed (rewritten inode)
	TPath      = "path"      // a path now names the file
	TPathClose = "pathclose" // a path stopped naming the file
	TGen       = "gen"       // a generation of the copy started (Reset marks a reset entry)
	TCommit    = "commit"    // bytes [From,To) were copied and the offset committed
	TSnap      = "snap"      // a snapshot version was stored
	TTomb      = "tomb"      // a deletion record (Tombstone)
	TState     = "state"     // the file's state changed (ok, copy_mismatch)
	TRelease   = "release"   // pinned reset entries up to Version were released
)

// Rec is one journal record. Seq is assigned by the store when it is written.
type Rec struct {
	Seq  int64  `json:"seq"`
	T    string `json:"t"`
	At   int64  `json:"at"` // unix nanoseconds the recorder saw it
	File int64  `json:"file,omitempty"`
	Gen  int    `json:"gen,omitempty"`

	Source string `json:"source,omitempty"`
	Mode   string `json:"mode,omitempty"` // append, snapshot or link
	Path   string `json:"path,omitempty"`
	Link   string `json:"link,omitempty"`
	Method string `json:"method,omitempty"` // clone or bytes
	Reason string `json:"reason,omitempty"`
	State  string `json:"state,omitempty"`
	Reset  bool   `json:"reset,omitempty"`

	Dev       uint64 `json:"dev,omitempty"`
	Ino       uint64 `json:"ino,omitempty"`
	Birth     int64  `json:"birth,omitempty"`
	FirstHash uint64 `json:"firstHash,omitempty"`

	From    int64  `json:"from,omitempty"`
	To      int64  `json:"to,omitempty"`
	Size    int64  `json:"size,omitempty"` // size of the original when observed
	Tail    uint64 `json:"tail,omitempty"` // xxhash64 of the last TailLen bytes of the copy
	TailLen int    `json:"tailLen,omitempty"`
	Hash    uint64 `json:"hash,omitempty"`
	Version int    `json:"version,omitempty"`
	Epoch   int    `json:"epoch,omitempty"`
}
