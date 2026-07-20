// Package provider is pa-monitor's nested cache of point-in-time PULL lookups:
// git branch, subshell count, terminal host, process env, repo label, and PR.
//
// Two eviction subtrees hang off one Cache:
//
//   - the PID/session subtree (bySession, keyed by SESSION-ID): env, terminal
//     host, and subshell count. Session-id keying — not PID keying — is what
//     makes these reuse-safe: a recycled PID is a new session-id, so a dead
//     session's cached value can never leak into a new session that inherits its
//     PID. A dead-but-not-GC'd session keeps its own frozen node (tombstone).
//   - the workspace subtree (byCwd, keyed by CWD): git branch and repo label,
//     refcount-evicted when the last session in a cwd disappears.
//
// Freshness policies: env + terminal-host = WhilePIDAlive; subshell =
// (path,mtime) transcript-change invalidation; git branch =
// UntilFileChanges(.git/HEAD); repo label = LongLived; PR = the file-backed
// session.PRCache (bounded elsewhere with a found-entry TTL + prune).
//
// Each provider's fetch/exec boundary is injectable so tests never spawn a
// subprocess, and the four metered lookups (git_branch, subshell, terminal_host,
// pr_lookup) record RecordSubprocess ONLY on a real fetch (cache miss), via the
// nil-safe Recorder seam defined here — so the counts drop as caching takes hold.
// The Recorder is defined locally (not imported from internal/otel or
// internal/core/poller) to keep provider free of those dependencies; provider
// imports only session/signal/bridge/subshell + stdlib and MUST NOT import
// poller/daemon/corpus/labels/otel.
//
// Phase 2: the Cache is accessed ONLY from the (synchronous) tick goroutine
// (confinement). The single mutex + "never hold the lock across a ps/gh backend"
// discipline is in place so Phase 3 can move the Cache behind the producer
// goroutine without re-keying or re-locking.
package provider
