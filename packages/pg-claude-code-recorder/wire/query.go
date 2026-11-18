package wire

import "encoding/json"

// The query, search and files.inventory shapes.

// Filter is the JSON filter: a leaf {"field": F, <operator>: V} or a
// combination (all, any, not).
type Filter struct {
	Field    string          `json:"field,omitempty"`
	Eq       json.RawMessage `json:"eq,omitempty"`
	In       []any           `json:"in,omitempty"`
	Range    *Range          `json:"range,omitempty"`
	Under    string          `json:"under,omitempty"`
	Exists   *bool           `json:"exists,omitempty"`
	Contains string          `json:"contains,omitempty"`
	Match    string          `json:"match,omitempty"`
	All      []Filter        `json:"all,omitempty"`
	Any      []Filter        `json:"any,omitempty"`
	Not      *Filter         `json:"not,omitempty"`
}

// Range bounds a value: From inclusive and To exclusive (Gte, Gt, Lte, Lt are
// the explicit forms).
type Range struct {
	From any `json:"from,omitempty"`
	To   any `json:"to,omitempty"`
	Gte  any `json:"gte,omitempty"`
	Gt   any `json:"gt,omitempty"`
	Lte  any `json:"lte,omitempty"`
	Lt   any `json:"lt,omitempty"`
}

// Agg is one aggregate.
type Agg struct {
	Fn       string `json:"fn"`
	Field    string `json:"field,omitempty"`
	Distinct string `json:"distinct,omitempty"`
	As       string `json:"as,omitempty"`
}

// Order is one ordering term.
type Order struct {
	By  string `json:"by"`
	Dir string `json:"dir,omitempty"`
}

// QueryRequest is the query operation's body.
type QueryRequest struct {
	Op        string   `json:"op,omitempty"`
	From      string   `json:"from,omitempty"`
	Where     *Filter  `json:"where,omitempty"`
	Group     []string `json:"group,omitempty"`
	Aggregate []Agg    `json:"aggregate,omitempty"`
	Select    []string `json:"select,omitempty"`
	Raw       bool     `json:"raw,omitempty"`
	// Follow resolves a link per row: parent (parent_uuid to uuid), result (a
	// tool_use to its tool_result) or call (a tool_result to its tool_use).
	// A link is resolved within the same file first.
	Follow string  `json:"follow,omitempty"`
	Order  []Order `json:"order,omitempty"`
	Limit  int     `json:"limit,omitempty"`
	// OldGenerations includes records of superseded generations.
	OldGenerations bool `json:"old_generations,omitempty"`
}

// RecordRef names a record by its position.
type RecordRef struct {
	File   string `json:"file"`
	Gen    int    `json:"gen"`
	Offset int64  `json:"offset"`
	Item   int    `json:"item"`
}

// Link is a followed record with the same columns as the row.
type Link struct {
	Record RecordRef `json:"record"`
	Row    []any     `json:"row"`
}

// QueryAnswer is the query answer.
type QueryAnswer struct {
	AsOf     string      `json:"as_of"`
	Columns  []string    `json:"columns"`
	Rows     [][]any     `json:"rows"`
	More     bool        `json:"more"`
	Degraded bool        `json:"degraded,omitempty"`
	Records  []RecordRef `json:"records,omitempty"`
	Links    [][]Link    `json:"links,omitempty"`
	Raw      []string    `json:"raw,omitempty"`
}

// InventoryRequest is the files.inventory operation's body. It lists the files
// that have records with their path facts and the newest record time, from the
// per-file table the parser maintains (file_facts), never from records.
//
// Sources limits the files to those sources (empty: every source). Since and
// Sessions narrow the list: a file is kept when its newest record is at or after
// Since (RFC 3339) OR its file_session is one of Sessions; with neither set no
// narrowing applies.
type InventoryRequest struct {
	Sources  []string `json:"sources,omitempty"`
	Since    string   `json:"since,omitempty"`
	Sessions []string `json:"sessions,omitempty"`
	// After pages the answer: only files with an id greater than After (the
	// answer is in file id order; continue from the last file while More).
	After int64 `json:"after,omitempty"`
	Limit int   `json:"limit,omitempty"`
}

// InventoryFile is one file of the inventory.
type InventoryFile struct {
	File        int64  `json:"file"`
	Source      string `json:"source"`
	Project     string `json:"project,omitempty"`
	FileSession string `json:"file_session,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	// Last is the newest record time over every generation of the file (empty
	// when no record of it carries a time).
	Last string `json:"last,omitempty"`
}

// InventoryAnswer is the files.inventory answer. Degraded is set while the
// recorder is catching up or while file_facts is still being backfilled on a
// database that existed before it, when a file may be missing from Files.
type InventoryAnswer struct {
	AsOf     string          `json:"as_of"`
	Files    []InventoryFile `json:"files"`
	More     bool            `json:"more"`
	Degraded bool            `json:"degraded,omitempty"`
}

// SearchRequest is the search operation's body.
type SearchRequest struct {
	Op     string   `json:"op,omitempty"`
	Mode   string   `json:"mode,omitempty"` // word (default) or substring
	Q      string   `json:"q"`
	Where  *Filter  `json:"where,omitempty"`
	Fields []string `json:"fields,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	// OldGenerations includes records of superseded generations.
	OldGenerations bool `json:"old_generations,omitempty"`
}

// Hit is one search hit.
type Hit struct {
	Record    RecordRef `json:"record"`
	SessionID string    `json:"session_id"`
	Time      string    `json:"time"`
	Field     string    `json:"field"`
	Score     float64   `json:"score,omitempty"`
	MatchAt   *int      `json:"match_at,omitempty"`
	Snippet   string    `json:"snippet"`
}

// SearchAnswer is the search answer.
type SearchAnswer struct {
	AsOf     string `json:"as_of"`
	Hits     []Hit  `json:"hits"`
	More     bool   `json:"more"`
	Degraded bool   `json:"degraded,omitempty"`
	// TooShort is true when a substring query was under three characters: it is
	// refused, so no hits are returned.
	TooShort bool   `json:"too_short,omitempty"`
	Note     string `json:"note,omitempty"`
	// Partial is true when a substring scan stopped at its byte budget or at the
	// query deadline before it had read every candidate copy. The hits are
	// correct but may be incomplete: ScannedBytes says how much text was read and
	// ScannedTo is the time of the oldest record read (the scan runs newest
	// first), so a retry narrowed to that side of ScannedTo covers the rest.
	Partial      bool   `json:"partial,omitempty"`
	ScannedBytes int64  `json:"scanned_bytes,omitempty"`
	ScannedTo    string `json:"scanned_to,omitempty"`
}
