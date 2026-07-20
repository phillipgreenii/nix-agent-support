// Package corpus owns the reactive read of the Claude Code corpus: it joins the
// two on-disk trees (~/.claude/sessions/*.json, PID-keyed, via session.Discoverer
// and ~/.claude/projects/<slug>/*.jsonl, cwd-keyed), resolves each session's
// transcript once (via a write-once title cache, not a per-tick re-probe), and
// tails each relevant file at most once per Scan — feeding criteria-gated
// observers that maintain projections.
//
// Phase 1a introduces the Monitor with two observers (SessionSnapshot,
// SubagentError) driven synchronously from Poller.Snapshot (zero new
// concurrency). UsagePricing + Limits observers, the producer goroutine, and the
// generic per-line Event firehose arrive in later phases.
//
// Import direction: corpus MAY import session, transcript, and the
// claude-transcript dependency; none of those may import corpus (no cycle).
package corpus
