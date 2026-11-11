package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// The requires clause compiler.
//
// A role declares requires = [ "<resource> <op> <LEVEL>", ... ]; the six
// operators work by position in the resource's scale ("greater" means better)
// and clauses are ANDed. Exported names: ImplicitClauseIndex, Compiled,
// CompileRequires, ExpandClause, QueryPoll, Config.PollingUnion,
// Config.RequiresWarnings.
//
// Clause indices are 0-based positions in the requires list; messages print
// them as requires[N]. A clause the registry adds because a resource has a
// non-ANY ignored value and the role names no clause on it has index
// ImplicitClauseIndex.

// ImplicitClauseIndex is the Index of a clause added from a resource's ignored
// value rather than written in a requires list.
const ImplicitClauseIndex = -1

// Compiled is one registration's compiled requirement. Requirement is the
// input of levels.Decide (one levels.Clause per original clause, each with its
// own allowed set and its original text, kept for explanations). Allowed is
// the ONE allowed-level set per resource (the intersection of that resource's
// clauses, scale order); a resource absent from it does not matter to the
// registration.
type Compiled struct {
	Requirement levels.Requirement
	Allowed     map[string][]string
}

// CompileRequires compiles the clause texts of one registration against the
// registry. Every problem is returned, each naming the registration and the
// clause index. An unknown resource or level, a malformed clause and an empty
// combined allowed set are errors.
func CompileRequires(rs ResourceSet, registration string, texts []string) (Compiled, []error) {
	var errs []error
	fail := func(i int, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s requires[%d] %q: %s", registration, i, texts[i], fmt.Sprintf(format, a...)))
	}
	out := Compiled{Allowed: map[string][]string{}}
	mentioned := map[string]bool{}
	for i, text := range texts {
		f := strings.Fields(text)
		if len(f) != 3 {
			fail(i, "want \"<resource> <op> <LEVEL>\"")
			continue
		}
		res, ok := rs.Find(f[0])
		if !ok {
			fail(i, "unknown resource %q", f[0])
			continue
		}
		op, lvl := levels.Op(f[1]), f[2]
		if !validOp(op) {
			fail(i, "unknown operator %q (use ==, !=, <, <=, >, >=; prefer >= and <=)", f[1])
			continue
		}
		if !slices.Contains(res.Scale, lvl) {
			fail(i, "unknown level %q for resource %q (scale: %s)", lvl, res.Name, strings.Join(res.Scale, " > "))
			continue
		}
		allowed := levels.AllowedLevels(levels.Scale{Levels: res.Scale}, op, lvl)
		if len(allowed) == 0 {
			fail(i, "allows no level of %q", res.Name)
			continue
		}
		mentioned[res.Name] = true
		out.Requirement.Clauses = append(out.Requirement.Clauses, levels.Clause{Index: i, Text: text, Resource: res.Name, Allowed: allowed})
		if cur, has := out.Allowed[res.Name]; has {
			allowed = intersectInOrder(res.Scale, cur, allowed)
			if len(allowed) == 0 {
				fail(i, "the clauses on %q together allow no level", res.Name)
				continue
			}
		}
		out.Allowed[res.Name] = allowed
	}
	// A resource the registration does not mention uses the registry's ignored.
	for _, res := range rs {
		if mentioned[res.Name] || res.Ignored == IgnoredAny {
			continue
		}
		allowed, err := ignoredAllowed(res)
		if err != nil {
			continue // buildResources rejected it already
		}
		out.Requirement.Clauses = append(out.Requirement.Clauses, levels.Clause{
			Index: ImplicitClauseIndex, Text: res.Name + " " + res.Ignored, Resource: res.Name, Allowed: allowed,
		})
		out.Allowed[res.Name] = allowed
	}
	return out, errs
}

// intersectInOrder returns the levels of scale that are in both a and b.
func intersectInOrder(scale, a, b []string) []string {
	var out []string
	for _, l := range scale {
		if slices.Contains(a, l) && slices.Contains(b, l) {
			out = append(out, l)
		}
	}
	return out
}

// ExpandClause renders a clause with its expansion:
// "disk >= OK (allowed: PLENTY, OK)". An implicit clause is marked.
func ExpandClause(c levels.Clause) string {
	s := fmt.Sprintf("%s (allowed: %s)", c.Text, strings.Join(c.Allowed, ", "))
	if c.Index == ImplicitClauseIndex {
		s += " [from the resource's ignored value]"
	}
	return s
}

// QueryPoll says whether and how a query is polled. A query is polled when ANY
// consumer could run. Allowed holds, per resource, the union of the consumers'
// allowed sets; a resource absent from it is one some consumer does not care
// about (any level). The union over-approximates the union of the consumers'
// conjunctions: it can over-poll and never under-polls. Exempt marks a query
// no requirement gates: a level-source query (it has no consumer) and the
// timer query.
type QueryPoll struct {
	Polled  bool
	Exempt  bool
	Allowed map[string][]string
}

// PollingUnion computes the QueryPoll of every configured query. The consumers
// of a query are the enabled roles that bind one of its emitted event types;
// no consumer, or only disabled consumers, means not polled.
func (c Config) PollingUnion() map[string]QueryPoll {
	out := make(map[string]QueryPoll, len(c.Queries))
	for _, src := range c.Queries {
		if src.LevelSource != "" || (src.Query != nil && query.IsTimer(src.Query)) {
			out[src.Name] = QueryPoll{Polled: true, Exempt: true}
			continue
		}
		var emits []string
		if src.Query != nil {
			emits = src.Query.Emits()
		}
		var consumers []Compiled
		for _, role := range c.Roles {
			if !role.Enabled || !bindsAny(role.Binds, emits) {
				continue
			}
			consumers = append(consumers, c.Requirements[role.Name])
		}
		if len(consumers) == 0 {
			out[src.Name] = QueryPoll{}
			continue
		}
		union := map[string][]string{}
		for _, res := range c.Resources {
			var u []string
			caresEverywhere := true
			for _, cons := range consumers {
				set, ok := cons.Allowed[res.Name]
				if !ok {
					caresEverywhere = false
					break
				}
				u = unionInOrder(res.Scale, u, set)
			}
			if caresEverywhere {
				union[res.Name] = u
			}
		}
		out[src.Name] = QueryPoll{Polled: true, Allowed: union}
	}
	return out
}

func bindsAny(binds, emits []string) bool {
	for _, b := range binds {
		if slices.Contains(emits, b) {
			return true
		}
	}
	return false
}

func unionInOrder(scale, a, b []string) []string {
	var out []string
	for _, l := range scale {
		if slices.Contains(a, l) || slices.Contains(b, l) {
			out = append(out, l)
		}
	}
	return out
}

// RequiresWarnings returns one warning per requires clause (of an enabled or
// disabled role alike) that names a resource with no sensor on this machine.
// The sensor is present when it is the router's own, when a registered
// level-source query names it, or when it is listed in Config.MachineSensors.
// The CLI prints the returned lines; they are not errors.
func (c Config) RequiresWarnings() []string {
	present := map[string]bool{RouterSensor: true}
	for _, s := range c.MachineSensors {
		present[s] = true
	}
	for _, src := range c.Queries {
		if src.LevelSource != "" {
			present[src.LevelSource] = true
		}
	}
	var out []string
	for _, role := range c.Roles {
		for _, cl := range c.Requirements[role.Name].Requirement.Clauses {
			res, ok := c.Resources.Find(cl.Resource)
			if !ok || cl.Index == ImplicitClauseIndex || present[res.Sensor] {
				continue
			}
			out = append(out, fmt.Sprintf("role %q requires[%d] %q: no sensor configured for resource %q", role.Name, cl.Index, cl.Text, res.Name))
		}
	}
	return out
}
