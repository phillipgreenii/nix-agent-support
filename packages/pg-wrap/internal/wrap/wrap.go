// Package wrap is pg-wrap's library: it decodes and validates wrap entries,
// composes them one layer per entry around a payload argv, builds the minimal
// base environment and runs a payload chain in its own process group.
//
// It imports the standard library only. A spec has no templating: an array
// entry's elements are used literally.
package wrap

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Entry is one wrap layer. Exactly one field is set: Shell for a string entry
// (shell-parsed, exec'd by /bin/sh), Argv for an array entry (exec'd directly,
// never shell-parsed).
type Entry struct {
	Shell string
	Argv  []string
}

// Decode turns a decoded TOML/JSON value into wrap entries. v MUST be a list
// (nil gives nil, nil) whose elements are each a string (string entry) or a list
// of strings (array entry). Any other shape, an empty array entry, an empty or
// whitespace-only string entry, or a nested list is an error.
func Decode(v any) ([]Entry, error) {
	if v == nil {
		return nil, nil
	}
	var elems []any
	switch t := v.(type) {
	case []any:
		elems = t
	case []string:
		elems = make([]any, len(t))
		for i, s := range t {
			elems[i] = s
		}
	case [][]string:
		elems = make([]any, len(t))
		for i, s := range t {
			elems[i] = s
		}
	default:
		return nil, fmt.Errorf("wrap: want a list of entries, got %T", v)
	}
	out := make([]Entry, 0, len(elems))
	for i, e := range elems {
		switch t := e.(type) {
		case string:
			if strings.TrimSpace(t) == "" {
				return nil, fmt.Errorf("wrap[%d]: empty string entry", i)
			}
			out = append(out, Entry{Shell: t})
		case []string:
			if len(t) == 0 {
				return nil, fmt.Errorf("wrap[%d]: empty array entry", i)
			}
			out = append(out, Entry{Argv: append([]string(nil), t...)})
		case []any:
			if len(t) == 0 {
				return nil, fmt.Errorf("wrap[%d]: empty array entry", i)
			}
			argv := make([]string, len(t))
			for j, x := range t {
				s, ok := x.(string)
				if !ok {
					return nil, fmt.Errorf("wrap[%d][%d]: array entry elements must be strings, got %T", i, j, x)
				}
				argv[j] = s
			}
			out = append(out, Entry{Argv: argv})
		default:
			return nil, fmt.Errorf("wrap[%d]: entry must be a string or a list of strings, got %T", i, e)
		}
	}
	return out, nil
}

// FirstWord returns the first word of a string entry or the first element of an
// array entry: the program the layer names. It is "" for an entry with neither.
func (e Entry) FirstWord() string {
	if e.Shell != "" {
		if f := strings.Fields(e.Shell); len(f) > 0 {
			return f[0]
		}
		return ""
	}
	if len(e.Argv) > 0 {
		return e.Argv[0]
	}
	return ""
}

// CheckEntry reports why one entry is unusable, or nil. Exactly one of a string
// entry or an array entry MUST be set, and its first word or first element MUST
// be an absolute path (WC-9).
func CheckEntry(e Entry) error {
	if (e.Shell != "") == (len(e.Argv) > 0) {
		return errBadEntry
	}
	first := e.FirstWord()
	if filepath.IsAbs(first) {
		return nil
	}
	if e.Shell != "" {
		return fmt.Errorf("first word %q of a string entry must be an absolute path", first)
	}
	return fmt.Errorf("first element %q of an array entry must be an absolute path", first)
}

// Validate checks entries: every string entry's first word and every array
// entry's first element MUST be an absolute path (WC-9). The first failure is
// returned, prefixed with the entry's index.
func Validate(entries []Entry) error {
	for i, e := range entries {
		if err := CheckEntry(e); err != nil {
			return fmt.Errorf("wrap[%d]: %w", i, err)
		}
	}
	return nil
}

// Compose returns one flat argv: one layer per entry, outermost first, around
// payload. A string entry becomes /bin/sh -c 'exec <wrap_i> "$@"' sh <rest>; an
// array entry becomes its elements followed by <rest>, with no shell. payload
// arrives as "$@" at every layer and is never shell-parsed.
func Compose(entries []Entry, payload []string) ([]string, error) {
	var out []string
	for i, e := range entries {
		layer, err := Layer(e)
		if err != nil {
			return nil, fmt.Errorf("wrap[%d]: %w", i, err)
		}
		out = append(out, layer...)
	}
	return append(out, payload...), nil
}

// Layer returns the words one entry contributes ahead of the rest of the chain.
func Layer(e Entry) ([]string, error) {
	switch {
	case e.Shell != "" && len(e.Argv) == 0:
		return []string{"/bin/sh", "-c", "exec " + e.Shell + ` "$@"`, "sh"}, nil
	case e.Shell == "" && len(e.Argv) > 0:
		return append([]string(nil), e.Argv...), nil
	default:
		return nil, errBadEntry
	}
}

var errBadEntry = errors.New("exactly one of a string entry or an array entry must be set")

// Display renders argv as one shell-quoted, human-readable line (for example for
// pg-wrap show). Words made only of safe characters are left bare.
func Display(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = quote(a)
	}
	return strings.Join(parts, " ")
}

func quote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_@%+=:,./-", r):
		default:
			safe = false
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
