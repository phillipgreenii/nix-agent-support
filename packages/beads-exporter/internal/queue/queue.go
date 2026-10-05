// Package queue classifies the configured queue flag lists and filters bd ready
// results in process.
//
// A queue is a named list of bd ready flags. Each list falls into exactly one
// of three classes, decided when the configuration is loaded:
//
//   - client-side: only --label, --exclude-label and --exclude-type. The queue
//     is computed from the single shared bd ready result, so it costs no spawn.
//   - spawn-only: at least one other read-only flag from the allowlist. The
//     exporter spawns bd ready with the queue's flags for it.
//   - rejected: anything else (a write flag, an unknown flag, a positional
//     argument, a flag missing its value). Configuration loading fails.
package queue

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
)

// Class is a queue's evaluation strategy.
type Class string

// The queue classes.
const (
	ClientSide Class = "client-side"
	SpawnOnly  Class = "spawn-only"
)

// clientFlags are the flags evaluated in process. All take one value.
var clientFlags = map[string]bool{
	"--label":         true,
	"--exclude-label": true,
	"--exclude-type":  true,
}

// spawnFlags is the allowlist of read-only bd ready flags that force a spawn.
// The value is the flag's arity: 1 takes a value, 0 is a boolean switch.
var spawnFlags = map[string]int{
	"--priority":   1,
	"--parent":     1,
	"--type":       1,
	"--assignee":   1,
	"--unassigned": 0,
	"--label-any":  1,
}

// Flags returns every flag name a queue may carry, sorted. The bd flag check
// proves each one exists in a bd package.
func Flags() []string {
	var out []string
	for f := range clientFlags {
		out = append(out, f)
	}
	for f := range spawnFlags {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Queue is a named, classified flag list.
type Queue struct {
	Name  string
	Args  []string
	Class Class
	// filter is set for client-side queues.
	filter *Filter
}

// Filter is the in-process predicate of a client-side queue.
type Filter struct {
	// Labels must ALL be present (--label is an AND across all occurrences).
	Labels []string
	// ExcludeLabels exclude a bead carrying ANY of them.
	ExcludeLabels []string
	// ExcludeTypes exclude a bead of ANY of these types.
	ExcludeTypes []string
}

// New classifies args and returns the Queue, or an error for a rejected list.
func New(name string, args []string) (Queue, error) {
	class, filter, err := classify(args)
	if err != nil {
		return Queue{}, fmt.Errorf("queue %q: %w", name, err)
	}
	q := Queue{Name: name, Args: append([]string{}, args...), Class: class}
	if class == ClientSide {
		q.filter = filter
	}
	return q, nil
}

// Filter returns the in-process predicate of a client-side queue, or nil.
func (q Queue) Filter() *Filter { return q.filter }

func classify(args []string) (Class, *Filter, error) {
	f := &Filter{}
	spawn := false
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if !strings.HasPrefix(tok, "--") {
			return "", nil, fmt.Errorf("unexpected argument %q: only long flags are accepted", tok)
		}
		name, value, hasValue := strings.Cut(tok, "=")
		switch {
		case clientFlags[name]:
			if !hasValue {
				if i+1 >= len(args) {
					return "", nil, fmt.Errorf("flag %s requires a value", name)
				}
				i++
				value = args[i]
			}
			vals := splitList(value)
			if len(vals) == 0 {
				return "", nil, fmt.Errorf("flag %s has an empty value", name)
			}
			switch name {
			case "--label":
				f.Labels = append(f.Labels, vals...)
			case "--exclude-label":
				f.ExcludeLabels = append(f.ExcludeLabels, vals...)
			case "--exclude-type":
				f.ExcludeTypes = append(f.ExcludeTypes, vals...)
			}
		default:
			arity, ok := spawnFlags[name]
			if !ok {
				return "", nil, fmt.Errorf("flag %s is not an allowed read-only queue filter", name)
			}
			spawn = true
			if arity == 1 && !hasValue {
				if i+1 >= len(args) {
					return "", nil, fmt.Errorf("flag %s requires a value", name)
				}
				i++
			}
			if arity == 0 && hasValue {
				return "", nil, fmt.Errorf("flag %s takes no value", name)
			}
		}
	}
	if spawn {
		return SpawnOnly, nil, nil
	}
	return ClientSide, f, nil
}

// splitList splits a comma list, trimming blanks.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Match reports whether b belongs to the queue. Templates never do: bd ready
// returns them with is_template set, and a template is not workable.
func (f *Filter) Match(b bd.Bead) bool {
	if b.IsTemplate {
		return false
	}
	have := make(map[string]struct{}, len(b.Labels))
	for _, l := range b.Labels {
		have[l] = struct{}{}
	}
	for _, want := range f.Labels {
		if _, ok := have[want]; !ok {
			return false
		}
	}
	for _, bad := range f.ExcludeLabels {
		if _, ok := have[bad]; ok {
			return false
		}
	}
	for _, t := range f.ExcludeTypes {
		if b.IssueType == t {
			return false
		}
	}
	return true
}

// Apply filters beads through the queue's predicate.
func (f *Filter) Apply(beads []bd.Bead) []bd.Bead {
	var out []bd.Bead
	for _, b := range beads {
		if f.Match(b) {
			out = append(out, b)
		}
	}
	return out
}

// DropTemplates removes templates, for a spawn-only result.
func DropTemplates(beads []bd.Bead) []bd.Bead {
	var out []bd.Bead
	for _, b := range beads {
		if !b.IsTemplate {
			out = append(out, b)
		}
	}
	return out
}
