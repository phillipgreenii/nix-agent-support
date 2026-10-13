package query

import (
	"net/url"
	"sort"
	"strings"
)

// Key is a query's identity: the configured name plus the arguments that change
// what GitHub is asked.
//
// Arguments that only filter a configured query's result (list_since and
// list_before) are NOT part of a Key: they are applied locally to the cached
// ids. A Key with no Args is the configured query itself (its row is stored as
// configured and the scheduler re-runs it); a Key with Args is an AD-HOC key
// (cached under adhoc_query_ttl, never re-run).
type Key struct {
	// Name is the configured query name.
	Name string
	// Args are the arguments that change what GitHub is asked: each pair is
	// appended to every expression of the configured query as the GitHub search
	// qualifier "k:v" (a value with white space is quoted). Nil or empty means
	// none.
	Args map[string]string
}

// Adhoc reports whether k differs from the configured query itself.
func (k Key) Adhoc() bool { return len(k.Args) > 0 }

// String is the canonical key text, the key of the query row in the store: the
// name alone for the configured query, otherwise the name, "?" and the
// arguments as sorted, URL-escaped k=v pairs joined by "&". Two Keys with the
// same name and the same arguments, however the map was built, have the same
// String.
func (k Key) String() string {
	if !k.Adhoc() {
		return k.Name
	}
	v := url.Values{}
	for name, val := range k.Args {
		v.Set(name, val)
	}
	return k.Name + "?" + v.Encode() // Encode sorts by key
}

// qualifiers renders the arguments as GitHub search qualifiers in a stable
// order, each prefixed by a space; empty when there are none.
func (k Key) qualifiers() string {
	names := make([]string, 0, len(k.Args))
	for name := range k.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		val := k.Args[name]
		b.WriteByte(' ')
		b.WriteString(name)
		b.WriteByte(':')
		if strings.ContainsAny(val, " \t\"") {
			b.WriteString(`"` + strings.ReplaceAll(val, `"`, `\"`) + `"`)
		} else {
			b.WriteString(val)
		}
	}
	return b.String()
}
