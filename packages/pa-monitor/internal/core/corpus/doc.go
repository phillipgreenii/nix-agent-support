// Package corpus owns the reactive read of the Claude Code corpus. It joins the
// session registry (session.Discoverer over the recorder's Process facts) with
// the transcripts, subagent files and status files the recorder holds: the
// recorder's change feed names which files changed, and the raw bytes of the new
// lines come from the recorder, so no code here opens a file of the Claude
// directory. Each file keeps an offset cursor (the offset of the last line
// folded); a reset entry of the feed drops the file's folded lines before
// anything else is applied to it. The lines of a file are folded once per Scan
// into criteria-gated observers that maintain the projections.
//
// Import direction: corpus MAY import session, transcript, and the
// claude-transcript dependency; none of those may import corpus (no cycle).
package corpus
