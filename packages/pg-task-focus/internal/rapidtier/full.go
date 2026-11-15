//go:build integration

// Package rapidtier: in the integration tier the property tests keep rapid's
// default count; see unit.go.
package rapidtier

// Cap is a no-op in the integration tier.
func Cap(n int) bool { return false }
