# Static nix-built marketplace for agent-support local plugins

**Status**: Accepted
**Date**: 2026-06-25
**Deciders**: phillipg

## Context

ADR-0003 established `pgii-local-plugins`: each plugin module registered itself
via `phillipgreenii.programs.claude.plugins.local.plugins.<name>` and the
`home/programs/pgii-local-plugins` module assembled `marketplace.json` +
`settings.json` keys at **home-manager eval time**. Every plugin shared one
date-based version (`self.lib.pluginVersion` = `0.YYYY.MMDDHHMMSS`), so any
commit to the repo bumped every plugin's version and busted Claude's plugin
cache — pure churn unrelated to whether the plugin's content changed.

Meanwhile `phillipg-nix-repo-base` introduced `mkClaudeMarketplace` (see
repo-base ADR-0010 + `docs/claude-marketplaces.md`): a builder that bundles a
committed marketplace tree into the store with **per-plugin content-digest**
versions (`<declared>+<digest>` — changes iff that plugin's own content
changes), plus a consumer module (`marketplaces.nixProvided`) that registers a
built marketplace. repo-base's own `pn-workspace-rules` ships this way.

The blocker to reusing it for agent-support was the assumption that the local
plugins were irreducibly "dynamic" (content sourced from packages + manifests
generated at eval time). On inspection that was mostly legacy: `pg-pr` and
`bash-lsp` were already effectively static (committed content, bare-command
references), and the only genuinely build-specific data was the two hook
plugins' `hooks.json` referencing nix-store binary paths.

## Decision

Migrate agent-support's local plugins to a **static, committed marketplace tree
built by repo-base's existing `mkClaudeMarketplace { src }`** — no new builder
construction mode.

1. A committed `claude-marketplace/` tree holds `.claude-plugin/marketplace.json`
   (`name = "phillipgreenii-nix-agent-support"`) + one dir per plugin
   (`.claude-plugin/plugin.json` + `skills`/`commands`/`agents`/`hooks`). The
   flake builds it as the package `phillipgreenii-nix-agent-support-marketplace`
   (installed identity `phillipgreenii-nix-agent-support-marketplace-local`) and
   registers it through `marketplaces.nixProvided` in `homeModules.default` —
   the same consumer path repo-base's marketplace uses.
2. **Per-plugin content-digest versions** replace the shared date version;
   `self.lib.pluginVersion` and the `plugins.local` option are retired.
3. **Hook plugins reference their binaries by BARE name** in the committed
   `hooks.json` (`claude-work-start`, `claude-extended-tool-approver`), not an
   absolute `/nix/store` path — an absolute store path is build/machine-specific
   and cannot be committed. This matches the pre-existing `pg-pr`/`bash-lsp`
   convention. Each plugin's HM module remains responsible for installing its
   binary on `PATH`; for `claude-extended-tool-approver` the module installs the
   (optionally `inputProcessor`-wrapped) binary so the bare command resolves to
   it.
4. **`defaultEnabled` is set per plugin** in `plugin.json` (the builder defaults
   absent ⇒ `false`): `true` for the five always-on plugins (bash-lsp,
   bash-scripting, bead-grooming, claude-activity, claude-extended-tool-approver)
   and `false` for `pg-pr` (it was opt-in and depends on the `pg-pr` CLI; enable
   per-machine via `marketplaces.overrides`).

## Consequences

### Positive

- One delivery mechanism shared with repo-base; the marketplace is content-digest
  versioned, so an unrelated repo edit no longer rebuilds/cache-busts every
  plugin. The tree is static and inspectable; no IFD.
- Drops the bespoke `pgii-local-plugins` aggregate module + the duplicated
  per-module version-stamping; content-only packages (`bash-scripting`,
  `pg-pr-plugin`) and their HM modules are removed (content lives in the tree).

### Negative

- One-time re-enable churn: the marketplace name changes from `pgii-local-plugins`
  to `phillipgreenii-nix-agent-support-marketplace-local`, so Claude installs the
  new `name@…-marketplace-local` keys fresh (same class of one-time churn ADR-0003
  itself incurred).
- Hook commands now resolve via `PATH` rather than an absolute store path: a
  machine that enables a hook plugin MUST also install its binary (its module's
  `enable`). Less hermetic than a baked store path; chosen for staticness and to
  match the existing `pg-pr`/`bash-lsp` convention.
- `pg-pr`'s default flips from "present when its module is enabled" to
  "present-but-disabled by default, opt-in via override".

### Neutral

- `pgii-claude-plugins` (the third-party marketplace module) is unaffected.
- `agent-rules` is **not a plugin** and is not part of this marketplace — it is
  the user-level always-on rules, delivered by writing the personal rules to the
  user `~/.claude/CLAUDE.md` ("user memory"), which Claude Code loads in every
  session (interactive and headless `claude -p`, verified against 2.1.186). User
  `CLAUDE.md` is the single, canonical always-on delivery. See the agent-rules
  delivery design (commit `5c790bf`).
- During this migration `agent-rules` was briefly mis-shipped as a SessionStart
  **hook plugin** (commit `63a696b`, pg2-44sj) added to this marketplace, on the
  assumption a plugin/hook was needed for always-on rules. That was confusion
  carried over from the plugin migration, not a new decision: because user
  `CLAUDE.md` **is** loaded, the hook injected the same ~4 KB of rules a second
  time (byte-identical double-injection, redundant token cost). pg2-qewh removed
  the hook plugin and restored user `CLAUDE.md` as the sole delivery; the
  agent-rules delivery design (commit `5c790bf`) was correct all along.

## Alternatives Considered

### A second `mkClaudeMarketplace` construction mode `{ name; owner; plugins }`

repo-base's plan deferred this "aggregate from pre-built plugin derivations" mode
to this migration. It is unnecessary once the plugins are expressed statically —
the existing `{ src }` mode suffices — and it would have reintroduced
content-behind-a-derivation (IFD pressure). Rejected.

### Static content + build-time store-path substitution for hermetic hooks

Keep absolute (wrapped) store paths in `hooks.json` by substituting them at build
time. This keeps hooks `PATH`-independent but requires per-plugin build machinery
(or the second construction mode). Rejected in favor of bare-`PATH` commands,
which were already the repo precedent.

## Related Decisions

Supersedes ADR-0003 (Single pgii-local-plugins Marketplace).

Refines ADR-0006 (enabledByDefault + overrides): per-plugin enable control is now
`plugin.json` `defaultEnabled` resolved through
`phillipgreenii.programs.claude.marketplaces.overrides`.

See also: phillipg-nix-repo-base docs/adr/0010-claude-marketplace-builder-and-identity.md
See also: phillipg-nix-repo-base docs/claude-marketplaces.md
