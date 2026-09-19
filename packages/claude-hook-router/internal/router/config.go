// Package router implements the claude-hook-router runtime's dispatch/merge
// loop: it reads router-config.json, reads the incoming Claude Code hook
// event JSON, selects and matcher-filters that event's delegate list, runs
// the sequential dispatch/merge loop against real delegate processes, and
// produces the single hookSpecificOutput JSON the caller (main) writes to
// stdout.
//
// See docs/adr/0071-claude-code-hook-router.md §2.3-§2.7 (nix-agent-support)
// for the full design this package implements against.
package router

import (
	"encoding/json"
	"fmt"
	"os"
)

// Delegate is one registration in router-config.json's per-event delegate
// list, exactly as packet A1's nix generator produces it. Matcher unmarshals
// a JSON null into the empty string, which MatchesEvent already treats as
// match-all — no separate null-handling is needed.
type Delegate struct {
	Name     string `json:"name"`
	Event    string `json:"event"`
	Matcher  string `json:"matcher"`
	Command  string `json:"command"`
	Contract string `json:"contract"`
	Priority int    `json:"priority"`
}

// Config is the full router-config.json shape: event name -> delegate list,
// in the order the generator wrote them (dispatch order is computed fresh
// from Priority/Name at dispatch time, not from this slice order).
type Config map[string][]Delegate

// LoadConfig reads and parses router-config.json from path. Any failure here
// is a total-failure case for the caller: per the design's fail-safe
// default, main must still emit {} rather than propagating the error as a
// hang or a denial.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read router config %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse router config %q: %w", path, err)
	}
	return cfg, nil
}

// EventMatchField returns the name of the incoming-hook-JSON field whose
// value a delegate's matcher for this event is evaluated against, per
// ADR 0071 §2.3: tool_name for the tool-invocation-shaped events,
// end_reason for SessionEnd, session_start_reason for SessionStart. The
// second return is false for an event this router does not know how to
// matcher-filter (still dispatched, just unfiltered).
func EventMatchField(event string) (string, bool) {
	switch event {
	case "PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest", "PermissionDenied":
		return "tool_name", true
	case "SessionEnd":
		return "end_reason", true
	case "SessionStart":
		return "session_start_reason", true
	default:
		return "", false
	}
}
