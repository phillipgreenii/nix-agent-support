package wire

// The feed, snapshot and raw shapes.

// FeedRequest is the feed operation's body.
type FeedRequest struct {
	Op      string   `json:"op,omitempty"`
	After   string   `json:"after,omitempty"`
	Where   *Filter  `json:"where,omitempty"`
	Project []string `json:"project,omitempty"`
	WaitMS  int      `json:"wait_ms,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

// Entry is one feed entry: a reset or a record.
type Entry struct {
	Seq    string         `json:"seq"`
	Kind   string         `json:"kind"` // reset or record
	Record *RecordRef     `json:"record,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
	// A reset entry names the file and generation whose records are replaced.
	File   string `json:"file,omitempty"`
	Gen    int    `json:"gen,omitempty"`
	Parser string `json:"parser,omitempty"`
}

// FeedAnswer is the feed answer.
type FeedAnswer struct {
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"next_cursor"`
	More       bool    `json:"more"`
	TimedOut   bool    `json:"timed_out"`
	Degraded   bool    `json:"degraded,omitempty"`
}

// SnapshotRequest is the snapshot operation's body. The first page is asked
// without At or Page; later pages repeat the answer's Cursor as At and send
// NextPage as Page.
type SnapshotRequest struct {
	Op      string   `json:"op,omitempty"`
	Where   *Filter  `json:"where,omitempty"`
	Project []string `json:"project,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	At      string   `json:"at,omitempty"`
	Page    string   `json:"page,omitempty"`
}

// SnapshotAnswer is the snapshot answer: the cursor to tail the feed from and a
// page of the records current at it.
type SnapshotAnswer struct {
	Cursor   string  `json:"cursor"`
	Entries  []Entry `json:"entries"`
	More     bool    `json:"more"`
	NextPage string  `json:"next_page,omitempty"`
	Degraded bool    `json:"degraded,omitempty"`
}

// RawRequest names a record, or a file range, whose bytes to read.
type RawRequest struct {
	Op     string     `json:"op,omitempty"`
	Record *RecordRef `json:"record,omitempty"`
	File   string     `json:"file,omitempty"`
	Gen    int        `json:"gen,omitempty"`
	Offset int64      `json:"offset,omitempty"`
	Length int        `json:"length,omitempty"`
}

// RawAnswer holds the bytes: Data is the text when they are valid UTF-8
// (Encoding "utf8"), else base64 (Encoding "base64").
type RawAnswer struct {
	File     string `json:"file"`
	Gen      int    `json:"gen"`
	Offset   int64  `json:"offset"`
	Length   int    `json:"length"`
	Encoding string `json:"encoding"`
	Data     string `json:"data"`
}
