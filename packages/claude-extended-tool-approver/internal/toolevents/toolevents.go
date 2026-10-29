// Package toolevents writes CETA's ceta.* events into the tool-event log that
// claude-tool-tracker reads. The contract between producers and the tracker is
// the documented line format (claude-tool-tracker README "Event format" and
// docs/event-kinds.md), so this package re-implements the small producer side
// instead of importing the tracker's Go code.
//
// Every write is best effort: a failure is returned to the caller, who MUST NOT
// let it change a verdict or block the call.
package toolevents

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"
)

const (
	// Source is CETA's source name (the middle part of the file name).
	Source = "ceta"
	// Schema is the format and version stamped on every line.
	Schema = "tool-events/1"
	// MaxLineBytes is the largest line (newline included) one write emits.
	MaxLineBytes = 65536

	KindVerdict      = "ceta.verdict"
	KindTrace        = "ceta.trace"
	KindRuleErrors   = "ceta.rule_errors"
	KindCommandClass = "ceta.command_class"
)

var sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Event is one ceta.* event for one tool call.
type Event struct {
	Kind      string
	SessionID string
	ToolUseID string
	AgentID   string
	Data      map[string]any
}

type envelope struct {
	Schema    string         `json:"schema"`
	EventID   string         `json:"event_id"`
	TS        int64          `json:"ts"`
	Source    string         `json:"source"`
	Kind      string         `json:"kind"`
	SessionID string         `json:"session_id"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	Data      map[string]any `json:"data"`
}

// Dir is $XDG_STATE_HOME/claude-tool-tracker/events (default ~/.local/state).
func Dir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "claude-tool-tracker", "events"), nil
}

// FileName is the CETA event file for a session.
func FileName(session string) string {
	return "tool-events." + session + "." + Source + ".jsonl"
}

// Append writes each event as one line, one append-mode write per line.
// It stops at the first error and returns it.
func Append(events ...Event) error {
	if len(events) == 0 {
		return nil
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := ensureDir(dir); err != nil {
		return err
	}
	for _, ev := range events {
		if err := appendOne(dir, ev); err != nil {
			return err
		}
	}
	return nil
}

func appendOne(dir string, ev Event) error {
	if !sessionRe.MatchString(ev.SessionID) {
		return fmt.Errorf("session_id %q is not usable in a file name", ev.SessionID)
	}
	if ev.ToolUseID == "" {
		return fmt.Errorf("tool_use_id is required for kind %q", ev.Kind)
	}
	now := time.Now()
	data := ev.Data
	if data == nil {
		data = map[string]any{}
	}
	env := envelope{
		Schema: Schema, EventID: newEventID(now), TS: now.UnixMilli(), Source: Source,
		Kind: ev.Kind, SessionID: ev.SessionID, ToolUseID: ev.ToolUseID, AgentID: ev.AgentID,
		Data: data,
	}
	line, err := marshal(&env, MaxLineBytes)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, FileName(ev.SessionID)), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if n, err := f.Write(line); err != nil {
		return err
	} else if n != len(line) {
		return fmt.Errorf("short write: %d of %d bytes", n, len(line))
	}
	return nil
}

func newEventID(now time.Time) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%012x%016x", now.UnixMilli(), now.UnixNano())
	}
	return fmt.Sprintf("%012x%s", now.UnixMilli(), hex.EncodeToString(b[:]))
}

func encode(e *envelope) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// marshal renders the line, shortening data per the README's "Maximum line
// size and shortening" when the line would exceed max.
func marshal(e *envelope, max int) ([]byte, error) {
	line, err := encode(e)
	if err != nil {
		return nil, err
	}
	if len(line) <= max {
		return line, nil
	}
	orig := len(line)
	short := *e
	// Round-trip through JSON so the data is plain maps, slices and strings.
	var generic map[string]any
	raw, err := json.Marshal(e.Data)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	short.Data = shorten(generic, max-(len(line)-len(raw)), orig)
	line, err = encode(&short)
	if err != nil {
		return nil, err
	}
	if len(line) > max {
		return nil, fmt.Errorf("event envelope alone exceeds %d bytes", max)
	}
	return line, nil
}

func dataSize(d map[string]any) int {
	b, err := json.Marshal(d)
	if err != nil {
		return 0
	}
	return len(b)
}

func shorten(data map[string]any, budget, originalLine int) map[string]any {
	data["_truncated"] = originalLine
	for dataSize(data) > budget {
		get, set := longestString(data)
		if get == nil || len(get()) <= 64 {
			return map[string]any{"_truncated": originalLine, "_dropped": true}
		}
		s := get()
		cut := len(s) / 2
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		set(fmt.Sprintf("%s…[truncated %d bytes]", s[:cut], len(s)-cut))
	}
	return data
}

func longestString(v any) (get func() string, set func(string)) {
	consider := func(g func() string, s func(string)) {
		if get == nil || len(g()) > len(get()) {
			get, set = g, s
		}
	}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k := range t {
				k := k
				if _, ok := t[k].(string); ok {
					consider(func() string { return t[k].(string) }, func(n string) { t[k] = n })
				} else {
					walk(t[k])
				}
			}
		case []any:
			for i := range t {
				i := i
				if _, ok := t[i].(string); ok {
					consider(func() string { return t[i].(string) }, func(n string) { t[i] = n })
				} else {
					walk(t[i])
				}
			}
		}
	}
	walk(v)
	return get, set
}

func ensureDir(dir string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, d := range missing {
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return os.Chmod(dir, 0o700)
}
