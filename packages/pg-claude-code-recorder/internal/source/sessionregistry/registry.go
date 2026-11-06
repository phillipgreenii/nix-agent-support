// Package sessionregistry is the session-registry source: sessions/{pid}.json.
// One record per version (a version is keyed by pid, procStart and the content
// hash, because pids are reused) plus a deletion record when the file vanishes.
package sessionregistry

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// Name is the source name.
const Name = "session-registry"

// ParserVersion is stamped on every record.
const ParserVersion = 3

// Entry is one registry file.
type Entry struct {
	PID             *int64          `json:"pid"             ingest:"int"`
	SessionID       string          `json:"sessionId"       ingest:"session"`
	Cwd             string          `json:"cwd"             ingest:"path" store:"record:cwd_id"`
	StartedAt       *float64        `json:"startedAt"       ingest:"time"`
	ProcStart       string          `json:"procStart"       ingest:"keyword"`
	Version         string          `json:"version"         ingest:"keyword" store:"record:version_id"`
	Kind            string          `json:"kind"            ingest:"keyword"`
	Entrypoint      string          `json:"entrypoint"      ingest:"keyword" store:"record:entrypoint_id"`
	PidDomain       string          `json:"pidDomain"       ingest:"keyword"`
	Name            string          `json:"name"            ingest:"keyword"`
	JobID           string          `json:"jobId"           ingest:"keyword"`
	NameSource      string          `json:"nameSource"      ingest:"keyword"`
	NameSince       *float64        `json:"nameSince"       ingest:"time"`
	Status          string          `json:"status"          ingest:"keyword"`
	WaitingFor      string          `json:"waitingFor"      ingest:"keyword"`
	UpdatedAt       *float64        `json:"updatedAt"       ingest:"time"`
	StatusUpdatedAt *float64        `json:"statusUpdatedAt" ingest:"time"`
	SocketPath      string          `json:"messagingSocketPath" ingest:"path"`
	PeerProtocol    json.RawMessage `json:"peerProtocol"`
	PeerFeatures    json.RawMessage `json:"peerFeatures"`
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
	if len(parts) != 2 || parts[0] != "sessions" || !strings.HasSuffix(parts[1], ".json") {
		return nil, false
	}
	return source.Captures{"pid": strings.TrimSuffix(parts[1], ".json")}, true
}

// Catalog lists the fields the source can produce.
func (*Source) Catalog() map[string]source.Kind {
	c := source.Catalog(Entry{})
	c["file_pid"] = source.KindInt
	c["content_hash"] = source.KindKeyword
	c["registry_key"] = source.KindID
	return c
}

func hashOf(raw []byte) string { return fmt.Sprintf("%016x", xxhash.Sum64(raw)) }

func key(pid, procStart, hash string) string { return pid + ":" + procStart + ":" + hash }

// Parse parses one stored version.
func (*Source) Parse(c source.Captures, raw []byte) ([]source.Record, error) {
	var e Entry
	rec, err := source.JSONRecord("registry", "", raw, &e)
	if err != nil {
		return nil, err
	}
	rec.Session = e.SessionID
	h := hashOf(raw)
	pid := c["pid"]
	if e.PID != nil {
		pid = fmt.Sprint(*e.PID)
	}
	rec.Fields = append(rec.Fields, source.Str("content_hash", source.KindKeyword, h)...)
	rec.Fields = append(rec.Fields, source.Str("registry_key", source.KindID, key(pid, e.ProcStart, h))...)
	var n int64
	if _, err := fmt.Sscan(c["pid"], &n); err == nil {
		rec.Fields = append(rec.Fields, source.Field{Name: "file_pid", Kind: source.KindInt, N: n})
	}
	switch {
	case e.UpdatedAt != nil:
		rec.Time = source.EpochTime(*e.UpdatedAt)
	case e.StartedAt != nil:
		rec.Time = source.EpochTime(*e.StartedAt)
	}
	return []source.Record{rec}, nil
}

// Tombstone is the deletion record: it names the registry key of the last
// stored version and the deletion time.
func (s *Source) Tombstone(c source.Captures, last []byte, at time.Time) (source.Record, bool) {
	rec := source.Record{Type: "registry_deleted", Item: source.ItemTombstone, Time: at}
	var n int64
	if _, err := fmt.Sscan(c["pid"], &n); err == nil {
		rec.Fields = append(rec.Fields, source.Field{Name: "file_pid", Kind: source.KindInt, N: n})
	}
	if len(last) > 0 {
		var e Entry
		if json.Unmarshal(last, &e) == nil {
			rec.Session = e.SessionID
			if e.PID != nil {
				rec.Fields = append(rec.Fields, source.Field{Name: "pid", Kind: source.KindInt, N: *e.PID})
			}
			rec.Fields = append(rec.Fields, source.Str("proc_start", source.KindKeyword, e.ProcStart)...)
			rec.Fields = append(rec.Fields, source.Str("registry_key", source.KindID, key(c["pid"], e.ProcStart, hashOf(last)))...)
		}
	}
	return rec, true
}

// Storage lists where each field is stored (implements source.Stored).
func (*Source) Storage() map[string]source.Storage { return source.MustCatalogStorage(Entry{}) }
