//go:build !integration

// Package rapidtier sizes the rapid property tests by tier. The unit gate
// (plain `go test ./...`) runs each capped property test for a smaller number
// of checks than rapid's default of 100; the integration tier (`-tags
// integration`, checks.<system>.pg-task-focus-property-tests) runs them at the
// full count. An explicit `-rapid.checks` flag or RAPID_CHECKS variable wins in
// both tiers. It imports nothing from this module, so any test package can use it.
package rapidtier

import (
	"flag"
	"os"
	"strconv"
)

// Cap lowers rapid's check count to n for the calling test binary, unless the
// operator already chose a count with RAPID_CHECKS. Call it from a package-level
// `var _ = rapidtier.Cap(n)` in a test file: that runs after rapid's flags are
// registered and before the testing package parses the command line, so
// `-rapid.checks` still overrides it.
func Cap(n int) bool {
	if _, set := os.LookupEnv("RAPID_CHECKS"); set {
		return false
	}
	return flag.Set("rapid.checks", strconv.Itoa(n)) == nil
}
