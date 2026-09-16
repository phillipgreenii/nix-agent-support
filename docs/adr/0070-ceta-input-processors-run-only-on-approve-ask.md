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
