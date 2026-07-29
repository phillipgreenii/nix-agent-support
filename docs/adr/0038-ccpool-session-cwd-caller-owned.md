# ccpool's session working directory is caller-owned, decided once at creation

**Status**: Accepted
**Date**: 2026-07-29
**Deciders**: Phillip Green II

## Context

Every ccpool session runs in some working directory: the value handed to
`Tmux.NewSession` (`packages/ccpool/internal/session/session.go:418`), which becomes the launched
`claude`'s cwd. **Who owns that choice had never been ratified** — not by an ADR, not by an
invariant, not by any shipped doc. This ADR closes that gap. It records a decision about an
**existing** behavior; it mandates no code change.

### What ships today, read off the code

Creation resolves the directory through a three-step chain
(`packages/ccpool/cmd/ccpool/new.go:53-60`):

```go
dir := *cwd
if dir == "" {
	if cfg.Claude.DefaultCwd != "" {
		dir = cfg.Claude.DefaultCwd
	} else {
		dir, _ = os.Getwd()
	}
}
```

`claude.default_cwd` is a deliberate, documented config field
(`packages/ccpool/internal/config/config.go:51`), not an accident. `ccpool reply` has the same
shape minus the flag — it exposes **no** `--cwd` (`packages/ccpool/cmd/ccpool/reply.go:59-61`).

One fact about the resolved value is **load-bearing for the decision below**, and was verified
against the code rather than assumed: **the directory is persisted, and resume replays the
persisted value rather than re-resolving it.**

- It is a stored column — `cwd` in the session row (`internal/store/ops.go:12`,
  `internal/store/store.go:41`) — stamped at insert on the brand-new path
  (`session.go`, step 5: `CWD: cwd`).
- The two **resume** launches pass **`row.CWD`** (`session.go:303` and `:330`). Only the
  **brand-new** launch (`session.go:349`) passes the freshly-resolved `cwd`.

So the operative model today is not "re-resolved on every command". It is: **the chain runs once,
at creation; the answer is persisted; every later resume reuses it.** A session therefore never
wanders between directories, whatever cwd a later `ccpool reply` happens to be invoked from.

The directory is also a **trust-boundary input**, not merely a convenience. Before launch, ccpool
canonicalises it (`filepath.EvalSymlinks`, `session.go:271`) so the trust key matches what Claude
records, then **pre-trusts** it in `~/.claude.json` (`EnsureTrusted`, `session.go:274`) and
pre-records MCP consent for that directory's `.mcp.json`
(`mcpconsent.PreDisableUnclassified`, `session.go:279`). Choosing the working directory is
therefore choosing what the launched agent is trusted over.

### The claim this ADR is answering

The behavior-docs restructure round-2 plan — an **ephemeral working file outside this repository**,
slated for deletion by its own epic's closing gate — carried a decision row **D28**:

> ccpool's session working directory should be the pool root, not the user home […] The first half
> is **intended behavior that differs from current code** — a realization gap, not a description.

The plan **already** labels it intent rather than description, and its findings section adds that
"the accurate description of today is 'the working directory is caller-supplied per session'".
D28's pool-root half was sourced from a conversation and **never ratified anywhere**. Bead
`pg2-x1026` was filed so that D28 is not published as a description of code that does something
else, and so that the question "who owns this?" gets an answer with a home more durable than a
throwaway plan.

A second fact decides the pool-root half. **The pool root is a state directory, not a workspace.**
`Config.PoolRoot` is the "canonical pool dir" (`internal/config/config.go:27`) that holds the store
DB, the hook log, and the lock files — `internal/config/config_test.go` asserts exactly that
(`StateDir` "want pool root (hook.log lives here)", `RuntimeDir` "want pool root (`*.lock` files
live here)"), and ADR 0014 built the pool registry around pools as directories carrying
"state per pool". `sessionmeta`'s `OpenPool`/`DBPathForPool` use "pool root" in this same
DB-path sense. It is a different concept from a session's working directory, and the two were
conflated.

What D28's "**not the user home**" half points at is real, and survives this ADR as an open
question: the chain's **last-resort** `os.Getwd()` step means `ccpool new` invoked from a home
directory with no `default_cwd` configured will pre-trust that home directory.

## Decision

**The CALLER owns a ccpool session's working directory. It is decided exactly once — at session
creation — and is immutable for the life of the session.** Concretely:

1. `ccpool new --cwd <dir>` **MUST** be honoured verbatim as the session's working directory. The
   caller is the owner of record; ccpool **MUST NOT** override or relocate an explicitly supplied
   directory.
2. When no `--cwd` is given, the resolution order **MUST** be `--cwd` → `claude.default_cwd` →
   the invoking process's working directory. This chain — exactly as
   `new.go:53-60` ships it — is hereby **ratified as the operative contract**, not merely
   tolerated as an accident.
3. The resolved directory **MUST** be persisted on the session row at creation, and every
   subsequent **resume MUST launch in the persisted value**, never re-resolve the chain. A
   session's working directory **MUST NOT** change because a later command was invoked from
   somewhere else.
4. `claude.default_cwd` **MUST** remain supported as the pool-scoped default. It is **not**
   deprecated by this ADR.
5. ccpool **MUST NOT** substitute the pool root for a session's working directory. The pool root
   is the pool's **state** directory (ADR 0014) and **MUST NOT** double as an agent workspace.
6. Documentation **MUST** describe the working directory as caller-owned, persisted, and
   immutable per session. No doc **MAY** assert "a session runs from the pool root" as current
   behaviour. Where the pool-root idea is recorded at all, it **MUST** be marked as an
   unadopted alternative (this ADR's Alternatives Considered) or as intent with an explicit
   realization gap — never as a description.

Rationale:

- **The consumer requires per-session directories.** pr-pool dispatches a worker per work item into
  that item's own git worktree; ccpool's own design intent is one session per external id, each in
  its own tree. A single pool-wide working directory would put every concurrently-dispatched agent
  in the **same** directory, collapsing precisely the worktree isolation the surrounding system is
  built on. Caller ownership is not a stylistic preference here; it is what makes the dispatch model
  expressible at all.
- **Pool root is state, not workspace.** Pointing sessions at it would have agents writing working
  files into the directory holding the store DB, the hook log, and the lock files. That is a
  regression in isolation, not an improvement — the opposite of what D28 was reaching for.
- **The persisted, immutable model already delivers the stability D28 wanted.** The worry behind
  "should be the pool root" is a session whose directory is unstable or surprising. The code
  already forecloses that: resume replays `row.CWD` (`session.go:303`, `:330`). The property is
  real today; what was missing was a doc saying so. Naming it (clause 3) is cheaper and safer than
  changing where sessions run.
- **`default_cwd` carries machinery that would be lost.** The home-manager module pre-trusts
  `default_cwd` at **activation** time (`home/programs/ccpool/default.nix`), which its own comment
  calls "the primary, non-racy trust path (vs. the runtime `ensure` fallback)". Deprecating the
  field would delete the only non-racy pre-trust path and would leave `ccpool reply` — which has no
  `--cwd` — with nothing but `os.Getwd()`.
- **Ratifying the status quo is the supersedable choice.** This ADR grants no new capability and
  forecloses nothing: moving to pool-root-by-default later remains available, and would then be a
  deliberate, reviewable behavior change superseding this record. Shipping a behavior change on the
  authority of an unratified row in a deleted plan file would not have been.

## Consequences

### Positive

- The question has a durable owner. "Who decides a session's cwd?" is answerable from
  `docs/adr/` instead of from an out-of-repo plan file that is scheduled for deletion.
- The persisted/immutable property (clause 3) is now a **stated contract** rather than an
  undocumented emergent property, so a future refactor that re-resolved the chain on resume would
  be a visible violation instead of a plausible-looking cleanup.
- D28 is disposed of correctly: recorded as considered-and-not-adopted with the reasoning, so
  deleting the plan file loses nothing.
- Zero behavior change, zero config surface change, zero store change. Nothing to roll out, and no
  window in which docs and code disagree.

### Negative

- **The accepted rough edge: the last-resort fallback grants trust over whatever directory the
  caller happened to be in.** `ccpool new <id>` with no `--cwd` and no `default_cwd`, invoked from
  a home directory, resolves to that home directory and then pre-trusts it
  (`EnsureTrusted`, `session.go:274`) — a broad folder-trust grant nobody explicitly asked for.
  This ADR **ratifies the chain including that step** rather than fixing it, and records it as
  the open question below. Mitigations available today are the two earlier steps of the same
  chain: pass `--cwd`, or set `default_cwd`.
- **A resume pre-trusts the resolved directory, not the one it will run in.** On resume the launch
  uses `row.CWD`, but `EnsureTrusted`/`PreDisableUnclassified` at `session.go:274`/`:279` still
  operate on the freshly-resolved `cwd`. So `ccpool reply` invoked from an unrelated directory
  writes a folder-trust entry for a directory the session will **not** run in. It is a spurious
  write, not a correctness break — the session's real directory was trusted at creation — but it
  is an inconsistency with clause 3's spirit. Folded into the open question below.
- Anyone who read D28 as settled policy will find the opposite ratified here. That is the point of
  the record, but it does mean the pool-root idea must not be re-derived from the plan file.

### Neutral

- `ccpool reply`'s lack of a `--cwd` flag stays as-is. Under clause 3 it is very nearly moot for
  an existing session — the resolved value does not determine where a resumed session runs. It
  still matters on the one path where `reply` creates a session from scratch.
- The canonicalisation step (`EvalSymlinks`, `session.go:271`) is untouched and orthogonal: it
  normalises whichever directory the chain produced so the trust key matches what Claude records.
- Nothing here touches `sessionmeta`'s "pool root" (`OpenPool`/`DBPathForPool`), which is the
  DB-path sense of the term and never was about a session's working directory.

### Open question

**Should the last-resort `os.Getwd()` step refuse instead of implicitly trusting an arbitrary
directory** — and should resume pre-trust `row.CWD` rather than the freshly-resolved value? Both
halves are the residue of D28's "not the user home" concern, and both are **deliberately left
open** here: either would be a behavior change (a new failure mode for `ccpool new` with no
configuration, and a change to which directories get trust entries), which is a decision for a
human and a separate ADR, not a side effect of documenting ownership. Revisit if an unintended
home-directory trust grant is actually observed. Whether ccpool should move to pool-root-relative
defaults at that time is subsumed by this same question; clause 5 forbids only the pool **state**
directory as a workspace.

## Alternatives Considered

### The session working directory is the pool root (D28 as written)

Launch every session in the pool's root directory. **Rejected.** The pool root is the pool's state
directory — store DB, hook log, lock files (ADR 0014, and `internal/config/config_test.go` asserts
it) — so this would have agents working inside pool state. It also gives every concurrently
dispatched session the **same** directory, which breaks the per-worktree isolation pr-pool's
dispatch depends on. And the stability it was reaching for already exists by another mechanism
(clause 3). Adopting it would have been a behavior change authorised only by an unratified row in
an ephemeral, out-of-repo plan file.

### Deprecate `default_cwd`; require an explicit `--cwd` always

Make the caller state the directory every time and delete the config fallback. **Rejected.** It
removes the activation-time pre-trust path that `home/programs/ccpool/default.nix` calls the
"primary, non-racy" one, leaving only the racy runtime fallback. It breaks `ccpool reply`, which
exposes no `--cwd` at all, so the strict form would need a new flag on every entry point first.
And it converts a working default into a hard error for existing callers — a real behavior change
in exchange for tidiness. The narrower, genuinely useful version of this idea is only about the
**last** step of the chain, and is the open question above.

### Re-resolve the chain on every command, including resume

Treat the working directory as a per-invocation input rather than session state. **Rejected**, and
worth naming because it is what a reader of `reply.go:59-61` alone might assume already happens. It
would let a session's directory change under it depending on where an operator ran `ccpool reply`
from — the exact instability D28 was reacting to. `row.CWD` is the better design and is already
what resume uses; clause 3 pins it.

### Leave it undocumented

Close the bead as "no misbehaviour found" and move on. **Rejected.** The gap was never a bug; it
was that a **pool-root claim existed in writing with no counter-record**, in a file about to be
deleted. Without a decision, the next reader of that plan re-derives D28 as policy, and ccpool's
behavior-docs set gets authored around a description of code that does something else — which is
the specific outcome `pg2-x1026` was filed to prevent.

## Related Decisions

- [ADR 0014](0014-ccpool-reap-all-pool-registry.md) — the pool registry over pools-as-directories,
  and the pool dir as the carrier of per-pool **state**. That is the sense of "pool root" clause 5
  refuses to overload; this ADR does not touch which pools exist or get reaped.
- [ADR 0015](0015-ccpool-session-facts-not-work-judgments.md) — ccpool tracks session **facts**. The
  persisted `cwd` is such a fact: where the session was launched, recorded once, not a judgment
  re-derived per command.
- [ADR 0037](0037-ccpool-reap-spares-human-paused-sessions.md) — the immediately preceding ccpool
  decision; its Context sets out the same evidence-over-assumption standard applied here (verify
  the load-bearing fact against the code, then decide).
- Bead `pg2-x1026` — the decision-and-documentation gap this ADR closes, found by the behavior-docs
  restructure evidence passes. ccpool's own behavior-docs set does not exist yet; when it is
  authored, clause 6 is the rule its working-directory statement must satisfy.
