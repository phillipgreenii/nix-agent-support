package query

import (
	"sort"
	"strings"

	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
)

// Key is a query's identity: the configured name plus the arguments that change
// what GitHub is asked (see the shared query.Key, which this aliases).
//
// A Key's Args are appended to every expression of the configured query as the
// GitHub search qualifier "k:v" (a value with white space is quoted); see
// qualifiers.
type Key = shared.Key

// qualifiers renders k's arguments as GitHub search qualifiers in a stable
// order, each prefixed by a space; empty when there are none.
func qualifiers(k Key) string {
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
