// Package scope decides what the recorder ingests: the allow roots (projects/,
// sessions/, history.jsonl and the control mod's ends/ root), the first-version
// block list, and the source and mode of each ingested file.
package scope

import (
	"path/filepath"
	"strings"
)

// Mode is how a source's files are copied.
type Mode string

// Copy modes.
const (
	Append   Mode = "append"
	Snapshot Mode = "snapshot"
)

// Source names.
const (
	SrcTranscript  = "claude-transcript"
	SrcSubagentMD  = "claude-subagent-meta"
	SrcToolResult  = "claude-tool-result-body"
	SrcHistory     = "claude-history"
	SrcRegistry    = "session-registry"
	SrcStatus      = "claude-status"
	SrcSessionMode = "session-mode"
	SrcCompaction  = "session-compaction"
	SrcControlEnd  = "claude-control-end"
	SrcUnparsed    = "unparsed"
)

// Decision is the matcher's answer for one path.
type Decision struct {
	Ingest bool
	Reason string // why not, when Ingest is false
}

// Scope is the ingest-scope matcher the watcher's injected filter uses.
type Scope struct {
	claudeDir string
	endsDir   string // <controlStateDir>/ends, may lie outside claudeDir; "" when unconfigured
}

// New builds a Scope for the Claude directory and the control ends root.
func New(claudeDir, endsDir string) *Scope {
	s := &Scope{claudeDir: filepath.Clean(claudeDir)}
	if endsDir != "" {
		s.endsDir = filepath.Clean(endsDir)
	}
	return s
}

// ClaudeDir returns the Claude directory.
func (s *Scope) ClaudeDir() string { return s.claudeDir }

// EndsDir returns the control ends root ("" when unconfigured).
func (s *Scope) EndsDir() string { return s.endsDir }

func rel(root, path string) (string, bool) {
	r, err := filepath.Rel(root, path)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(r), true
}

// Blocked reports whether the base name or any component of path is on the
// block list: sessions/*.key, atomic-write temporary files (*.tmp.*), .DS_Store
// and anything under node_modules.
func Blocked(relPath string) (bool, string) {
	parts := strings.Split(relPath, "/")
	base := parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		if p == "node_modules" {
			return true, "node_modules"
		}
	}
	if base == "node_modules" {
		return true, "node_modules"
	}
	if base == ".DS_Store" {
		return true, "ds_store"
	}
	if strings.Contains(base, ".tmp.") {
		return true, "atomic_tmp"
	}
	if len(parts) == 2 && parts[0] == "sessions" && strings.HasSuffix(base, ".key") {
		return true, "session_key"
	}
	return false, ""
}

// Match decides whether path (absolute) is ingested.
func (s *Scope) Match(path string) Decision {
	path = filepath.Clean(path)
	if s.endsDir != "" {
		if r, ok := rel(s.endsDir, path); ok {
			if b, why := Blocked(r); b {
				return Decision{Reason: why}
			}
			return Decision{Ingest: true}
		}
	}
	r, ok := rel(s.claudeDir, path)
	if !ok {
		return Decision{Reason: "outside_roots"}
	}
	if b, why := Blocked(r); b {
		return Decision{Reason: why}
	}
	switch {
	case r == "history.jsonl":
		return Decision{Ingest: true}
	case strings.HasPrefix(r, "projects/"), strings.HasPrefix(r, "sessions/"):
		return Decision{Ingest: true}
	}
	return Decision{Reason: "outside_roots"}
}

// Classify returns the source and copy mode of an in-scope path.
func (s *Scope) Classify(path string) (string, Mode) {
	path = filepath.Clean(path)
	if s.endsDir != "" {
		if r, ok := rel(s.endsDir, path); ok && strings.HasSuffix(r, ".json") {
			return SrcControlEnd, Snapshot
		}
	}
	r, ok := rel(s.claudeDir, path)
	if !ok {
		return SrcUnparsed, Snapshot
	}
	base := r[strings.LastIndex(r, "/")+1:]
	parts := strings.Split(r, "/")
	switch {
	case r == "history.jsonl":
		return SrcHistory, Append
	case len(parts) == 2 && parts[0] == "sessions" && strings.HasSuffix(base, ".json"):
		return SrcRegistry, Snapshot
	case parts[0] != "projects":
		return SrcUnparsed, Snapshot
	case containsDir(parts, "workflows"), containsDir(parts, "scratchpad"), containsDir(parts, "memory"):
		return SrcUnparsed, Snapshot
	case strings.HasPrefix(base, ".orphaned-"), strings.HasPrefix(base, ".superseded-"), base == "sessions-index.json":
		return SrcUnparsed, Snapshot
	case containsDir(parts, "tool-results"):
		return SrcToolResult, Snapshot
	case containsDir(parts, "subagents") && strings.HasPrefix(base, "agent-") && strings.HasSuffix(base, ".meta.json"):
		return SrcSubagentMD, Snapshot
	case containsDir(parts, "subagents") && strings.HasPrefix(base, "agent-") && strings.HasSuffix(base, ".jsonl"):
		return SrcTranscript, Append
	case strings.HasSuffix(base, ".status.jsonl"):
		return SrcStatus, Append
	case strings.HasSuffix(base, ".session-mode.json"):
		return SrcSessionMode, Snapshot
	case strings.HasSuffix(base, ".compaction.json"):
		return SrcCompaction, Snapshot
	case len(parts) == 3 && strings.HasSuffix(base, ".jsonl"):
		return SrcTranscript, Append
	}
	return SrcUnparsed, Snapshot
}

func containsDir(parts []string, name string) bool {
	for _, p := range parts[:len(parts)-1] {
		if p == name {
			return true
		}
	}
	return false
}
