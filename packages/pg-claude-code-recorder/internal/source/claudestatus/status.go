// Package claudestatus is the claude-status source:
// projects/{project}/{session}.status.jsonl, one record per line (the
// pgd display mod's rate-limit captures).
package claudestatus

import (
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// Name is the source name.
const Name = "claude-status"

// ParserVersion is stamped on every record.
const ParserVersion = 2

// Line is one status line.
type Line struct {
	TS               *float64 `json:"ts"                  ingest:"time"`
	SessionID        string   `json:"session_id"          ingest:"session"`
	Hostname         string   `json:"hostname"            ingest:"keyword"`
	FiveHourPct      *float64 `json:"five_hour_pct"       ingest:"float"`
	FiveHourResetsAt *float64 `json:"five_hour_resets_at" ingest:"time"`
	SevenDayPct      *float64 `json:"seven_day_pct"       ingest:"float"`
	SevenDayResetsAt *float64 `json:"seven_day_resets_at" ingest:"time"`
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
func (*Source) Mode() source.Mode { return source.Append }

// Match takes the path relative to the Claude directory.
func (*Source) Match(path string) (source.Captures, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] != "projects" || !strings.HasSuffix(parts[2], ".status.jsonl") {
		return nil, false
	}
	return source.Captures{"project": parts[1], "session": strings.TrimSuffix(parts[2], ".status.jsonl")}, true
}

// Catalog lists the fields the source can produce.
func (*Source) Catalog() map[string]source.Kind {
	c := source.Catalog(Line{})
	c["project"] = source.KindKeyword
	c["file_session"] = source.KindID
	return c
}

// Parse parses one status line.
func (*Source) Parse(c source.Captures, raw []byte) ([]source.Record, error) {
	var l Line
	rec, err := source.JSONRecord("status", c["session"], raw, &l)
	if err != nil {
		return nil, err
	}
	if l.SessionID != "" {
		rec.Session = l.SessionID
	}
	if l.TS != nil {
		rec.Time = source.EpochTime(*l.TS)
	}
	rec.Fields = append(rec.Fields, source.Str("project", source.KindKeyword, c["project"])...)
	rec.Fields = append(rec.Fields, source.Str("file_session", source.KindID, c["session"])...)
	return []source.Record{rec}, nil
}
