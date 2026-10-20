# CETA input processors run only on Approve/Ask, never on Abstain (NoOpinion)

**Status**: Accepted (resolves `tc-7m85u` item 3)
**Date**: 2026-09-16
**Deciders**: Phillip Green II

## Context

`internal/inputproc` lets a consumer configure one or more commands that rewrite a Bash tool
call's command text before it runs (`CETA_INPUT_PROCESSORS`, an ordered list as of this same
bead — see `cmd/claude-extended-tool-approver/main.go`'s `handlePreToolUse`). The chain is only
invoked when ceta's own rule-chain verdict for the leaf is `Approve` or `Ask`:

```go
if (result.Decision == hookio.Approve || result.Decision == hookio.Ask) &&
    inputproc.Configured() && input.ToolName == "Bash" {
```

`NoOpinion` (serialized `abstain`, emitted as `{}`) is deliberately excluded. This was tempting to
widen: an abstained command is exactly the case where ceta has nothing more to say and simply
hands the call to Claude Code, so it looks like the "safe" case to also let a processor rewrite —
no ceta verdict is at stake either way.

It is not safe, because of a mechanism outside ceta's control. Claude Code's own
`settings`/`--allowedTools` allowlist match — the thing that lets a command run with **no
prompt at all** — is evaluated against the tool call **after** any `updatedInput` a PreToolUse
hook returns is applied. A command that would have matched the allowlist as originally typed can
therefore fail to match once rewritten, turning a silent auto-run into an interactive prompt.

Measured 2026-09-16 (run set `hooktest-D`, round 2, `tc-7m85u`'s own description): an input
processor that prefixes an otherwise-allowlisted command with an `export X=1;` / `X=1 <cmd>` form
turned the run into a denial/prompt in R2 and R4, and was allowed in R3 only once
`Bash(export:*)` was added to the allowlist alongside the original pattern. That is: the identical
rewrite reached Claude Code's allowlist match in more than one run, and was denied wherever the
allowlist covered only the ORIGINAL command shape, not the rewritten one — confirming that the
allowlist match happens against the REWRITTEN command, not the command Claude Code was originally
asked to run. The failure mode does not depend on which ceta verdict authorized the rewrite — the
point at which Claude Code checks the allowlist has no visibility into ceta's Decision at all,
only into the final command text.

## Decision

**Input processors run only when ceta's own verdict is `Approve` or `Ask`. The condition MUST
NOT be extended to include `NoOpinion`/abstain without a fresh measurement against the Claude
Code version in use at the time.**

On `Approve`/`Ask`, ceta has already decided the leaf is not going through the allowlist-silent
path — `Approve` suppresses the prompt itself, and `Ask` always prompts — so a rewrite cannot
demote a silent allow into a prompt: there was no silent allow to begin with. `NoOpinion` is the
one verdict where the allowlist is still live and unconsulted by ceta, so it is the one verdict a
rewrite can make strictly worse (an unprompted run becomes a prompted one) with zero upside (ceta
was never going to approve or ask about it anyway).

## Consequences

- A processor configured for the abstain path (a hypothetical identity-stamping processor that
  wants to tag every command, gated or not) cannot be reached today. Its remedy is the one
  `docs/adr/0040-ceta-approved-commands-are-absolute.md` already establishes for a different
  problem: add the specific command to `approvedCommands` so ceta's OWN verdict becomes `Approve`
  for it, which both keeps the non-overridable security band's semantics intact for the rest of
  the corpus and puts the leaf through the input-processor chain.
- The `inputProcessors` HM option's description documents this limitation directly
  (`home/programs/claude-extended-tool-approver/default.nix`), and `main.go`'s `handlePreToolUse`
  carries the same note with this ADR's number, so a future change to the gating condition is not
  made without re-deriving why it is not gated on `NoOpinion` today.
- If Claude Code's own allowlist-match-against-rewritten-input behavior ever changes (matching the
  ORIGINAL command instead of the updated one, say), this ADR's Decision should be re-measured
  rather than assumed to still hold — the table above is a snapshot of one Claude Code version,
  not a permanent property of the protocol.

## Amendment 2026-10-20 (pg2-jwilh): drop a rewrite the worktree-isolation guard would refuse

Claude Code's own worktree-isolation guard (not a workspace tool) refuses, in a session isolated
with `EnterWorktree` (or an `isolation: worktree` subagent), any command whose program is not a
known launcher (`env`, `nice`, `nohup`, `sudo`, `timeout`, ...) and that has a git-named word among
its operands. It judges the REWRITTEN command ceta hands back as `updatedInput`, so
`git commit -m x` rewritten to `rtk git commit -m x` was refused (as was every `rtk git ...`,
and `rtk rg -n git src`). Whether rtk rewrites a `git` command at all depends on Claude Code's
permission allow rules (rtk exits 0 only when they cover the command), so it cannot be configured
away in rtk.

**Decision.** After the input-processor chain returns a changed command, ceta MUST drop the rewrite
(emit the decision with no `updatedInput`, so the original command runs) when BOTH hold:

1. the hook payload `cwd` is inside a linked git worktree (walking up from `cwd`, the first
   directory holding a `.git` entry has a `.git` that is a regular file starting with `gitdir:`;
   filesystem-only), AND
2. the rewritten command is guard-hostile: some simple command in it has an executable that is not
   git-named and a git-named word among its arguments (Claude Code's normalisation: strip a leading
   `-opt=`, take the part after the last `/`, strip a `:...`/`@...` suffix, lowercase, match
   `^git(\.exe|\.real|-[a-z][\w-]*)?$`), or the rewrite cannot be parsed.

The predicate is deliberately a superset of the guard (it ignores the guard's last-operand
exception and known-launcher exemption) because dropping a rewrite is always safe: the original
command runs, only uncompacted. Each drop writes one stderr line. The payload carries no isolation
field, so `cwd` is the only signal; a non-isolated session in a linked worktree merely loses rtk
compaction for guard-hostile commands. Rewrites of other commands, and every rewrite outside a
linked worktree, are unchanged. Implemented in `internal/inputproc/isolation.go` and applied in
`handlePreToolUse`; guarded by a real-rtk integration test.
