---
name: markdown-conventions
description: Prettier-safe backtick-wrapping convention for glob patterns, cron expressions, underscored paths, and Python identifiers in Markdown.
paths: ["**/*.md"]
---

# Markdown Authoring Conventions

Moved out of the repo's always-on `CLAUDE.md` (tc-ql0o Stage D, 2026-08-26): this detail only
matters while editing a Markdown file.

This project uses prettier to format `*.md` files. Always wrap glob patterns, cron expressions,
file paths with underscores, and Python identifiers in backticks to prevent prettier from
interpreting them as markdown emphasis or bold markup.
