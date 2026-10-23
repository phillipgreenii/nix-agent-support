package query

import "net/url"

// Key is a query's identity: the configured name plus the arguments that change
// what the origin is asked.
//
// Arguments that only filter a configured query's result are NOT part of a Key:
// they are applied locally to the cached ids. A Key with no Args is the
// configured query itself (its row is stored as configured and the scheduler
// re-runs it); a Key with Args is an AD-HOC key (cached under the ad-hoc ttl,
// never re-run).
type Key struct {
	// Name is the configured query name.
	Name string
	// Args are the arguments that change what the origin is asked; how a
	// connector turns them into a request is the Lister's business. Nil or
	// empty means none.
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
