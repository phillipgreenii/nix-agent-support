package query

import (
	"fmt"
	"sort"

	"github.com/BurntSushi/toml"
)

// Factory decodes a query type's same-named sub-table (held as a Primitive) into a
// concrete Query, then validates it.
type Factory func(md toml.MetaData, prim toml.Primitive) (Query, error)

// Factories is an instance-scoped registry (NOT package-level init() maps — that
// fights the codebase's constructor-injection convention). NewQueryFactories seeds
// the built-in query types; adding a type is one line here.
type Factories struct{ m map[string]Factory }

func NewQueryFactories() *Factories {
	f := &Factories{m: map[string]Factory{}}
	f.m["beads-ready"] = decodeInto(func() Query { return &BeadsReady{} })
	f.m["beads-list"] = decodeInto(func() Query { return &BeadsList{} })
	f.m["command"] = decodeInto(func() Query { return &CommandQuery{} })
	f.m["github-issues"] = decodeInto(func() Query { return &GitHubIssues{} })
	f.m["jira-issues"] = decodeInto(func() Query { return &JiraIssues{} })
	return f
}

// decodeInto builds a Factory that PrimitiveDecodes into a fresh pointer of the
// concrete type, validates, and returns the value (dereferenced).
func decodeInto(mk func() Query) Factory {
	return func(md toml.MetaData, prim toml.Primitive) (Query, error) {
		q := mk()
		if err := md.PrimitiveDecode(prim, q); err != nil {
			return nil, err
		}
		if err := q.Validate(); err != nil {
			return nil, err
		}
		return derefQuery(q), nil
	}
}

// Decode looks up the factory for typ and decodes prim into the concrete Query.
func (f *Factories) Decode(typ string, md toml.MetaData, prim toml.Primitive) (Query, error) {
	fn, ok := f.m[typ]
	if !ok {
		return nil, fmt.Errorf("unknown query type %q (known: %s)", typ, f.known())
	}
	return fn(md, prim)
}

func (f *Factories) known() string {
	ks := make([]string, 0, len(f.m))
	for k := range f.m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return fmt.Sprint(ks)
}

// derefQuery converts the *T used for decoding back to the value form the rest of
// the package compares against (BeadsReady, not *BeadsReady).
func derefQuery(q Query) Query {
	switch v := q.(type) {
	case *BeadsReady:
		return *v
	case *BeadsList:
		return *v
	case *CommandQuery:
		return *v
	case *GitHubIssues:
		return *v
	case *JiraIssues:
		return *v
	}
	return q
}
