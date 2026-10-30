// Package sessionmode is the session-mode source:
// projects/{project}/{session}.session-mode.json, one record per version.
package sessionmode

import (
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// Name is the source name.
const Name = "session-mode"

// ParserVersion is stamped on every record.
const ParserVersion = 1

// Mode is one session-mode file.
type Mode struct {
	Kind      string `json:"kind"       ingest:"keyword" field:"mode_kind"`
	Detail    string `json:"detail"     ingest:"text"    field:"mode_detail"`
	State     string `json:"state"      ingest:"keyword" field:"mode_state"`
	StartedAt string `json:"started_at" ingest:"time"    field:"mode_started_at"`
	UpdatedAt string `json:"updated_at" ingest:"time"    field:"mode_updated_at"`
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

// Match takes the path relative to the Claude directory.
func (*Source) Match(path string) (source.Captures, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] != "projects" || !strings.HasSuffix(parts[2], ".session-mode.json") {
		return nil, false
	}
	return source.Captures{"project": parts[1], "session": strings.TrimSuffix(parts[2], ".session-mode.json")}, true
}

// Catalog lists the fields the source can produce.
func (*Source) Catalog() map[string]source.Kind {
	c := source.Catalog(Mode{})
	c["project"] = source.KindKeyword
	c["file_session"] = source.KindID
	return c
}

// Parse parses one stored version.
func (*Source) Parse(c source.Captures, raw []byte) ([]source.Record, error) {
	var m Mode
	rec, err := source.JSONRecord("session_mode", c["session"], raw, &m)
	if err != nil {
		return nil, err
	}
	if t, ok := source.ParseTime(m.UpdatedAt); ok {
		rec.Time = t
	}
	rec.Fields = append(rec.Fields, source.Str("project", source.KindKeyword, c["project"])...)
	rec.Fields = append(rec.Fields, source.Str("file_session", source.KindID, c["session"])...)
	return []source.Record{rec}, nil
}
