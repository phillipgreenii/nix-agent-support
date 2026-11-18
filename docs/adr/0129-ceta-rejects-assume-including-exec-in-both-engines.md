# CETA rejects `assume`, `--exec` included, in both engines

**Status**: Accepted (operator ruled 2026-11-17; supersedes the earlier decision to delegate `assume --exec` to the rule chain; records an operator-ruled exception to ADR 0075 R1)
**Date**: 2026-11-17
**Deciders**: Phillip Green II

## Context

CETA's production-access gate is `assume`: an agent reaches production systems only from an
environment the operator assumed. Two holes let an agent perform the assume itself.

1. **Rule chain.** `internal/rules/assume` rejected a bare `assume`, but `assume <role> --exec
"<cmd>"`, directly or through `source assume` / `. assume`, was structurally delegated: the call took
   the inner command's verdict and was approved whenever the inner command was approvable. An earlier
   decision introduced that delegation so a read-only inner command would not abstain.
2. **Effect engine.** There was no `assume` schema, so `assume` abstained ("no schema for assume")
   and the auto-mode classifier decided. `source assume ...` and `. assume ...` abstained for a second
   reason: the `source` and `.` builtins had no schema either ("no schema for source").

## Operator rulings (Phillip, 2026-11-17, verbatim)

Verbatim except one bracketed edit: the name of a consumer-specific production database tool is
replaced with a description, because this repository is public.

- "yes reject assume --exec like assume."
- "CETA will not ask for [a production database tool]. in general, as long as assume isn't allowed to be called by the
  agent, then it would only be able to access prod systems if it was run in an assumed environment
  which is only possible if i have explicitly done so. so, the block/ask is not on using prod tools or
  accessing prod systems, but on the initial auth/envar setup to allow the access."
- On the work: "update existing logic to be assume supported."

## Decision

1. **`assume` is rejected in every spelling, in both engines.** A bare `assume`, `assume <role>`,
   `assume ... --exec <cmd>`, `assume ... --exec=<cmd>`, and the same through `source assume` and
   `. assume` all reject. `--exec` still performs the assume, so it gets no special treatment. The
   earlier `--exec` delegation is **superseded**: the rule no longer evaluates the payload, and the
   evaluator wiring it needed in `internal/setup/factory.go` is removed.
2. **Rule chain.** `internal/rules/assume` rejects any leaf whose executable is `assume`, or whose
   executable is `source` or `.` with `assume` as the script, with one message
   ("AWS assume-role commands must be run outside of Claude sessions...").
3. **Effect engine, by effect, not by name.** ADR 0075 R8 and P1 require commands to be described by
   specs and decided by general policies, so there is no name-only reject. The smallest mechanism that
   fits:
   - A built-in `assume` command schema states one implicit effect, a remote operation
     `assume-role` on the resource `aws`. Unknown flags are inert, because no flag can remove the
     effect.
   - `effectpolicy.RemoteMutation` gains one case in its existing, open operation vocabulary:
     `assume-role` is Forbidden, the same way `force-push` and `pr-create` are. No new policy and no
     new effect kind.
   - A built-in `source` schema (also registered as `.`) has one subcommand, `assume`, that reuses
     the `assume` schema. The script name is the subcommand key, so `source assume ...` and
     `. assume ...` carry the same effect. Sourcing any other script stays an unmodeled subcommand
     and abstains exactly as before.
   - A call by absolute path (`/usr/local/bin/assume`) is not trusted by ADR 0075 P4 and keeps
     abstaining as an untrusted executable identity; the rule chain still rejects it by basename.
   - `.` is written to `dot-source.json` so it is not a hidden file.
4. **Exception to ADR 0075 R1.** R1 says the old engine receives no fixes. The edit to
   `internal/rules/assume` and its wiring is an operator-ruled exception, made because the old
   engine is still what the live hook runs until it is removed. The precedent is ADR 0104, which
   changed the old engine under an operator ruling for the same reason.
5. **Credential setup is pinned, not changed.** Agents set production credentials in the environment
   themselves today (a tracker sample of Bash calls from 2026-10-29 to 2026-11-17 set `AWS_PROFILE`
   to a production profile in 159 calls, which the classifier approved 146 times and the live hook
   7 times). The effect engine permits any unclassified `AWS_*` assignment. Tests in both engines
   pin the current verdicts of `AWS_PROFILE=x cmd`, `export AWS_PROFILE=x`,
   `export AWS_SESSION_TOKEN=x` and `env -u AWS_SESSION_TOKEN AWS_PROFILE=x cmd`: all four approve in
   both engines. Which credential setups to reject is a separate operator decision, not made here.

## Consequences

- An agent can no longer run `assume` to obtain production access, directly, with `--exec`, or by
  sourcing it, in either engine. A real `assume --exec` use by an agent was not observed in the
  tracker sample (39 Bash calls mentioning assume, none a real `--exec` call), so no current workflow
  is lost.
- The block is on creating the assumed environment, not on using production tools inside one.
- Whoever changes the four pinned AWS credential forms changes a test on purpose and must cite the
  operator decision.
