package query

import (
	"fmt"
	"sort"

	"github.com/BurntSushi/toml"
)

// Factory decodes a query type's same-named sub-table (held as a Primitive) into a
// concrete Query, installs the [[query]]-level Meta (emits/trigger), then
// validates it.
type Factory func(meta Meta, md toml.MetaData, prim toml.Primitive) (Query, error)

// Factories is an instance-scoped registry (NOT package-level init() maps — that
// fights the codebase's constructor-injection convention). NewQueryFactories seeds
// the built-in query types; adding a type is one line here.
type Factories struct{ m map[string]Factory }

func NewQueryFactories() *Factories {
	f := &Factories{m: map[string]Factory{}}
	f.m["command"] = decodeInto(func() Query { return &CommandQuery{} })
	// beads-ready / beads-list / github-issues / jira-issues were removed here
	// (pg2-n75tk): each typed "how another tool is configured" into Core, and
	// jira-issues was structurally unsatisfiable (its backing command exists
	// only in a downstream flake). command is the one surviving generic
	// source; see MIGRATION.md. query.BeadsReady itself is NOT deleted — it
	// still backs the in-Go built-in default query set (roles.BuiltinQuerySet),
	// which never goes through this factory.
	return f
}

// decodeInto builds a Factory that PrimitiveDecodes into a fresh pointer of the
// concrete type, installs the [[query]]-level Meta, validates, and returns the
// value (dereferenced). Meta is installed BEFORE Validate so a query type can
// validate its emits.
func decodeInto(mk func() Query) Factory {
	return func(meta Meta, md toml.MetaData, prim toml.Primitive) (Query, error) {
		q := mk()
		if err := md.PrimitiveDecode(prim, q); err != nil {
			return nil, err
		}
		if ms, ok := q.(metaSetter); ok {
			ms.setMeta(meta)
		}
		if err := q.Validate(); err != nil {
			return nil, err
		}
		return derefQuery(q), nil
	}
}

// Decode looks up the factory for typ and decodes prim into the concrete Query,
// installing the [[query]]-level meta (emits/trigger).
func (f *Factories) Decode(typ string, meta Meta, md toml.MetaData, prim toml.Primitive) (Query, error) {
	fn, ok := f.m[typ]
	if !ok {
		return nil, fmt.Errorf("unknown query type %q (known: %s)", typ, f.known())
	}
	return fn(meta, md, prim)
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
// the package compares against (CommandQuery, not *CommandQuery).
func derefQuery(q Query) Query {
	switch v := q.(type) {
	case *CommandQuery:
		return *v
	}
	return q
}
