---
name: claude-code-mods
description: >-
  Use when writing, changing or debugging a Claude Code mod (a plugin that draws or extends the
  Claude Code interface through the `$` host facade): the status line, the band above the prompt,
  panes and modals, `$.env.set`, `$.config.set`, `$.session.usage()`, streaming hooks, plugin-added
  nouns, `$.process.run` or `$.fs`. Points at the findings guide, which records what was verified
  live on Claude Code 2.1.314 and what comes only from the docs, so the spike is not repeated.
---

# Claude Code mods: findings

Read the guide `docs/guides/claude-code-mods.md` (repo-relative to the agent-support repo checkout)
before writing mod code. It holds findings F1 to F12, each marked verified live or docs only, the
open items, and the spike results.

- Treat a finding marked docs only as unverified; verify it before building on it.
- The findings were observed on Claude Code 2.1.314. If the installed version differs, re-check the
  finding you depend on.
- When you learn something new about mods, append it to the guide under "Spike results" and update
  its open items.
