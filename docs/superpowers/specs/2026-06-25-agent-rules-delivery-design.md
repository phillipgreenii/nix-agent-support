# agent-rules delivery: user-level CLAUDE.md, not a plugin

**Date**: 2026-06-25
**Bead**: pg2-44sj
**Status**: Accepted

## Problem

`agent-rules@pgii-local-plugins` shipped personal always-on rules
(`pgii-agent-rules.md`) as a **plugin-root `CLAUDE.md`**. Claude Code (verified
against 2.1.186) does **not** load a plugin-root `CLAUDE.md` — the plugin loaded
`Skills(0)` / ~0 tokens, so the rules were inert. Same root cause as the
pn-workspace-rules issue (pg2-eogq).

The rules are meant to apply to **every** action in **every** session
(interactive and headless `claude -p` workers alike).

## Decision

Deliver the rules as the **user-level `~/.claude/CLAUDE.md`** ("user memory"),
written by home-manager:

```nix
home.file.".claude/CLAUDE.md".source = ./pgii-agent-rules.md;
```

Retire the `agent-rules` plugin (remove its `plugins.local.plugins` registration
and the `pgii-local-plugins/agent-rules/*` files).

## Rationale

Mechanism fit, established empirically for 2.1.186:

| Mechanism                      | Always-on body?   | Reaches `claude -p`?             | Verdict                                       |
| ------------------------------ | ----------------- | -------------------------------- | --------------------------------------------- |
| Plugin-root `CLAUDE.md`        | No (not loaded)   | No                               | inert (status quo)                            |
| Skill                          | name+desc only    | n/a                              | body is on-invoke — wrong for always-on rules |
| SessionStart hook              | yes (interactive) | hook fires in `-p` but redundant | adds machinery, no clean split                |
| **User `~/.claude/CLAUDE.md`** | **yes**           | **yes (verified)**               | **chosen**                                    |

Verification (throwaway dirs, real `~/.claude` untouched):

- A `CLAUDE.md` token was emitted by `claude -p` → user memory **is** loaded in
  headless mode.
- A SessionStart hook marker fired under `claude -p`, contradicting the docs'
  "doesn't fire in `-p`" claim — so a hook cannot scope rules to interactive-only.
  Hence the interactive-vs-autonomous split cannot be hard-enforced via hooks;
  it stays a soft, in-file instruction (which is how the file is already written).

This also yields a coherent convention: **always-on rules → user `CLAUDE.md`;
skills/plugins → nix-managed marketplace.**

## Scope / non-goals

- No per-machine enable toggle for the rules beyond `phillipgreenii.programs.claude.enable`
  (YAGNI; add an option later if a machine needs to opt out).
- The interactive/autonomous split is not mechanically enforced (no reliable
  headless signal for hooks).

## Consequences

- Rules become live via `nix` apply alone (the north star: managed by nix,
  available with only nix config).
- The previously-installed `agent-rules@pgii-local-plugins` plugin becomes an
  orphan in the runtime plugin cache once the marketplace no longer lists it.
  Bare-name `claude plugin uninstall` is risky (can remove the wrong plugin), so
  leave the ghost for careful manual cleanup (tracked with the other ghost
  cleanups, pg2-onli).
