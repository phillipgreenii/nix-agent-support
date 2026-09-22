# CETA: approve the `kill -0`/`kill -s 0` null-signal liveness probe

**Status**: Accepted (resolves `pg2-z3u4f` item 1)
**Date**: 2026-09-22
**Deciders**: Phillip Green II (via drain-beads dispatch)

## Context

`pg2-23z9w` enumerated the commands our own plugins prescribe that CETA cannot decide. One class —
81 misses, the largest still open after that bead's other six were resolved — is not literally
prescribed by any plugin text at all: it is `kill -0 <pid>`, composed at runtime by agents polling
whether a backgrounded process is still alive, most often as the guard of a Monitor until-loop
(`kill -0 <pid> && echo RUNNING && echo DONE`, or `until ! kill -0 "$PID" 2>/dev/null; do sleep 2;
done`). With no rule claiming it, every invocation falls through to chain exhaustion — an abstain
that then either hits the auto-mode LLM classifier or, in a `dontAsk` dispatched session, is denied
outright.

`kill` is not on `internal/rules/dangerouscmds`' blanket denylist. That package's own doc comment
records this as deliberate ("`kill`/`killall`/`pkill`/... are intentionally omitted from this
bead's scope") — a general `kill` policy (what to do about `kill -9`, bare `kill`, `killall`,
`pkill`) was never decided and is explicitly **not** this ADR's subject. This ADR decides only the
narrow null-signal probe shape.

Two questions the bead itself posed, both resolved below:

1. **Is `-0`/`-s 0` the only safe form to allowlist?** Answered by identifying every spelling
   `kill(1)` (BSD/macOS and GNU/util-linux) actually supports for selecting signal 0, and by the
   safety argument in the Decision section, which does not depend on which spelling was used at
   all — it depends on the signal being null.
2. **How does this interact with a compound Monitor until-loop?** Answered by how CETA already
   folds a Bash compound: the engine (`internal/engine/engine.go`'s `evaluateParsed`) splits an
   expression into leaves and evaluates each leaf independently through the full first-match-wins
   chain before folding most-restrictive-wins. A rule never sees the whole compound — only its own
   leaf — so `kill -0 <pid> && echo RUNNING && echo DONE` needs no special compound-aware logic:
   the `kill -0 <pid>` leaf is judged on its own by the new rule below, `echo RUNNING`/`echo DONE`
   are already `alwaysSafe` in `internal/rules/safecmds`, and the fold naturally reaches Approve.

## Decision

1. **A new rule module, `internal/rules/killprobe`, is added** (extension point 1 — this is
   genuinely a parse/classify decision, not expressible as `rules.json` data; a flat
   `approvedCommands` entry for `kill` was rejected below). It Approves a `kill` leaf **iff** the
   leaf requests POSIX signal 0 (the "null signal") and nothing else disqualifying; every other
   `kill` invocation is left unmatched (`NotApplicable`), unchanged from before this rule existed.

2. **The safety argument does not depend on the target.** `kill(2)` defines signal 0 as performing
   only the existence/permission check — it sends nothing. Unlike every other signal, the pid
   operand(s) are therefore irrelevant to safety: even `kill -0 -1` (a process-group/broadcast
   target) has no actual effect when the signal is null. Only the **signal selection** needs
   scrutiny, which is exactly what `killprobe.isSignalZeroProbe` checks — it accepts any pid
   operand unconditionally and disqualifies on any signal other than 0.

3. **Recognized signal-0 spellings**: the glued numeric form `-0`, and the separate/`=`-joined
   named-flag forms `-s 0`, `--signal 0`, `--signal=0`. `-s0` (glued short-flag-plus-value) is
   deliberately **not** recognized — neither BSD/macOS nor GNU/util-linux `kill(1)` documents a
   glued form for `-s`, so there is no observed idiom to cover, and adding it would be speculative
   surface with no corpus evidence behind it (unlike the four spellings above, which the `-0`
   idiom's own two natural variants — glued-numeric and the POSIX `-s <name>` form, plus GNU's long
   option in both its spellings — already cover).

4. **Any other flag disqualifies the whole invocation immediately**, even one that also contains a
   `-0`: a non-zero numeric signal (`-9`), a named signal (`-TERM`, `-KILL`), signal listing (`-l`,
   `-L`), or anything the parser does not recognize (`-a`, `-q`/`--queue`, `-p`). A command naming
   two different signals is not the single-purpose liveness-probe idiom this rule exists for, and
   the safe default is to defer, not to guess which one would win at runtime. This mirrors the
   under-matching bias already documented elsewhere in this package (e.g.
   `internal/rules/pnworkspace`'s `approvedSubcommands` doc): a missed match costs a prompt/abstain
   — the status quo before this rule existed — never a wrong Approve.

5. **Known, accepted limitation**: a negative pid (`-1`, or any negative process-group id) is
   lexically indistinguishable from a glued numeric signal flag (both are `-` followed by digits),
   so `kill -0 -1` is misread as naming a second, disqualifying signal and defers instead of
   approving. This is the safe direction (a miss, not a wrong Approve) and is not worth a
   getopt-style flags-before-operands split for an idiom that in practice always targets one
   positive pid — `killprobe_test.go` pins the case as a documented limitation, not a bug to fix
   silently later.

6. **Placement in `internal/setup.RuleChain`**: alongside `pnworkspace`, just before `safecmds`.
   Ordering relative to its neighbours does not matter for correctness — no other rule in the chain
   recognizes `kill` at all (per `dangerouscmds`' own scope note) — but it groups with
   `pnworkspace` as the other small, fixed-allowlist, config-free Bash-command classifier.

## Rejected alternatives

- **A flat `approvedCommands`/`rules.json` entry for `kill`.** Rejected outright: per ADR 0040,
  `approvedCommands` is absolute for its leaf — it approves the command with **any** arguments.
  That would auto-approve `kill -9 <anything>`, which is a real, unreviewed way to terminate an
  arbitrary process. The whole point of this decision is that only the null-signal form is safe;
  a flat allowlist entry cannot express that distinction at all.
- **Widening `dangerouscmds`' denylist to include `kill` with `operandGated` carve-out for signal
  0** (the same shape as that package's existing `mount`/`dd` predicates). Considered, but
  `dangerouscmds`' own scope note explicitly leaves `kill` undecided for a reason: doing so would
  make every OTHER `kill` invocation (bare `kill`, `kill -9`, ...) a hard, non-overridable Reject
  instead of today's abstain (which still reaches a human prompt or the LLM classifier in `auto`
  mode). That is a real policy change to `kill`'s general treatment, well beyond this bead's scope
  (`pg2-z3u4f` item 1 is specifically the liveness-probe idiom) and not something to fold into a
  narrow-allowlist ADR. Left for a future bead if `kill`'s general policy is ever revisited.
- **Recognizing `-s0` (glued) or bash-only signal-name aliases for 0.** No signal name for 0 exists
  (it is the null signal, unnamed on any platform this repo targets), and no glued-`-s` form is
  documented — both would be speculative surface with no observed idiom behind them.

## Consequences

- The 81 measured `kill -0 <pid> && ...` misses should no longer abstain or deny once this rule
  ships and is applied.
- `killall`, `pkill`, and every other `kill` invocation not requesting the null signal are
  completely unaffected — still unclaimed by any rule, exactly as before.
- A future need to recognize `-s0` or a broader `kill` policy is a new, separate decision; this ADR
  covers only the null-signal probe shape enumerated above.
