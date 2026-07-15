# pr-pool review role: layered least-privilege execution model for untrusted PR content

**Status**: Proposed (BLOCKED on human sign-off — see Decision §5 and the companion plan)
**Date**: 2026-07-15
**Deciders**: Phillip Green II

> This ADR is **Proposed**, not Accepted. It captures the intended security-boundary /
> execution-model decision for review; implementation MUST NOT begin until a human accepts
> it and signs off on the allowlist literals. Companion design + plan:
> `docs/superpowers/plans/2026-07-15-pr-pool-untrusted-content-isolation.md` (bead
> `pg2-jpfw.9`).
>
> **Revised 2026-07-15** to fold in a blocking adversarial review (five NOT-SOUND findings,
> B1–B5). The material change from the first draft: **fetch/checkout of untrusted content
> moves OUT of the review role into a TRUSTED orchestrator pre-step**, so the review role
> holds a genuinely read-only allowlist with **no `git` verbs at all** and a `bd` grant
> narrowed to five verbs (not blanket `Bash(bd:*)`).

## Context

The pr-pool `review` role (`packages/pr-pool/internal/roles/builtin.go:83-101`) runs
`claude` autonomously over **untrusted PR HEAD content** — a teammate's or an external
contributor's branch. Today the role's own prompt instructs the model to fetch and check
out the PR head into a worktree (`builtin.go:38-39`) and post a review back via `pg-pr`.

Untrusted content can execute attacker-controlled code as soon as any code-executing tool
verb runs against the tree (`_test.go` via `go test`, `//go:generate`, `prek`/`pre-commit`
hooks, `nix` build scripts, `Makefile`/format hooks). A read-only audit (bead comment
2026-07-09), a source verification (2026-07-15), and a blocking adversarial review
(2026-07-15) established the review role's current posture and its exploitable channels:

- **Permission mode + budget watchdog: satisfied.** `dontAsk` deny-by-default
  (`config/config.go:99`), `--autonomous` blocks `AskUserQuestion`, finite wall-clock
  budget (`config/config.go:113`).
- **Tool allowlist: pool-wide, not per-role, and unsafe for review.** `roles.Role`
  (`roles/roles.go:17-25`) has no `AllowedTools` field. The single default list
  (`config/config.go:109`) grants the read-only review role `Edit`/`Write`/`git commit`,
  the full set of code-exec verbs, **`Bash(git fetch:*)`/`Bash(git checkout:*)`**, and
  blanket **`Bash(bd:*)`**. The last two are execution/exfiltration channels in their own
  right (B1/B2 below).
- **Credential / env exposure: missing.** The claude launch only ADDS env vars
  (`executor/ccpool.go:55-62`) via `tmux -e`, which cannot remove inherited variables. The
  pane inherits the tmux server's full ambient env (`SSH_AUTH_SOCK`, `GH_TOKEN`, `AWS_*`,
  internal tokens). The only scrub (`beads/runner.go:35-44`) applies to pr-pool's own `bd`
  subprocess, never to `claude`. No OS sandbox is emitted.
- **On-disk credential exposure: unmodeled.** A PR tree can contain a **symlink** to
  `~/.aws/credentials` / `~/.ssh/id_ed25519` / `~/.config/gh/hosts.yml` /
  `~/.claude/.credentials.json`; `Read`/`Grep`/`Glob` follow it and the review posts it out.
- **Worktree teardown: absent.** `worktree.Ensure` reuses idempotently and never removes;
  it also does not check out the PR head (the prompt does).

### Blocking findings resolved by this decision

- **B1** `Bash(git fetch:*)`/`Bash(git checkout:*)` are an RCE channel: `git fetch
ext::sh -c '<cmd>'` (`protocol.ext.allow` defaults to `user`) and `git fetch
--upload-pack='<cmd>' file:///…` execute arbitrary commands inside git, invisible to the
  prefix matcher; `git checkout` fires `post-checkout` hooks / gitattributes filters.
- **B2** blanket `Bash(bd:*)` is not read-only: `bd hooks install` (installs git hooks into
  the SHARED canonical clone → fires on the operator's next git op), `bd sql` (raw SQL incl.
  `DELETE`), `bd mail send` (exfil), `bd federation add-peer`+`sync` (exfil). All four
  subcommands verified present in the installed `bd`.
- **B3** correctness: `git -C {{.WorktreeDir}} fetch` does not match `Bash(git fetch:*)`
  under `dontAsk` (leading token is `git -C`), so the role can't fetch except under the
  insecure `bypassPermissions` this design replaces.
- **B4** on-disk credential exfil via a PR-tree symlink (unmodeled).
- **B5** Phase 1 (allowlist) alone is an unsafe re-enable gate — the ambient env is still
  inherited.

The hard platform constraint: **macOS/darwin has no Linux namespaces or seccomp**, so a
container-grade sandbox is not available. Neither prior design doc
(`docs/superpowers/specs/2026-06-12-ccpool-pool-isolation-design.md`;
`docs/superpowers/plans/2026-06-23-pr-pool-deny-by-default-allowlist.md`) specifies an
env-scrub or unprivileged-execution mechanism. The decision below is therefore new.

## Decision

Adopt a **layered, defense-in-depth least-privilege execution model** for the review role,
structured as a **Chain of Responsibility**: a trusted pre-step plus four independent gates
plus post-attempt teardown. No single gate is trusted to be sufficient. The primary boundary
is the trusted pre-step + tool allowlist + credential strip (mechanisms fully in our
control, not darwin-dependent); the OS sandbox is defense-in-depth and explicitly **not** a
complete boundary under an allow-default profile.

0. **Trusted orchestrator pre-step (Template Method) — resolves B1 + B3 + checkout hooks.**
   The executor already runs a trusted pre-step (`worktree.Ensure`, `executor/ccpool.go:46`)
   before creating the review session (`:63`). Extend it to fetch + hard-reset +
   `git clean -ffdx` + check out the PR head with **hardened git** — `-c
protocol.ext.allow=never -c protocol.file.allow=user -c core.hooksPath=/dev/null`, no
   attacker-controlled flags — and to write a review **diff artifact** the model reads via
   `Read`/`Grep`. The review model never runs `git fetch`/`git checkout`. Fail-closed: a
   failed pre-step does not create the session.

1. **Per-role read-only tool allowlist (Strategy) — primary cut; resolves B1/B2/B3.** Add
   an `AllowedTools` field to the role model so tool authorization is per-role, not
   pool-wide. The review role gets a MINIMAL read-only set with **no code-exec verb, no
   write verb, and no `git` verb at all**, and a `bd` grant narrowed to exactly five verbs:

   ```text
   Read,Glob,Grep,Bash(bd update:*),Bash(bd comment:*),Bash(bd close:*),Bash(bd show:*),Bash(bd children:*),Bash(pg-pr review submit:*)
   ```

   No blanket `Bash(bd:*)`, no `bd sql`/`hooks`/`mail`/`federation`. Worker/feedback roles
   keep their broader set (they are not the untrusted path).

2. **Default-deny env allowlist + claude-cred relocation (Decorator) — resolves exfil + B4.**
   A launch-time wrapper resets the environment (`env -i`) and re-exports only a fixed
   allowlist, stripping `SSH_AUTH_SOCK`/`GH_TOKEN`/`AWS_*`/internal tokens. GitHub access is
   provided by a **single narrowly-scoped review PAT** (fine-grained `contents:read` +
   `pull-requests:write` on the reviewed repos), injected **from an out-of-band secret file**
   (not the ambient env, not the keychain). The wrapper sets `CLAUDE_CONFIG_DIR` + a scratch
   `HOME` so the operator's real dotfiles (and the model OAuth token) are **absent** from
   the review's `HOME`.

3. **OS containment via `sandbox-exec` (Seatbelt) — defense-in-depth; addresses B4.** The
   same wrapper re-execs under a Seatbelt profile confining FS writes to the worktree +
   `TMPDIR` + the review-input dir and **denying reads** of the on-disk cred set:
   `~/.ssh`, `~/.aws`, `~/.config/gh`, `~/.config/gcloud`, `~/.kube`, `~/.npmrc`,
   `~/.docker/config.json`, the operator's real `~/.claude/.credentials.json`,
   `~/Library/Keychains`, and operator `.env` files outside the worktree. Under an
   allow-default profile this protects only the enumerated paths — it is a belt, not a
   complete boundary.

4. **Per-attempt worktree teardown (RAII).** Untrusted content MUST NOT persist into a
   subsequent dispatch: the executor removes the worktree + scratch branch on success, and
   the pre-step hard-resets **and** cleans a reused path before checkout (both, not
   either/or).

5. **Recorded human sign-off.** Both security-sensitive allowlist literals (the pool-wide
   default `config.go:109` and the new review-role list) require recorded sign-off: this ADR
   moves to `Accepted` with the deciding human named, and the `config.go:100` comment is
   replaced with a reference. No implementing branch merges before this.

**Re-enable gate (resolves B5).** The minimum gate to re-enable unattended review is the
**bundle** of steps 0–3 above (RCE-free per-role allowlist + trusted hardened pre-step + env
scrub/cred-relocation + FS-sandbox baseline). **Phase 1 / the allowlist alone is NOT a
gate.** Teardown (step 4) and the recorded sign-off (step 5, a merge gate) complete the
work but are not part of the minimum re-enable gate.

All controls are **fail-closed**: a missing sandbox binary, a missing scoped credential, or
a failed pre-step refuses to launch (or reviews nothing) rather than launching un-isolated.

## Consequences

### Positive

- The prompt-injection → RCE → exfiltration path is cut at multiple independent layers: the
  trusted pre-step removes the untrusted git operation from the model; the allowlist removes
  every code-exec/git verb and the dangerous `bd` subcommands; the cred strip + relocation
  remove the ambient and on-disk credentials; FS confinement + teardown are further layers.
- The review role's authority reduces to a single bright, auditable line: **zero `git`
  verbs, zero code-exec verbs, five `bd` verbs, one `pg-pr` verb, and read-only file
  access.**
- Least privilege becomes per-role: review is minimal; worker/feedback keep what they need.
  Backward-compatible (empty per-role `AllowedTools` falls back to the pool default).
- Reconciles `pg2-k8nx` (broken git SSH auth in the review env): the review model performs
  no git, so it needs no git auth; the trusted pre-step uses the scoped PAT over HTTPS.
- The long-outstanding human sign-off gate is finally structured and recordable.

### Negative

- The env-scrub + sandbox work **spans two packages** (a new `launch.Spec` seam in ccpool
  plus pr-pool wiring) and adds a nix-built launcher script — a coordinated change.
- The trusted pre-step must now generate a review **diff artifact** (the model has no `git
diff`), a small addition to the executor's responsibilities.
- The OS layer depends on a **deprecated** Apple mechanism (`sandbox-exec`); it may break on
  a future macOS and, under allow-default, is not a complete boundary.
- Managing a scoped review PAT (from a secret file) and (optionally) a dedicated user is new
  operational surface.

### Neutral

- The strongest containment (a dedicated unprivileged local user + `pf`-by-uid egress) is
  **documented as an upgrade path, deferred** — it needs a privileged, out-of-band setup
  step (agent policy forbids `sudo`), so it is out of scope for the first phases.

## Residual risk (honest darwin verdict)

- **No hard container on darwin.** Layered defense-in-depth, not namespace/seccomp-grade
  isolation. Without the deferred dedicated-user layer the review runs as the operator uid,
  so a sandbox escape regains operator authority (minus the scrubbed env).
- **Seatbelt is allow-default and enumerated.** FS deny-read protects only the listed cred
  paths; an unenumerated on-disk secret remains readable. The design does **not** claim
  Seatbelt blocks arbitrary on-disk reads.
- **Network egress cannot be meaningfully restricted for review.** The role needs egress to
  GitHub + `api.anthropic.com`; Seatbelt cannot allow-by-hostname. Exfil-over-network is
  mitigated only indirectly (no code-exec/git verb to initiate an arbitrary POST; ambient
  creds gone), NOT by an OS network boundary.
- **Steal-able secrets remain — the earlier "exfiltrated data has no value" claim is
  withdrawn.** The injected scoped review PAT, the model OAuth token (now under
  `CLAUDE_CONFIG_DIR`), and any unenumerated on-disk secret retain value; if a residual
  read+egress channel exists they are exfiltratable. The design shrinks blast radius
  (fine-grained PAT, file-sourced, no code-exec/git verb) but does not pretend the data is
  worthless.
- **Shared beads store + `.git` object store** remain writable by the review's allowed `bd`
  verbs (data-integrity vector, not RCE); the review necessarily posts attacker-influenced
  content into the PR (monitor the posted reviews). Both noted, out of scope here.

## Alternatives Considered

### Keep read-only `git diff`/`git log`/`git status`/`git rev-parse` in the review allowlist

Rejected as the primary cut. It reintroduces the A6 textconv/`--ext-diff` residual into the
model's hands and re-opens the `git -C` matcher ambiguity (B3) for those verbs. Once the
trusted pre-step provides a diff artifact, the model needs no git at all; "zero git verbs"
is a strictly brighter, more auditable boundary. The cost (no ad-hoc `git log`/`blame`) is
acceptable for a read-only review over a pre-computed diff.

### Perform the untrusted fetch/checkout in the review role under an allowlist

Rejected (this is the first draft's approach, found NOT-SOUND). `Bash(git fetch:*)` is an
RCE channel invisible to the prefix matcher (B1) and un-matchable for the `git -C` form
under `dontAsk` (B3). Moving the operation into trusted, hardened code eliminates both.

### Blanket `Bash(bd:*)` for the review's bead workflow

Rejected (B2). It admits `bd hooks install` (cross-trust-boundary RCE + persistence into the
shared canonical clone), `bd sql` (arbitrary SQL), `bd mail send`, and `bd federation
add-peer`+`sync` (exfil). Narrowed to five leading-token verbs.

### Linux-style container (namespaces + seccomp / bubblewrap)

Rejected: not available on darwin, the deployment platform. Revisit only if review execution
moves to a Linux host.

### Dedicated unprivileged local user as the primary mechanism, now

Deferred (not rejected): the **strongest** control — the review uid structurally cannot read
the operator's per-user secrets, and it enables `pf`-by-uid egress filtering. But it needs
privileged provisioning (`users.users` + a per-user tmux/launchd), which agent policy cannot
perform (`sudo` prohibited), and it complicates the ccpool/tmux model. Documented as the
upgrade path.

### claude's built-in `--sandbox` (OS Bash-tool sandbox) as the sole control

Rejected as sole control (adopt as an additional layer if available): `--permission-mode` is
a tool gate, not an OS boundary; the built-in Bash sandbox (where present) sandboxes only
claude's own tool subprocesses, is version-dependent, and its coverage of transitive
children must be verified.

### Denylist the sensitive env vars via `tmux -e VAR=` empty overrides

Rejected as insufficient: it is a denylist (fragile — misses the next new token), and it
cannot deliver FS/network confinement or claude-cred relocation. A default-DENY allowlist
via an exec-time wrapper is the robust shape.

## Related Decisions

- Builds on `docs/superpowers/plans/2026-06-23-pr-pool-deny-by-default-allowlist.md` (the
  pool-wide `dontAsk` + allowlist) — this ADR makes the allowlist per-role and records its
  overdue sign-off.
- Complements `docs/superpowers/specs/2026-06-12-ccpool-pool-isolation-design.md` (XDG/tmux
  pool scoping), which explicitly does not cover env-scrub or unprivileged execution.
- See also: bead `pg2-jpfw.9` (this work), `pg2-f9vcg` (per-role tool access follow-up),
  `pg2-yb03` (SECURITY-GATED interim `bypassPermissions` posture), `pg2-k8nx` (git SSH auth
  in the review env — resolved by moving git to the trusted pre-step with the scoped PAT).
