package builtin_test

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/ccaudit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/configinventory"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/hookdecisions"
)

// TestBuiltinProviderQueriesAreKnown guards against a built-in provider
// declaring a query its binary rejects (which exits 2 and fails every run).
func TestBuiltinProviderQueriesAreKnown(t *testing.T) {
	known := map[string][]string{
		"ccaudit":          ccaudit.AllQueries,
		"hook-decisions":   hookdecisions.QueryNames(),
		"config-inventory": {configinventory.QueryName},
	}
	for _, p := range builtin.Providers() {
		k, ok := known[p.Name]
		if !ok {
			t.Errorf("provider %q has no known-query list in this test", p.Name)
			continue
		}
		for lens, qs := range p.Queries {
			for _, q := range qs {
				if !slices.Contains(k, q) {
					t.Errorf("provider %q lens %q query %q is not implemented (known: %v)", p.Name, lens, q, k)
				}
			}
		}
	}
}
