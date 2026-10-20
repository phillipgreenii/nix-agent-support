// Package wrap is the shared wrapper library for pg-router: it decodes and
// validates wrap entries, composes them one layer per entry around a payload
// argv, provides the adapter-owned ready-marker stage, builds the minimal base
// environment and runs a payload chain in its own process group.
//
// It is a deliberately non-internal package so pg-router core (queries) and the
// ccpool handler (both executors) apply one composition rule. It imports the
// standard library only; the array-entry template engine is supplied by the
// caller as a RenderFunc.
package wrap

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/template/parse"
)

// Entry is one wrap layer. Exactly one field is set: Shell for a string entry
// (shell-parsed, exec'd by /bin/sh), Argv for an array entry (exec'd directly,
// elements rendered through the caller's template engine, never shell-parsed).
type Entry struct {
	Shell string
	Argv  []string
}

// RenderFunc renders one element of an array entry (for example through the
// handler's text/template engine). An element without template actions renders
// to itself.
type RenderFunc func(elem string) (string, error)

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

// hasTemplateAction reports whether s holds a Go text/template action under the
// default delimiters. A parse error (an unclosed "{{", for example) is returned
// as an error. Function names are not checked.
func hasTemplateAction(s string) (bool, error) {
	if !strings.Contains(s, "{{") {
		return false, nil
	}
	t := parse.New("wrap")
	t.Mode = parse.SkipFuncCheck
	if _, err := t.Parse(s, "", "", map[string]*parse.Tree{}); err != nil {
		return false, err
	}
	// text/template parsing succeeded and the text holds "{{": it is an action
	// (including a comment or a define, which also count as template syntax).
	return true, nil
}

// Validate checks entries. Every string entry's first word and every array
// entry's first element MUST be an absolute path (WC-9, checked on the raw,
// unrendered text). A string entry MUST NOT contain a template action. An array
// entry MAY contain template actions only when allowTemplates is true. A parse
// error in an entry is a validation error.
func Validate(entries []Entry, allowTemplates bool) error {
	for i, e := range entries {
		if (e.Shell != "") == (len(e.Argv) > 0) {
			return fmt.Errorf("wrap[%d]: exactly one of a string entry or an array entry must be set", i)
		}
		if e.Shell != "" {
			first := ""
			if f := strings.Fields(e.Shell); len(f) > 0 {
				first = f[0]
			}
			if !filepath.IsAbs(first) {
				return fmt.Errorf("wrap[%d]: first word %q of a string entry must be an absolute path", i, first)
			}
			act, err := hasTemplateAction(e.Shell)
			if err != nil {
				return fmt.Errorf("wrap[%d]: %w", i, err)
			}
			if act {
				return fmt.Errorf("wrap[%d]: a string entry must not contain a template action", i)
			}
			continue
		}
		if !filepath.IsAbs(e.Argv[0]) {
			return fmt.Errorf("wrap[%d]: first element %q of an array entry must be an absolute path", i, e.Argv[0])
		}
		for j, el := range e.Argv {
			act, err := hasTemplateAction(el)
			if err != nil {
				return fmt.Errorf("wrap[%d][%d]: %w", i, j, err)
			}
			if act && !allowTemplates {
				return fmt.Errorf("wrap[%d][%d]: template actions are not allowed in this wrap", i, j)
			}
		}
	}
	return nil
}

// Compose returns one flat argv: one layer per entry, outermost first, around
// payload. A string entry becomes /bin/sh -c 'exec <wrap_i> "$@"' sh <rest>; an
// array entry becomes its rendered elements followed by <rest>, with no shell.
// payload arrives as "$@" at every layer and is never shell-parsed. render may be
// nil when no array entry holds a template action; a render error aborts
// composition. The ready-marker stage is NOT added here: the adapter appends
// MarkerStage() as the last entry.
func Compose(entries []Entry, render RenderFunc, payload []string) ([]string, error) {
	var layers [][]string
	for i, e := range entries {
		switch {
		case e.Shell != "" && len(e.Argv) == 0:
			layers = append(layers, []string{"/bin/sh", "-c", "exec " + e.Shell + ` "$@"`, "sh"})
		case e.Shell == "" && len(e.Argv) > 0:
			layer := make([]string, 0, len(e.Argv))
			for j, el := range e.Argv {
				if render == nil {
					act, err := hasTemplateAction(el)
					if err != nil {
						return nil, fmt.Errorf("wrap[%d][%d]: %w", i, j, err)
					}
					if act {
						return nil, fmt.Errorf("wrap[%d][%d]: template action but no render function", i, j)
					}
					layer = append(layer, el)
					continue
				}
				s, err := render(el)
				if err != nil {
					return nil, fmt.Errorf("wrap[%d][%d]: render: %w", i, j, err)
				}
				layer = append(layer, s)
			}
			layers = append(layers, layer)
		default:
			return nil, fmt.Errorf("wrap[%d]: %w", i, errBadEntry)
		}
	}
	var out []string
	for _, l := range layers {
		out = append(out, l...)
	}
	return append(out, payload...), nil
}

var errBadEntry = errors.New("exactly one of a string entry or an array entry must be set")

// Display renders argv as one shell-quoted, human-readable line (for example for
// pg-router config --show). Words made only of safe characters are left bare.
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
