// Package metrics holds the metric family registry, the sample type and the
// hand-rolled Prometheus text renderer.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Type is a Prometheus metric type.
type Type string

// The supported types.
const (
	Gauge   Type = "gauge"
	Counter Type = "counter"
)

// Family describes one metric family: its name, type, help text and the exact
// ordered set of label names every sample must carry.
type Family struct {
	Name   string
	Type   Type
	Help   string
	Labels []string
}

// Label is one name/value pair of a sample.
type Label struct {
	Name  string
	Value string
}

// Sample is one series value.
type Sample struct {
	Family string
	Labels []Label
	Value  float64
}

// Emitter is anything that can produce samples for one database.
type Emitter interface {
	Samples(db string) []Sample
}

// Registry is an ordered set of families. The order is the output order.
type Registry struct {
	families []Family
	index    map[string]int
}

// NewRegistry builds a registry from families, rejecting duplicates.
func NewRegistry(families ...Family) (*Registry, error) {
	r := &Registry{index: map[string]int{}}
	for _, f := range families {
		if err := r.Add(f); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Add appends a family. A later pass adds its own families this way.
func (r *Registry) Add(f Family) error {
	if f.Name == "" || (f.Type != Gauge && f.Type != Counter) {
		return fmt.Errorf("family %q: invalid name or type", f.Name)
	}
	if _, dup := r.index[f.Name]; dup {
		return fmt.Errorf("family %q registered twice", f.Name)
	}
	if len(f.Labels) == 0 || f.Labels[0] != "db" {
		return fmt.Errorf("family %q: every series must carry db as its first label", f.Name)
	}
	r.index[f.Name] = len(r.families)
	r.families = append(r.families, f)
	return nil
}

// Families returns a copy of the registered families in output order.
func (r *Registry) Families() []Family {
	return append([]Family{}, r.families...)
}

// Render writes samples in the Prometheus text exposition format. Families
// appear in registry order with HELP and TYPE exactly once; samples within a
// family are sorted by label values. A sample whose family is unknown, whose
// labels differ from the family's label names, or that duplicates another
// series is an error: the registry is the allowlist.
func (r *Registry) Render(w io.Writer, samples []Sample) error {
	byFamily := make(map[string][]Sample, len(r.families))
	for _, s := range samples {
		idx, ok := r.index[s.Family]
		if !ok {
			return fmt.Errorf("sample for unregistered family %q", s.Family)
		}
		fam := r.families[idx]
		if len(s.Labels) != len(fam.Labels) {
			return fmt.Errorf("family %q: sample has %d labels, want %d", s.Family, len(s.Labels), len(fam.Labels))
		}
		for i, l := range s.Labels {
			if l.Name != fam.Labels[i] {
				return fmt.Errorf("family %q: label %d is %q, want %q", s.Family, i, l.Name, fam.Labels[i])
			}
		}
		byFamily[s.Family] = append(byFamily[s.Family], s)
	}

	var b strings.Builder
	for _, fam := range r.families {
		ss := byFamily[fam.Name]
		if len(ss) == 0 {
			continue
		}
		sort.SliceStable(ss, func(i, j int) bool { return lessLabels(ss[i].Labels, ss[j].Labels) })
		for i := 1; i < len(ss); i++ {
			if !lessLabels(ss[i-1].Labels, ss[i].Labels) {
				return fmt.Errorf("family %q: duplicate series %s", fam.Name, formatLabels(ss[i].Labels))
			}
		}
		fmt.Fprintf(&b, "# HELP %s %s\n", fam.Name, escapeHelp(fam.Help))
		fmt.Fprintf(&b, "# TYPE %s %s\n", fam.Name, fam.Type)
		for _, s := range ss {
			fmt.Fprintf(&b, "%s%s %s\n", fam.Name, formatLabels(s.Labels), formatValue(s.Value))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func lessLabels(a, b []Label) bool {
	for i := range a {
		if a[i].Value != b[i].Value {
			return a[i].Value < b[i].Value
		}
	}
	return false
}

func formatLabels(ls []Label) string {
	if len(ls) == 0 {
		return ""
	}
	parts := make([]string, len(ls))
	for i, l := range ls {
		parts[i] = l.Name + `="` + escapeLabelValue(l.Value) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// formatValue renders a sample value without an exponent so timestamps stay
// readable and exact.
func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// escapeLabelValue escapes exactly backslash, double quote and newline, and
// replaces invalid UTF-8 with U+FFFD. It deliberately does not use Go's %q,
// which would also escape tabs and non-ASCII runes in a way the exposition
// format does not define.
func escapeLabelValue(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// escapeHelp escapes backslash and newline in HELP text.
func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// SeriesLabel builds a label list in order from alternating name, value pairs.
func SeriesLabel(pairs ...string) []Label {
	ls := make([]Label, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		ls = append(ls, Label{Name: pairs[i], Value: pairs[i+1]})
	}
	return ls
}
