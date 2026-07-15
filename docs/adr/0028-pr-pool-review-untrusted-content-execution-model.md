# pr-pool review role: layered least-privilege execution model for untrusted PR content

**Status**: Proposed (BLOCKED on human sign-off — see Decision §5 and the companion plan)
**Date**: 2026-07-15
**Deciders**: Phillip Green II

> This ADR is **Proposed**, not Accepted. It captures the intended security-boundary /
> execution-model decision for review; implementation MUST NOT begin until a human accepts
> it and signs off on the allowlist literals. Companion design + plan:
> `docs/superpowers/plans/2026-07-15-pr-pool-untrusted-content-isolation.md` (bead
> `pg2-jpfw.9`).

## Context

The pr-pool `review` role (`packages/pr-pool/internal/roles/builtin.go:83-101`) runs
`claude` autonomously over **untrusted PR HEAD content** — a teammate's or an external
contributor's branch. The role fetches and checks out the PR head into a worktree and lets
the model read the diff and post a review back via `pg-pr`.

Untrusted content can execute attacker-controlled code as soon as any code-executing tool
verb runs against the tree (`_test.go` via `go test`, `//go:generate`, `prek`/`pre-commit`
hooks, `nix` build scripts, `Makefile`/format hooks). A read-only audit (bead comment
2026-07-09) plus a fresh source verification (2026-07-15, this branch) established the
review role's current posture:

- **Permission mode + budget watchdog: satisfied.** `dontAsk` deny-by-default
  (`config/config.go:99`), `--autonomous` blocks `AskUserQuestion`, finite wall-clock
  budget (`config/config.go:113`).
- **Tool allowlist: pool-wide, not per-role.** `roles.Role` (`roles/roles.go:17-25`) has
  no `AllowedTools` field. The single default list (`config/config.go:109`) grants the
  read-only review role `Edit`/`Write`/`git commit` **and** `go build/test/vet`, `gofmt`,
  `go mod`, `nix flake check`, `prek`, `pre-commit` — i.e. the exact code-execution
  vectors. This is the open RCE path.
- **Credential / env exposure: missing.** The claude launch only ADDS env vars
  (`executor/ccpool.go:55-62`; `session/session.go:392-407`) via `tmux -e`
  (`tmux/client.go:49-64`), which cannot remove inherited variables. The pane inherits the
  tmux server's full ambient env (`SSH_AUTH_SOCK`, `GH_TOKEN`, `AWS_*`, internal tokens).
  The only scrub (`beads/runner.go:35-44`) applies to pr-pool's own `bd` subprocess, never
  to `claude`. No OS sandbox is emitted (`ccpool launch/launch.go:70-103`).
- **Worktree teardown: absent.** `worktree.Ensure` reuses idempotently and never removes;
  `teardownAll` (`orchestrator/orchestrator.go:278-292`) closes only ccpool sessions.

The hard constraint is the platform: **macOS/darwin has no Linux namespaces or seccomp**,
so a container-grade sandbox is not available. Neither existing design doc
(`docs/superpowers/specs/2026-06-12-ccpool-pool-isolation-design.md` — XDG/tmux pool
scoping only; `docs/superpowers/plans/2026-06-23-pr-pool-deny-by-default-allowlist.md` —
the pool-wide allowlist, carrying an unmet "human sign-off required" gate) specifies an
env-scrub or unprivileged-execution mechanism. The decision below is therefore new.

## Decision

Adopt a **layered, defense-in-depth least-privilege execution model** for the review role,
structured as a **Chain of Responsibility** of four independent gates plus post-attempt
teardown. No single gate is trusted to be sufficient. The primary boundary is the tool
allowlist + credential strip (mechanisms fully in our control, not darwin-dependent); the
OS sandbox is defense-in-depth.

1. **Per-role tool allowlist (Strategy) — primary cut.** Add an `AllowedTools` field to the
   role model so tool authorization is per-role, not pool-wide. The review role gets a
   MINIMAL read-only set with **no** code-executing or write verbs
   (`Read,Glob,Grep,Bash(git fetch/checkout/status/diff/log/rev-parse:*),Bash(bd:*),Bash(pg-pr review submit:*)`).
   Worker/feedback roles keep their broader set (they are not the untrusted path).

2. **Default-deny env allowlist (Decorator) — primary cut for exfil.** A launch-time
   wrapper resets the environment (`env -i` semantics) and re-exports only a fixed
   allowlist, stripping `SSH_AUTH_SOCK`/`GH_TOKEN`/`AWS_*`/internal tokens. GitHub access
   the review legitimately needs is provided by a **single narrowly-scoped review
   credential** (fine-grained PAT: `contents:read` + `pull-requests:write` on the reviewed
   repos), injected out-of-band — not the operator's ambient credentials. The wrapper is
   introduced at `ccpool launch/launch.go` (the single source of truth for the claude
   invocation) as an exec-prefix and driven per-role by pr-pool.

3. **OS containment via `sandbox-exec` (Seatbelt) — defense-in-depth.** The same wrapper
   optionally re-execs under a Seatbelt profile confining FS writes to the worktree +
   `TMPDIR` and denying reads of `~/.ssh`/`~/.aws`/`~/.config/gh`/keychain. This is the
   darwin-native mechanism this repo already relies on (nix-darwin's build sandbox).

4. **Per-attempt worktree teardown (RAII).** Untrusted content MUST NOT persist into a
   subsequent dispatch's execution: the executor removes the worktree + scratch branch on
   success (and hard-resets a reused path before checkout).

5. **Recorded human sign-off.** Both security-sensitive allowlist literals (the pool-wide
   default `config.go:109` and the new review-role list) require recorded human sign-off:
   this ADR moves to `Accepted` with the deciding human named, and the `config.go:100`
   comment is replaced with a reference. No implementing branch merges before this.

All controls are **fail-closed**: a missing sandbox binary or missing scoped credential
refuses to launch the review role rather than launching un-isolated.

## Consequences

### Positive

- The prompt-injection → RCE → exfiltration path is cut at two independent layers (tool
  allowlist removes the execution channel; cred strip removes the exfil payload), with FS
  confinement and teardown as further layers.
- Least privilege becomes per-role: review is minimal; worker/feedback keep what they need.
  Backward-compatible (empty per-role `AllowedTools` falls back to the pool default).
- The long-outstanding human sign-off gate is finally structured and recordable.

### Negative

- The env-scrub + sandbox work **spans two packages** (a new `launch.Spec` seam in ccpool
  plus pr-pool wiring) and adds a nix-built launcher script — a coordinated change, not a
  single-package edit.
- The OS layer depends on a **deprecated** Apple mechanism (`sandbox-exec`); it may break
  on a future macOS and is not an unbypassable boundary.
- Managing a scoped review credential and (optionally) a dedicated user is new operational
  surface.

### Neutral

- The strongest containment (a dedicated unprivileged local user + `pf`-by-uid egress) is
  **documented as an upgrade path, deferred** — it needs a privileged, out-of-band setup
  step (agent policy forbids `sudo`), so it is out of scope for the first phases.

## Residual risk (honest darwin verdict)

- **No hard container on darwin.** Layered defense-in-depth, not namespace/seccomp-grade
  isolation. Without the deferred dedicated-user layer the review runs as the operator uid,
  so a sandbox escape regains operator authority (minus the scrubbed env).
- **Network egress cannot be meaningfully restricted for review.** The role needs egress to
  GitHub + `api.anthropic.com`; Seatbelt cannot allow-by-hostname (coarse only). Exfil-over-
  network is mitigated only indirectly (no code-exec tools to initiate it; scrubbed creds
  have no value), NOT by an OS network boundary.
- **Shared beads store + `.git` object store** remain writable by the untrusted session
  (data-integrity vector, not RCE); claude's own model credential necessarily survives the
  scrub. Both are noted, out of scope here.

## Alternatives Considered

### Linux-style container (namespaces + seccomp / bubblewrap)

Rejected: not available on darwin, which is the deployment platform. Revisit only if review
execution moves to a Linux host.

### Dedicated unprivileged local user as the primary mechanism, now

Deferred (not rejected): it is the **strongest** control — the review uid structurally
cannot read the operator's secrets, and it enables `pf`-by-uid egress filtering. But it
needs privileged provisioning (`users.users` + a per-user tmux/launchd), which agent policy
cannot perform (`sudo` prohibited), and it complicates the ccpool/tmux model. Documented as
the phase-3 upgrade path.

### claude's built-in `--sandbox` (OS Bash-tool sandbox) as the sole control

Rejected as sole control (adopt as an additional layer if available): `--permission-mode`
is a tool gate, not an OS boundary; the built-in Bash sandbox (where present) sandboxes only
claude's own tool subprocesses, is version-dependent, and its coverage of transitive
children must be verified. Use it to reinforce, not to replace, layers 1-3.

### Denylist the sensitive env vars via `tmux -e VAR=` empty overrides

Rejected as insufficient: it is a denylist (fragile — misses the next new token), and it
cannot deliver FS/network confinement. A default-DENY allowlist via an exec-time wrapper is
the robust shape.

### Restrict network egress via Seatbelt / `pf`

Rejected as a review-role control: infeasible (§ Residual risk) — the role needs GitHub +
Anthropic egress and darwin offers no clean per-hostname per-process filter.

## Related Decisions

- Builds on `docs/superpowers/plans/2026-06-23-pr-pool-deny-by-default-allowlist.md` (the
  pool-wide `dontAsk` + allowlist) — this ADR makes the allowlist per-role and records its
  overdue sign-off.
- Complements `docs/superpowers/specs/2026-06-12-ccpool-pool-isolation-design.md` (XDG/tmux
  pool scoping), which explicitly does not cover env-scrub or unprivileged execution.
- See also: bead `pg2-jpfw.9` (this work), `pg2-f9vcg` (per-role tool access follow-up),
  `pg2-yb03` (SECURITY-GATED interim `bypassPermissions` posture), `pg2-k8nx` (git SSH auth
  in the review env — reconcile with the scoped-credential decision).
