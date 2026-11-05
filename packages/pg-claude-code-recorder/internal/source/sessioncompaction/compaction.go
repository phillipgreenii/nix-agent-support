// Package sessioncompaction is the session-compaction source:
// projects/{project}/{session}.compaction.json, one record per version.
package sessioncompaction

import (
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// Name is the source name.
const Name = "session-compaction"

// ParserVersion is stamped on every record.
const ParserVersion = 2

// Marker is one compaction file.
type Marker struct {
	TS      *float64 `json:"ts"      ingest:"time" field:"compaction_ts"`
	Trigger string   `json:"trigger" ingest:"keyword" field:"compaction_trigger"`
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
	if len(parts) != 3 || parts[0] != "projects" || !strings.HasSuffix(parts[2], ".compaction.json") {
		return nil, false
	}
	return source.Captures{"project": parts[1], "session": strings.TrimSuffix(parts[2], ".compaction.json")}, true
}

// Catalog lists the fields the source can produce.
func (*Source) Catalog() map[string]source.Kind {
	c := source.Catalog(Marker{})
	c["project"] = source.KindKeyword
	c["file_session"] = source.KindID
	return c
}

// Parse parses one stored version.
func (*Source) Parse(c source.Captures, raw []byte) ([]source.Record, error) {
	var m Marker
	rec, err := source.JSONRecord("compaction", c["session"], raw, &m)
	if err != nil {
		return nil, err
	}
	if m.TS != nil {
		rec.Time = source.EpochTime(*m.TS)
	}
	rec.Fields = append(rec.Fields, source.Str("project", source.KindKeyword, c["project"])...)
	rec.Fields = append(rec.Fields, source.Str("file_session", source.KindID, c["session"])...)
	return []source.Record{rec}, nil
}
