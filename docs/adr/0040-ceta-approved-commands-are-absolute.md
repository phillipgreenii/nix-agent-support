# CETA `approvedCommands` entries are absolute; the unit of concern is the command, not the argument

**Status**: Accepted (resolves `pg2-xkugg`)
**Date**: 2026-07-29
**Deciders**: Phillip Green II

## Context

CETA's `config-rules` rule sits at **slot 1** of the rule chain
(`internal/setup/factory.go`), ahead of the generic security validators `git-directory`,
`dangerous-commands`, `path-traversal`, `secrets`, and `env-vars`. The placement is deliberate
and carries an explicit comment — _"so an explicit consumer decision still wins"_.

Its Approve returns on the **executable basename alone** and never inspects the operands
(`internal/rules/configrules/configrules.go`): on `r.approved[base]` it returns `Approve`, with
the sole operand-adjacent guard being `len(pc.EnvVars) > 0` → `Abstain`. Under first-match-wins,
an allowlisted executable is therefore an unconditional Approve for that leaf **regardless of its
arguments**, and the whole early security band is skipped.

Measured against the ZR fixture, where `grazr` is one of 12 `approvedCommands` entries:

| command                             | result  | what was skipped                                        |
| ----------------------------------- | ------- | ------------------------------------------------------- |
| `grazr /Users/testuser/.ssh/id_rsa` | `allow` | `secrets` never runs                                    |
| `grazr .git/config`                 | `allow` | `git-directory`'s **non-overridable Reject** never runs |
| `grazr ../../x`                     | `allow` | `path-traversal` never runs                             |

The A/B is clean — hold the command shape fixed and change only the executable:
`frobnicate .git/config` → Reject/`git-directory`; `grazr .git/config` → Approve/`config-rules`.

Three backstops survive, because the bypass is **per-leaf**, and each was verified:

- the engine redirection check — `grazr > /etc/hosts` → **Reject**
- sibling leaves — `grazr && sudo rm -rf /` → **Reject**
- the `len(EnvVars) > 0` withhold — `FOO=bar grazr build` → **Abstain**

Scope is bounded to what a consumer put in its own `rules.json`, so this is not a
remote-attacker vector. Three dispositions were considered: keep the behavior as-is and
document it; narrow the precedence so `config-rules` loses to `secrets` and `git-directory`;
or make the Approve argument-aware so an allowlist entry approves the executable while the
early band still screens the operands.

## Decision

**An `approvedCommands` entry MUST remain absolute for its leaf: it approves the command,
arguments included, and the early security band MUST NOT be consulted for that leaf.**
`config-rules` stays at slot 1 and its Approve stays argument-blind.

The unit of trust in CETA is the **command**, not the argument. `approvedCommands` means what it
says — approved. Making every allowlisted command's operands fully understood would require the
parser and every rule to be argument-aware for that command, which is not practical and is not
the trade-off this tool is making.

**The mitigation for a command whose arguments are a concern is to take that command OUT of
`approvedCommands`** — not to weaken the mechanism for all of them. `approvedCommands` is a
per-consumer list under the consumer's own control, so the remedy is always available and is
strictly local to the command in question.

This makes the pinned behavior in `TestIntegration_ConfigRulesPrecedence` (landed `7e98d6a2`,
bead `pg2-v94d7`) **intended** rather than merely recorded. Those rows were committed with
comments stating they capture production behavior and are _not_ an endorsement; that hedge is
now resolved in favor of the behavior.

## Consequences

- **Adding a tool to `approvedCommands` is a security decision with a wider blast radius than
  its name suggests.** It exempts that command from secret-path detection, from path-traversal
  screening, and from `git-directory`'s hard deny — the last of which is otherwise **not**
  user-overridable anywhere else in the chain. This must be documented wherever
  `approvedCommands` is described, because it is genuinely surprising: nothing about the option
  name hints that it disables a non-overridable deny.
- **The bar for entry is "I trust this command with any argument it is handed."** A command that
  takes arbitrary paths, reads files it is pointed at, or forwards its operands to another
  program does not meet that bar and belongs outside the list.
- **Removal is the escalation path.** If a specific allowlisted command turns out to be a
  problem, pull it from `approvedCommands`; it then falls through to the normal chain and its
  arguments are screened again. No code change is needed to apply that mitigation.
- The three surviving backstops above remain load-bearing and must not regress — a redirection,
  a sibling leaf, or a leading env-var assignment each still escapes the allowlist's reach.
- `TestIntegration_ConfigRulesPrecedence`'s comments should be updated from "records production
  behavior, not an endorsement" to cite this ADR as the decision, so a future reader does not
  re-open the question from the hedge.
- This decision covers **argument-blindness only**. It does not bless slot-1 precedence over any
  rule added to the chain later; a new rule that needs to beat a consumer allowlist is its own
  decision.
