// Package claudehistory is the claude-history source: history.jsonl, one
// record per entry.
package claudehistory

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// Name is the source name.
const Name = "claude-history"

// ParserVersion is stamped on every record.
const ParserVersion = 3

// Entry is one history.jsonl line.
type Entry struct {
	Display        string          `json:"display"   ingest:"text"`
	Timestamp      *float64        `json:"timestamp" ingest:"time"`
	Project        string          `json:"project"   ingest:"path"`
	SessionID      string          `json:"sessionId" ingest:"session"`
	PastedContents json.RawMessage `json:"pastedContents" ingest:"opaque"`
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
	if path == "history.jsonl" {
		return source.Captures{}, true
	}
	return nil, false
}

// Catalog lists the fields the source can produce.
func (*Source) Catalog() map[string]source.Kind {
	c := source.Catalog(Entry{})
	c["slash_command"] = source.KindKeyword
	return c
}

// SlashCommand returns "exit" or "clear" when display is that typed command
// (alone or followed by arguments), else "". It is a direct read of the typed
// text, not an interpretation of it.
func SlashCommand(display string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(display), " ")
	switch first {
	case "/exit":
		return "exit"
	case "/clear":
		return "clear"
	}
	return ""
}

// Parse parses one history line.
func (*Source) Parse(_ source.Captures, raw []byte) ([]source.Record, error) {
	raw = bytes.TrimSpace(raw)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	rec := source.Record{Type: "history", Item: source.ItemLine, Session: e.SessionID}
	rec.Fields = source.Extract(&e, "")
	rec.Fields = append(rec.Fields, source.Str("slash_command", source.KindKeyword, SlashCommand(e.Display))...)
	if e.Timestamp != nil {
		rec.Time = source.EpochTime(*e.Timestamp)
	}
	rec.Unknown = source.UnknownKeysValid(raw, reflect.TypeOf(Entry{}), "")
	return []source.Record{rec}, nil
}
