// Package versioncmp provides the shared client/daemon build-identifier
// comparison used to warn when a stale daemon process outlives a client
// rebuild. It is stdlib-only and imports no internal packages so both the
// TUI/render layer and cmd/pa-monitor can depend on it.
//
// It is named versioncmp (not version) because package main in cmd/pa-monitor
// already declares a `version` global; a `version` import there would collide.
package versioncmp

// Mismatch reports whether client and daemon are known-different build
// identifiers. Both must be non-empty; equal strings are never a mismatch.
//
// Deliberate tradeoff: "dev" is treated as a normal value. A dev client vs an
// installed daemon therefore WARNS on purpose (the common rebuild-without-
// restart case). Two different "dev" builds compare equal and yield a
// false-negative — accepted, because dev-vs-dev is not the scenario this guard
// targets and suppressing dev-time noise is worth more than catching it.
func Mismatch(client, daemon string) bool {
	return client != "" && daemon != "" && client != daemon
}
