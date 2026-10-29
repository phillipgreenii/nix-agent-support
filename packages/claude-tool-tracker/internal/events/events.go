// Package events is the producer side of the claude-tool-tracker event log:
// the envelope, the file naming and permissions, and the single-write append.
// The contract is documented in README.md ("Event format") and
// docs/event-kinds.md.
package events

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Schema is the format and version stamped on every line.
const Schema = "tool-events/1"

// MaxLineBytes is the largest line (newline included) one append writes.
const MaxLineBytes = 65536

const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

var (
	sourceRe  = regexp.MustCompile(`^[a-z0-9-]+$`)
	sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	fileRe    = regexp.MustCompile(`^tool-events\.([A-Za-z0-9_-]+)\.([a-z0-9-]+)\.jsonl$`)
)

// Envelope is one event line.
type Envelope struct {
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

// kindsWithoutToolUseID lists the kinds that may omit tool_use_id. Every other
// kind (known or not) requires it.
var kindsWithoutToolUseID = map[string]bool{
	"tracker.session_end":      true,
	"tracker.permission_offer": true, // PermissionRequest input declares no tool_use_id
}

// ValidSource reports whether s may be a source (the file name's middle part).
func ValidSource(s string) bool { return sourceRe.MatchString(s) }

// ValidSessionID reports whether s is safe as a file name part.
func ValidSessionID(s string) bool { return sessionRe.MatchString(s) }

// ParseFileName extracts session id and source from an event file name, or
// reports false when the name does not match the whole pattern.
func ParseFileName(name string) (session, source string, ok bool) {
	m := fileRe.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// FileName is the event file for a session and source.
func FileName(session, source string) string {
	return "tool-events." + session + "." + source + ".jsonl"
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

// NewEventID returns a unique id: 12 hex digits of the millisecond clock then
// 16 hex digits from crypto/rand.
func NewEventID(now time.Time) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; fall back to nanos.
		return fmt.Sprintf("%012x%016x", now.UnixMilli(), now.UnixNano())
	}
	return fmt.Sprintf("%012x%s", now.UnixMilli(), hex.EncodeToString(b[:]))
}

// Validate checks the envelope's required fields and the source/kind rules.
func (e *Envelope) Validate() error {
	switch {
	case e.Schema != Schema:
		return fmt.Errorf("schema must be %q", Schema)
	case e.EventID == "":
		return errors.New("event_id is required")
	case e.TS <= 0:
		return errors.New("ts is required")
	case !ValidSource(e.Source):
		return fmt.Errorf("source %q must match [a-z0-9-]+", e.Source)
	case !strings.HasPrefix(e.Kind, e.Source+".") || len(e.Kind) == len(e.Source)+1:
		return fmt.Errorf("kind %q must be <source>.<name> for source %q", e.Kind, e.Source)
	case !ValidSessionID(e.SessionID):
		return fmt.Errorf("session_id %q must match [A-Za-z0-9_-]+", e.SessionID)
	case e.ToolUseID == "" && !kindsWithoutToolUseID[e.Kind]:
		return fmt.Errorf("tool_use_id is required for kind %q", e.Kind)
	case e.Data == nil:
		return errors.New("data is required (use {})")
	}
	return nil
}

// Marshal renders the envelope as one line, newline included, shortened to at
// most max bytes by Shorten when needed.
func (e *Envelope) Marshal(max int) ([]byte, error) {
	line, err := encode(e)
	if err != nil {
		return nil, err
	}
	if len(line) <= max {
		return line, nil
	}
	orig := len(line)
	short := *e
	short.Data = Shorten(e.Data, max-(len(line)-dataSize(e.Data)), orig)
	line, err = encode(&short)
	if err != nil {
		return nil, err
	}
	if len(line) > max {
		return nil, fmt.Errorf("event envelope alone exceeds %d bytes", max)
	}
	return line, nil
}

func encode(e *Envelope) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil // Encode appends the newline
}

func dataSize(d map[string]any) int {
	b, err := json.Marshal(d)
	if err != nil {
		return 0
	}
	return len(b)
}

// Shorten returns a copy of data whose JSON size is at most budget bytes. It
// repeatedly halves the longest string anywhere in data (cutting on a rune
// boundary and appending a marker naming the bytes removed) until the data
// fits, and records data["_truncated"] = original line size. When no string is
// left worth halving it drops the data to only the _truncated and _dropped keys.
func Shorten(data map[string]any, budget, originalLine int) map[string]any {
	out := deepCopy(data).(map[string]any)
	out["_truncated"] = originalLine
	for dataSize(out) > budget {
		holder := longestString(out)
		if holder == nil || len(holder.get()) <= 64 {
			return map[string]any{"_truncated": originalLine, "_dropped": true}
		}
		s := holder.get()
		cut := len(s) / 2
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		holder.set(fmt.Sprintf("%s…[truncated %d bytes]", s[:cut], len(s)-cut))
	}
	return out
}

type strRef struct {
	get func() string
	set func(string)
}

func longestString(v any) *strRef {
	var best *strRef
	var walk func(v any)
	consider := func(get func() string, set func(string)) {
		if best == nil || len(get()) > len(best.get()) {
			best = &strRef{get, set}
		}
	}
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k := range t {
				k := k
				if s, ok := t[k].(string); ok {
					consider(func() string { return t[k].(string) }, func(n string) { t[k] = n })
					_ = s
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
	return best
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = deepCopy(x)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, x := range t {
			s[i] = deepCopy(x)
		}
		return s
	default:
		return v
	}
}

// Append validates e, creates the directory (0700) and file (0600) as needed,
// forces both modes whatever the umask, and writes the line with ONE write call
// on an O_APPEND descriptor. It returns the path written.
func Append(e *Envelope) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	line, err := e.Marshal(MaxLineBytes)
	if err != nil {
		return "", err
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := ensureDir(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName(e.SessionID, e.Source))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, fileMode)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(fileMode); err != nil {
		return "", err
	}
	if n, err := f.Write(line); err != nil {
		return "", err
	} else if n != len(line) {
		return "", fmt.Errorf("short write: %d of %d bytes", n, len(line))
	}
	return path, nil
}

// ensureDir creates dir and its missing parents with mode 0700 and chmods the
// directories it created (and dir itself) to 0700 whatever the umask.
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
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	for _, d := range missing {
		if err := os.Chmod(d, dirMode); err != nil {
			return err
		}
	}
	return os.Chmod(dir, dirMode)
}
