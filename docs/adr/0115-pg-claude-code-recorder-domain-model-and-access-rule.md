# pg-claude-code-recorder: domain model, control mod, store split, access rule and `agentsession` retirement

**Status**: Accepted (operator "continue", 2026-10-30, after two six-dimension reviews; amends `phillipgreenii-nix-agent-support` ADR 0062's connector architecture; origin `pg2-6z56f`)
**Date**: 2026-10-30
**Deciders**: phillipg

## Context

Several tools read Claude Code's files each in their own way, and the bare word "session" meant a
conversation, a transcript and a running process at different times. The design
`docs/superpowers/specs/2026-10-30-pg-claude-code-recorder-design.md` replaces the duplicated
readers with one daemon, `pg-claude-code-recorder`, and replaces terminal-host adapters with one
control mod. Five of its decisions bind later work and pass the ADR tests (hard to reverse, surprising
without context, a real alternative existed), so they are recorded here. The design holds the rest.

## Decision

1. **Vocabulary and domain model.** Five terms are the shared vocabulary: **Conversation** (what
   Claude Code calls a session; identity is its `session_id`; what `claude --resume` continues),
   **Transcript** (the files that record one Conversation), **Process** (one running `claude`: a pid
   plus its start time), **Host** (where a Process takes input; a fact only, nothing acts on it) and
   **Account usage** (the 5-hour and weekly windows). The bare word "session" leaves the vocabulary.
   A Conversation is `open` (a live Process holds it) or `closed`; `resumable` is a derived property,
   not a state; "active" is retired. The recorder stores end evidence and a client derives one of five
   labels (exited, cleared, closed normally, crashed, unknown). A crash query lists Conversations
   whose end evidence reads as a crash; it replaces a resume-list command, which is not built.
2. **A control mod replaces host adapters.** Control of a running Claude goes through a mod that
   listens on a per-process inbox and acts inside its own Claude (submit, interrupt, run an
   allowlisted slash command, stop), plus a small command that sends those requests. No terminal-host
   adapter (tmux, cmux) and no `pg-host` is built. The mod also writes the end reason of a
   Conversation, which the recorder ingests.
3. **The store splits observations from derived data.** Facts that cannot be re-derived from copies
   (file ids, generations, path history, deletions, seen-times, Process and Conversation facts) live
   in the database and in an fsynced append-only observations journal. Records and search indexes are
   derived: deleting them and re-parsing every copy reproduces them. A lost database is rebuilt by
   replaying the journal and re-parsing, and the rebuild bumps the feed epoch so clients resync.
4. **The access rule.** Any tool MAY read the recorder directly over its socket or Go client package
   (pa-monitor and ccpool do). The only required indirection is that pg-desk reaches it through
   pg-connector. A client of a recorder that is down reports "daemon offline" and does not fall back to
   Claude's files.
5. **`agentsession` is retired, not split.** pg-connector's `agentsession` capability and its backend
   are removed, in three beads with real `blocked-by` edges: a discovery bead, a removal bead here
   (behavior docs first) and a deployment-repo bead. The earlier `host` and `control` capabilities
   are not built. A `conversation` or `process` capability is added only if discovery finds a caller
   of the backend's `search`, `activity` or `attention` capabilities that needs it. This amends
   `phillipgreenii-nix-agent-support` ADR 0062's capability list and the pg-connector behavior docs.

## Consequences

- Clients (pa-monitor, ccpool, ccaudit, beads-exporter) move onto the recorder one at a time
  (Strangler Fig); pa-monitor and ccpool end up as policy only.
- The recorder records facts and clients hold interpretations (ADR 0015 of this repo applied to
  session files).
- Code inside Claude (hooks, the approver) keeps reading its own Conversation's files directly.
- The retirement edits the pg-connector behavior docs before any code and needs a change in the
  deployment repository, which lands before or with the removal.
- Removal of files from the Claude directory is off by default until pa-monitor `sessions` and ccpool
  resume are converted; settling is always on.
