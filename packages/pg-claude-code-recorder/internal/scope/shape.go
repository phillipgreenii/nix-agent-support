package scope

import "strings"

// This file is the one path-shape allowlist for the Claude directory. Match
// (the per-event and per-file filter) and DescendDir (the sweep walker's
// directory predicate) both answer from it, so they cannot disagree.
//
// Paths are relative to the Claude directory, slash separated, split into
// components. Accepted:
//
//	history.jsonl
//	sessions/**
//	projects/<project>/<name>      name ends in .jsonl (transcript, which
//	                               includes .status.jsonl), .session-mode.json
//	                               or .compaction.json; names Classify treats as
//	                               unparsed (dot-prefixed .orphaned-/.superseded-
//	                               copies) are not accepted
//	projects/<project>/<uuid>/subagents/**
//	projects/<project>/<uuid>/tool-results/**
//	projects/<project>/<uuid>/scratchpad/**
//	projects/<project>/<uuid>/workflows/**
//
// A session directory is recognized by its UUID shape, never by a list of names
// to skip, so spikes-*, memory, artifacts and any future directory under a
// project are not walked. scratchpad and workflows stay because removal and
// extract carry them with their Conversation (settle.SessionOf maps them to it).

// sessionSubdirs are the directories of a session directory that are walked.
var sessionSubdirs = map[string]bool{
	"subagents":    true,
	"tool-results": true,
	"scratchpad":   true,
	"workflows":    true,
}

// IsUUID reports whether s has the canonical 8-4-4-4-12 lower-case hex shape.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

// projectFileKind reports whether a file name directly under a project directory
// is a kind Classify consumes (not SrcUnparsed).
func projectFileKind(base string) bool {
	if strings.HasPrefix(base, ".orphaned-") || strings.HasPrefix(base, ".superseded-") {
		return false
	}
	return strings.HasSuffix(base, ".jsonl") ||
		strings.HasSuffix(base, ".session-mode.json") ||
		strings.HasSuffix(base, ".compaction.json")
}

// allowFile reports whether the file at parts (relative to the Claude
// directory) is in the allowlist.
func allowFile(parts []string) bool {
	switch parts[0] {
	case "history.jsonl":
		return len(parts) == 1
	case "sessions":
		return len(parts) > 1
	case "projects":
		switch {
		case len(parts) == 3:
			return projectFileKind(parts[2])
		case len(parts) > 4:
			return IsUUID(parts[2]) && sessionSubdirs[parts[3]]
		}
	}
	return false
}

// allowDir reports whether the walker may read the directory at parts. The
// Claude directory itself (no parts) is never read.
func allowDir(parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "sessions":
		return true
	case "projects":
		switch len(parts) {
		case 1, 2:
			return true
		case 3:
			return IsUUID(parts[2])
		default:
			return IsUUID(parts[2]) && sessionSubdirs[parts[3]]
		}
	}
	return false
}
