// Package integration holds pg-rescue's end-to-end tests (design section
// 14.4, bead pg2-04jgw): the real pg-rescue wrapper, the real
// pg-rescue-claude, pg-rescue-bead and pg-rescue-flake-lock-conflict
// handlers, and real git, driven against a bare origin with two clones. Only
// the things that need a network, a model or a tracker are faked: `nix`,
// `claude` and `pg-connector`.
//
// Every test is behind the `integration` build tag so it stays off the
// deploy path; this file exists so the package still builds without the tag.
// Run them with `go test -tags integration ./internal/integration/`, or via
// the flake's `pg-rescue-integration-tests` check.
package integration
