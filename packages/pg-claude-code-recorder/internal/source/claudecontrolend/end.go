// Package claudecontrolend is the claude-control-end source: the end files the
// control mod writes on session.end, <controlStateDir>/ends/{session}.{ms}.json,
// one snapshot record per file carrying the session id, the end reason and the
// time. The schema is claude-marketplace/pg-claude-control/schema/
// session-end.schema.json; the mod owns the format and this source reads it.
// The reason is detail only: it cannot turn a crashed Conversation into
// anything else.
package claudecontrolend

import (
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// Name is the source name.
const Name = "claude-control-end"

// ParserVersion is stamped on every record.
const ParserVersion = 1

// RelPrefix is the path prefix of an end file as the parser sees it: the ends
// root lies outside the Claude directory, so the parser names its files
// "ends/<name>".
const RelPrefix = "ends/"

// Reasons is the reason enum of the schema.
var Reasons = []string{"prompt_input_exit", "clear", "other", "resume", "logout"}

// End is one end file.
type End struct {
	SessionID string `json:"sessionId" ingest:"session"`
	Reason    string `json:"reason"    ingest:"keyword"`
	TimeMs    *int64 `json:"timeMs"    ingest:"time"`
}

// Valid reports whether e satisfies the schema's required keys and reason enum.
func (e End) Valid() bool {
	if e.SessionID == "" || e.TimeMs == nil || *e.TimeMs < 0 {
		return false
	}
	for _, r := range Reasons {
		if r == e.Reason {
			return true
		}
	}
	return false
}

// Source implements source.Source.
type Source struct{}

// New returns the source.
func New() *Source { return &Source{} }

// Name implements source.Source.
func (*Source) Name() string { return Name }

// Version implements source.Source.
func (*Source) Version() int { return ParserVersion }

// Mode implements source.Source.
func (*Source) Mode() source.Mode { return source.Snapshot }

// Match takes "ends/<session>.<ms>.json".
func (*Source) Match(path string) (source.Captures, bool) {
	rest, ok := strings.CutPrefix(path, RelPrefix)
	if !ok || strings.Contains(rest, "/") || !strings.HasSuffix(rest, ".json") {
		return nil, false
	}
	return source.Captures{"file": strings.TrimSuffix(rest, ".json")}, true
}

// Catalog lists the fields the source can produce.
func (*Source) Catalog() map[string]source.Kind { return source.Catalog(End{}) }

// Parse parses one end file.
func (*Source) Parse(_ source.Captures, raw []byte) ([]source.Record, error) {
	var e End
	rec, err := source.JSONRecord("control_end", "", raw, &e)
	if err != nil {
		return nil, err
	}
	rec.Session = e.SessionID
	if e.TimeMs != nil {
		rec.Time = source.EpochTime(float64(*e.TimeMs))
	}
	return []source.Record{rec}, nil
}
